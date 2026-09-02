package process

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"syscall"
	"time"
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

// ErrStopUnconfirmed means the runtime accepted termination signals but the
// replica did not report an exit within the bounded grace windows. Callers
// replacing a slot must not start a successor at the same index in this state.
var ErrStopUnconfirmed = errors.New("replica stop unconfirmed")

// ErrExternalLogsThrottled marks a provider rate-limit response. API callers
// use it to return a bounded Retry-After hint without exposing provider errors.
var ErrExternalLogsThrottled = errors.New("external logs throttled")

// ReplicaEndpoint is the result of starting a replica: where the proxy routes
// to it, which provider owns it, a stable worker identity used for recovery,
// and the operational RunHandle for Signal/Wait/Stats/removal. A remote runtime
// returns a non-loopback URL here; local runtimes return http://127.0.0.1:<port>.
type ReplicaEndpoint struct {
	URL          string        // route URL, e.g. "http://127.0.0.1:34521"
	Provider     string        // runtime provider, e.g. native, docker, fargate, scaleway_serverless
	WorkerID     string        // stable identity: PID (stringified), container ID, task ARN
	Handle       RunHandle     // operational handle
	ExternalLogs *ExternalLogs // provider-owned log destination, when ShinyHub cannot stream output
	// StartupGuard is non-nil only for a guarded native launch. The child blocks
	// before exec until the control plane acknowledges durable PID persistence.
	StartupGuard io.WriteCloser
}

// ExternalLogs is a durable handoff to a provider-owned logging surface.
// Resource remains useful when ConsoleURL cannot be constructed (for example,
// an older short ECS task ARN), while region and cluster support a copyable CLI
// recovery path. The Manager records this on the immutable run so the handoff
// survives process exit, scale-down, and control-plane restart.
type ExternalLogs struct {
	Provider   string `json:"provider"`
	Resource   string `json:"resource"`
	Region     string `json:"region,omitempty"`
	Cluster    string `json:"cluster,omitempty"`
	LogGroup   string `json:"log_group,omitempty"`
	LogStream  string `json:"log_stream,omitempty"`
	LogURL     string `json:"log_url,omitempty"`
	ConsoleURL string `json:"console_url,omitempty"`
}

// ExternalLogEvent is one provider-retained application log event. Timestamp
// is provider time rather than ShinyHub receipt time, so callers can preserve
// ordering when polling multiple remote streams.
type ExternalLogEvent struct {
	Message   string    `json:"message"`
	Timestamp time.Time `json:"timestamp"`
}

// ExternalLogPage is a bounded provider response. NextCursor is opaque to
// ShinyHub and the browser; passing it back resumes strictly after this page.
type ExternalLogPage struct {
	Events     []ExternalLogEvent `json:"events"`
	NextCursor string             `json:"next_cursor,omitempty"`
}

// ExternalLogReader retrieves provider-owned logs on demand. Implementations
// must treat ExternalLogs as untrusted persisted input and validate the
// provider-specific identifiers before making an external request.
type ExternalLogReader interface {
	Read(context.Context, ExternalLogs, string, int32) (ExternalLogPage, error)
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
	// Stats returns CPU usage and RSS bytes for the handle. cpuPercent is a rate
	// over the interval since this runtime's previous Stats call for the same
	// handle, on the scale where 100 means one fully busy core. It is nil when
	// no rate can be computed yet, which an implementation must report rather
	// than rounding down to 0: a caller cannot otherwise tell a missing baseline
	// from an idle process.
	Stats(ctx context.Context, handle RunHandle) (cpuPercent *float64, rssBytes uint64, err error)
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

// LifetimeFileInheritor is implemented by local runtimes that can pass host
// descriptors into a process tree. Jobs use it to keep publication and
// consumer flocks tied to physical process lifetime across a control-plane
// crash. Container and remote runtimes use their own recovery fences.
type LifetimeFileInheritor interface {
	InheritsLifetimeFiles() bool
}

// DurableDataReporter is an optional capability for runtimes whose per-app data
// dir may NOT survive a restart or be shared across replicas. A runtime that
// does not implement it is treated as durable (native, docker, and remote
// workers all back the data dir with a persistent host directory). Only the
// Fargate runtime implements it, returning false unless a durable backend
// (S3 Files, or an operator-asserted volume) is configured. The durable-data
// Fargate and serverless runtimes implement it. The durable-data guard consumes
// it via Manager.TierHasDurableDataFor to block deploying a data-using app onto
// a tier that would silently lose its data.
type DurableDataReporter interface {
	// TierHasDurableData reports whether app-data on this tier survives task
	// restart/hibernation and is shared across replicas.
	TierHasDurableData() bool
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
// Running means "logically active", which is distinct from routability. For
// Fargate, provisioning/pending tasks are active before they have an IP. For a
// scale-to-zero service, the retained definition may be active while no backing
// instance exists. Consumers that need "routable now" must also check URL != "".
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

// AppResourceCleaner is an optional capability for runtimes that persist
// provider-owned resources beyond a replica's process lifetime. CleanupApp is
// invoked when an app is deleted and by startup tombstone reconciliation. It
// must be idempotent: a missing or already-deleted resource is success.
type AppResourceCleaner interface {
	CleanupApp(ctx context.Context, appID int64) error
}

// ManagedRuntime is the provider-neutral contract for remotely managed,
// hibernatable replicas. A managed runtime can start and stop replicas through
// Runtime, reconstruct live state after a control-plane restart through
// ReplicaInventory, and remove retained provider resources when the app is
// deleted through AppResourceCleaner.
//
// Providers may implement sleep differently. Fargate terminates the task;
// request-driven serverless providers may retain the service definition while
// their underlying instance scales to zero. Callers depend only on the
// observable contract: after Signal+Wait the replica is no longer reported as
// running, and a later Start makes it routable again.
type ManagedRuntime interface {
	Runtime
	ReplicaInventory
	AppResourceCleaner
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

// DeploymentWarmReadopter is the generation-aware extension used when an
// adopted process may have been launched beside another generation.
type DeploymentWarmReadopter interface {
	ReadoptDeploymentWarm(slug string, deploymentID int64, index, pid int) error
}

// ResourceEnforcer is the optional runtime capability to report whether per-app
// memory/CPU limits are ACTUALLY enforced on this host. Only the native runtime
// implements it (enforcement is best-effort, gated on cgroup v2 delegation);
// container/remote runtimes always enforce, so they do not implement it and the
// manager treats them as enforcing.
type ResourceEnforcer interface {
	// ResourceEnforcement reports whether memory and cpu limits are enforced.
	ResourceEnforcement() (memory, cpu bool)
}

// CgroupReadopter re-registers a replica's per-app resource-limit cgroup after a
// server restart, INDEPENDENT of warm-wake. Without it, an adopted replica that
// has a memory/CPU limit (but no warm-wake) loses its cgroup mapping, so the
// runtime can neither tear it down nor detect an OOM-kill for it. Only the native
// runtime implements it; container runtimes hold this state in their daemon.
type CgroupReadopter interface {
	// ReadoptCgroup re-registers the deterministic app-<slug>-<index> cgroup for
	// an adopted pid when it exists and still contains the pid, and seeds its OOM
	// baseline. Best-effort: returns nil when there is no such cgroup (no limit
	// was set, or warm-wake is off and no base exists).
	ReadoptCgroup(slug string, index, pid int) error
}

// DeploymentCgroupReadopter re-registers either a generation-scoped or legacy
// cgroup for an adopted deployment process.
type DeploymentCgroupReadopter interface {
	ReadoptDeploymentCgroup(slug string, deploymentID int64, index, pid int) error
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

// ProcessExitError carries a runtime-reported exit code through Runtime.Wait.
// Long-running replicas are expected not to exit at all, so code 0 is still an
// exit event worth surfacing rather than a successful Wait to discard.
type ProcessExitError struct {
	Code int
}

func (e *ProcessExitError) Error() string { return fmt.Sprintf("process exited with code %d", e.Code) }

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

// ContainerInfo is a summary of a managed container used during recovery and
// orphan cleanup. State is the Docker state (for example "running" or
// "exited"); an empty state is tolerated for non-Docker test/runtime adapters.
type ContainerInfo struct {
	ID     string
	Labels map[string]string
	State  string
}

// TaskRef identifies one Fargate task returned by a FargateTaskSweeper. It
// lives in the process package so both fargate.Runtime and lifecycle can use
// it without an import cycle (fargate imports process; lifecycle imports both
// fargate and process; placing TaskRef here breaks the fargate->lifecycle
// direction that would otherwise form a cycle).
type TaskRef struct {
	ARN    string
	Labels map[string]string
}
