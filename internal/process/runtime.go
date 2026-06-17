package process

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"syscall"
)

// ErrNoLiveWorker is returned (wrapped) when a tier-bound remote runtime has no
// live worker to place a replica on. The watcher treats it as a zero-cost
// failure: a missing worker is an infrastructure gap, not the app's fault, so it
// must not consume the crash-restart budget.
var ErrNoLiveWorker = errors.New("no live worker for tier")

// ErrReplicaAlreadyRunning is returned (wrapped) by Manager.Start when the
// target slug+index slot is already running. The watcher treats it as zero-cost:
// a re-placement that races a slot already (re)filled is a no-op, not a failure.
var ErrReplicaAlreadyRunning = errors.New("replica already running")

// ErrReplicaNotFound is returned (wrapped) by Manager.StopReplica when the
// slug+index slot has no live entry. Callers that distinguish an already-gone
// replica from a real stop failure (e.g. autoscale scale-down) match this
// sentinel: a missing entry is benign, while any other error means the replica
// may still be running and its control-plane state must be left intact.
var ErrReplicaNotFound = errors.New("replica not found")

// ReplicaEndpoint is the result of starting a replica: where the proxy routes
// to it, which provider owns it, a stable worker identity used for recovery,
// and the operational RunHandle for Signal/Wait/Stats/removal. A remote runtime
// returns a non-loopback URL here; local runtimes return http://127.0.0.1:<port>.
type ReplicaEndpoint struct {
	URL      string    // route URL, e.g. "http://127.0.0.1:34521"
	Provider string    // "native" | "docker" (future: "remote_docker" | "fargate")
	WorkerID string    // stable identity: PID (stringified), container ID, task ARN
	Handle   RunHandle // operational handle
}

// Runtime abstracts how app processes are started and managed.
// NativeRuntime uses exec.Command; DockerRuntime uses the Docker Engine API.
type Runtime interface {
	// Start spawns a new process. logWriter receives combined stdout+stderr.
	// The returned ReplicaEndpoint carries the route URL the proxy must use,
	// the provider name, a durable worker identity, and the operational handle.
	Start(ctx context.Context, p StartParams, logWriter io.Writer) (ReplicaEndpoint, error)
	// Signal sends sig to the process or container identified by handle.
	Signal(handle RunHandle, sig syscall.Signal) error
	// Wait blocks until the process or container identified by handle exits.
	Wait(ctx context.Context, handle RunHandle) error
	// Stats returns CPU usage (percent, 0–100+) and RSS bytes for the handle.
	Stats(ctx context.Context, handle RunHandle) (cpuPercent float64, rssBytes uint64, err error)
	// RunOnce spawns a short-lived process from the same bundle/runtime context
	// as Start, blocks until it exits or ctx is cancelled, and returns the
	// exit info. Implementations MUST signal SIGTERM on ctx cancel and
	// SIGKILL after a 10-second grace.
	RunOnce(ctx context.Context, p StartParams, logWriter io.Writer) (ExitInfo, error)
	// HostPreparesDeps reports whether bundle dependencies (uv sync,
	// renv::restore) should be installed on the host before Start. Native
	// runtimes use the host's PATH and need this; container runtimes prepare
	// deps inside the image/container, so callers must NOT touch the host.
	HostPreparesDeps() bool
	// AppBindHost reports the address an app process should bind its listening
	// socket to. Native and Docker host-network runtimes return "127.0.0.1" so
	// only the in-process proxy can reach the app. Docker bridge-network
	// runtimes return "0.0.0.0" so the published port mapping (which lives in
	// the container's separate network namespace) is reachable from the host.
	AppBindHost() string
	// HostProvidesAppData reports whether the host running this Manager is
	// responsible for provisioning the per-app data directory and shared-mount
	// host paths. Local runtimes (native, docker on the control-plane host)
	// return true. Remote runtimes return false: the worker provisions its own
	// app-data, so the Manager must not create host directories or symlinks and
	// must strip host paths before dispatching Start.
	HostProvidesAppData() bool
}

// ReplicaTransporter is an optional capability for runtimes that route replica
// traffic through a non-default HTTP transport (for example a remote worker's
// mTLS tunnel). The proxy and health-check paths use this transport so that
// requests to the replica's reported URL authenticate correctly. The transport
// is per-worker: a tier may have several workers, and each replica's route must
// use the mTLS transport of the worker that actually hosts it.
type ReplicaTransporter interface {
	// ReplicaTransportForWorker returns the RoundTripper to use when dialing
	// replicas hosted by the named worker, or nil to use the default transport
	// (also returned when the worker is not a live host on this runtime's tier).
	ReplicaTransportForWorker(nodeID string) http.RoundTripper
}

// InventoryItem describes one managed container as reported by a remote
// runtime's inventory. Recovery reconciles a replica row against these items by
// matching the slug/replica_index/deployment_id labels, then routes to URL.
// WorkerID names the worker that reported the container; with inventory
// aggregated across a tier's coexisting workers, recovery uses it to bind a
// replica row to its owning worker's container, so a same-labeled container on
// another worker is not adopted with the wrong worker's URL, handle, and
// transport.
//
// Running means "not stopped": for the Fargate runtime a task in PROVISIONING,
// PENDING, or RUNNING state is reported as Running=true. Only STOPPED tasks
// are Running=false. This is intentional: a Fargate task that has not yet
// acquired an IP is not yet routable, but it is NOT gone and must not trigger
// re-placement. Consumers that need "routable now" must check URL != "" in
// addition to Running.
type InventoryItem struct {
	ContainerID string
	Labels      map[string]string
	// Running is true for any task not in STOPPED state (PROVISIONING, PENDING,
	// or RUNNING). It is false only when the task has terminated. Consumers
	// that need a routable URL must additionally check URL != "".
	Running  bool
	URL      string
	WorkerID string
}

// ReplicaInventory is an optional capability for runtimes that can enumerate
// their live replicas without a host PID (remote workers). RecoverProcesses
// uses it to reconcile remote tiers by deployment id instead of InspectPID.
type ReplicaInventory interface {
	Inventory(ctx context.Context) ([]InventoryItem, error)
}

// ErrRuntimeNotSnapshotter is returned (wrapped) by Manager.Suspend/Resume when
// the replica's tier runtime does not implement Snapshotter. Callers fall back
// to Stop (hibernate) or a cold RunReplica (wake).
var ErrRuntimeNotSnapshotter = errors.New("runtime does not support snapshot")

// ErrReplicaNotSuspended is returned (wrapped) by Manager.Resume when the target
// slot is not in a suspended state, so there is nothing to resume.
var ErrReplicaNotSuspended = errors.New("replica not suspended")

// Snapshotter is an optional capability for runtimes that can freeze a running
// replica's warmed memory and restore it, skipping a cold restart on wake. A
// runtime that does not implement it uses the existing stop/cold-start path.
//
// A runtime that implements Snapshotter must also ensure its Stop/Signal path can
// terminate a SUSPENDED replica - a frozen resource (e.g. a paused cgroup) does
// not deliver SIGTERM until it is thawed, so the runtime must unfreeze before
// killing (or kill the frozen resource directly). Until a real Snapshotter
// runtime is registered, suspended state never arises in production: Manager.
// Suspend returns ErrRuntimeNotSnapshotter and the watcher falls back to Stop.
type Snapshotter interface {
	// Suspend freezes the replica identified by handle and tries to release its
	// warmed memory from host RAM. It returns freed=true ONLY when the warmed
	// memory was actually released (driver-defined threshold). On any result
	// other than (true, nil) - freed=false OR err != nil, including an error
	// after a partial freeze - the driver MUST first restore the replica to a
	// normally stoppable state so the caller's Stop path works without
	// special-casing a frozen cgroup. The handle stays valid for a later Resume
	// only on (true, nil).
	Suspend(ctx context.Context, handle RunHandle) (freed bool, err error)

	// Resume restores a previously suspended replica and returns its route
	// endpoint (the same URL when the driver preserves the process/port in
	// place). Resume MUST be idempotent: if the replica is already serving,
	// return the current endpoint and nil error. On a genuine error the driver
	// MUST tear down the stale frozen/suspended resource before returning, so the
	// caller's cold-boot fallback cannot collide with or leak it.
	Resume(ctx context.Context, handle RunHandle) (ReplicaEndpoint, error)
}

// WarmReadopter is implemented by a runtime whose warm-wake state is held in
// this process's memory and is therefore lost when a replica is re-adopted after
// a server restart - the native runtime, whose per-app cgroup mapping Adopt does
// not rebuild the way Start does. Manager.Adopt calls ReadoptWarm best-effort so
// a re-adopted replica can be warm-frozen and warm-resumed again. Runtimes whose
// warm state survives a restart independently (e.g. Docker's daemon-held paused
// containers) do not implement it.
type WarmReadopter interface {
	// ReadoptWarm re-registers the warm-wake state for an adopted replica. It
	// returns ErrRuntimeNotSnapshotter when warm-wake is unavailable (the caller
	// stays silent); any other error means the warm state could not be rebuilt
	// (the caller logs and the replica hibernates via Stop).
	ReadoptWarm(slug string, index, pid int) error
}

// PartialInventoryError reports that a tier's aggregated inventory is
// incomplete: at least one worker was queried successfully, but Workers could
// not be reached. The returned items hold what the reachable workers reported.
// Recovery uses Workers to distinguish a replica whose container is genuinely
// gone (its owning worker reported and the container was absent) from one whose
// owning worker was merely unreachable (status unknown); the latter must not
// drive a live app to stopped.
type PartialInventoryError struct {
	Workers []string
}

func (e *PartialInventoryError) Error() string {
	return fmt.Sprintf("inventory incomplete: %d worker(s) unreachable: %v", len(e.Workers), e.Workers)
}

// ExitInfo summarizes how a one-shot process ended.
type ExitInfo struct {
	Code     int  // exit code; -1 if Signaled
	Signaled bool // true if killed by signal (e.g. SIGKILL after timeout)
}

// SharedMount is a read-only mount of another app's data dir into the consumer.
type SharedMount struct {
	SourceSlug string // for path naming under data/shared/<source-slug>
	HostPath   string // absolute path on the host (the source app's app-data dir)
}

// RunHandle identifies a running app instance.
// Exactly one field is non-zero depending on the runtime in use.
type RunHandle struct {
	PID         int    // set by NativeRuntime
	ContainerID string // set by DockerRuntime
}

// ContainerInfo is a summary of a running container used during process recovery.
type ContainerInfo struct {
	ID     string
	Labels map[string]string
}

// TaskRef identifies one Fargate task returned by a FargateTaskSweeper. It
// lives in the process package so both fargate.Runtime and lifecycle can use
// it without an import cycle (fargate imports process; lifecycle imports both
// fargate and process; placing TaskRef here breaks the fargate->lifecycle
// direction that would otherwise form a cycle).
type TaskRef struct {
	ARN string
}
