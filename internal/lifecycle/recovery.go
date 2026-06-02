package lifecycle

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"path/filepath"
	"strconv"
	"syscall"
	"time"

	gops "github.com/shirou/gopsutil/v4/process"

	"github.com/rvben/shinyhub/internal/db"
	"github.com/rvben/shinyhub/internal/deploy"
	"github.com/rvben/shinyhub/internal/fargate"
	"github.com/rvben/shinyhub/internal/process"
	"github.com/rvben/shinyhub/internal/proxy"
)

// ReconcileInflightDeployments fails any deployment still in 'pending' at
// startup. A pending row means a deploy or rollback was interrupted before
// the new pool was confirmed; failing it ensures process recovery falls back
// to the last good deployment instead of adopting a half-applied one. Must
// run before RecoverProcesses.
func ReconcileInflightDeployments(store *db.Store) {
	inflight, err := store.ListInflightDeployments()
	if err != nil {
		slog.Error("deploy reconcile: list inflight deployments", "err", err)
		return
	}
	for _, d := range inflight {
		if err := store.FailDeployment(d.ID); err != nil {
			slog.Error("deploy reconcile: fail interrupted deployment", "id", d.ID, "app_id", d.AppID, "err", err)
			continue
		}
		slog.Warn("deploy reconcile: failed interrupted deployment", "id", d.ID, "app_id", d.AppID, "version", d.Version)
	}
}

// validateNativeProcess confirms a recorded PID is still this app's replica
// and is serving on the recorded port before the proxy is wired to it.
//
// A bare "is the PID alive" check is not enough: after a host reboot or a
// crash the PID may have been reused by an unrelated process, and the
// recorded port row may be stale. Either case would make the proxy forward
// /app/<slug>/ to whatever now answers there.
//
// On Linux (the production target) the process working directory is read
// from /proc/<pid>/cwd and must equal the app's active bundle dir — an
// unrelated reused PID will not be running there. If the working directory
// cannot be read on this platform the check degrades to the port-liveness
// probe alone, which still rejects stale port rows.
func validateNativeProcess(pid, port int, bundleDir string) error {
	p, err := gops.NewProcess(int32(pid))
	if err != nil {
		return fmt.Errorf("pid %d not found: %w", pid, err)
	}
	if bundleDir != "" {
		switch cwd, cwdErr := p.Cwd(); {
		case cwdErr != nil:
			slog.Warn("recovery: cannot read process cwd; skipping identity check",
				"pid", pid, "err", cwdErr)
		default:
			want, _ := filepath.Abs(bundleDir)
			got, _ := filepath.Abs(cwd)
			if want != got {
				return fmt.Errorf("pid %d cwd %q does not match bundle %q (pid reuse?)", pid, got, want)
			}
		}
	}
	addr := net.JoinHostPort("127.0.0.1", strconv.Itoa(port))
	conn, err := net.DialTimeout("tcp", addr, 750*time.Millisecond)
	if err != nil {
		return fmt.Errorf("port %d not accepting connections: %w", port, err)
	}
	_ = conn.Close()
	return nil
}

// activeBundleDir returns the bundle directory of the app's most recent
// deployment, or "" if it cannot be resolved (validation then falls back to
// the port probe only).
func activeBundleDir(store *db.Store, appID int64) string {
	deps, err := store.ListDeployments(appID)
	if err != nil || len(deps) == 0 {
		return ""
	}
	return deps[0].BundleDir
}

// ContainerLister is implemented by DockerRuntime to support recovery.
// NativeRuntime does not implement it; pass nil for native mode.
type ContainerLister interface {
	ListByLabel(labelFilter string) ([]process.ContainerInfo, error)
	InspectPID(containerID string) (int, error)
}

// RecoverProcesses re-adopts running app processes after a server restart.
// Each replica is routed to its tier's runtime (via mgr.RuntimeForTier): a
// container-backed runtime (one that implements ContainerLister) is recovered
// by matching live containers to the replica's labels, every other runtime by
// validating the recorded PID. A single app's replicas may therefore span a
// native default tier and a container-backed burst tier. defaultMaxSessions is
// the runtime-wide session-cap fallback applied when an app has
// max_sessions_per_replica == 0.
func RecoverProcesses(store *db.Store, mgr *process.Manager, prx *proxy.Proxy, defaultMaxSessions int) {
	apps, err := store.ListRunningApps()
	if err != nil {
		slog.Error("process recovery: list running apps", "err", err)
		return
	}

	// Query each container-backed runtime at most once, even when several tiers
	// or apps share the same daemon, by caching its container list keyed on the
	// lister itself (DockerRuntime is a pointer, so the interface is comparable).
	containerCache := map[ContainerLister][]process.ContainerInfo{}
	listContainers := func(l ContainerLister) []process.ContainerInfo {
		if cs, ok := containerCache[l]; ok {
			return cs
		}
		cs, err := l.ListByLabel(`{"label":["` + process.LabelSlug + `"]}`)
		if err != nil {
			slog.Error("recovery: list docker containers", "err", err)
			cs = nil
		}
		containerCache[l] = cs
		return cs
	}

	// Query each remote tier's agent inventory at most once, shared across every
	// app that places replicas on that tier. unreachable holds the workers whose
	// inventory could not be fetched (a partial outage); a replica owned by one of
	// them has an unknown state and must not be reconciled as dead.
	type tierInventory struct {
		items       []process.InventoryItem
		unreachable map[string]bool // workers whose inventory failed (partial outage)
		allDown     bool            // whole-tier inventory failure: every replica indeterminate
	}
	remoteInventory := map[string]tierInventory{}
	getInventory := func(tier string, inv process.ReplicaInventory) tierInventory {
		if ti, ok := remoteInventory[tier]; ok {
			return ti
		}
		items, err := inv.Inventory(context.Background())
		var ti tierInventory
		var partial *process.PartialInventoryError
		switch {
		case errors.As(err, &partial):
			ti.unreachable = make(map[string]bool, len(partial.Workers))
			for _, nodeID := range partial.Workers {
				ti.unreachable[nodeID] = true
			}
			ti.items = items
			slog.Warn("recovery: partial remote inventory", "tier", tier, "unreachable", partial.Workers)
		case err != nil:
			// Whole-tier failure (every up worker unreachable, or none up): the
			// tier's state is unknown, so no replica on it may be reconciled as
			// dead or drive its app to stopped.
			ti.allDown = true
			slog.Error("recovery: remote inventory", "tier", tier, "err", err)
		default:
			ti.items = items
		}
		remoteInventory[tier] = ti
		return ti
	}

	for _, app := range apps {
		reps, err := store.ListReplicas(app.ID)
		if err != nil || len(reps) == 0 {
			markRecoveryStopped(store, app.Slug)
			continue
		}
		prx.SetPoolSize(app.Slug, app.Replicas)
		prx.SetPoolCap(app.Slug, deploy.ResolveMaxSessionsPerReplica(app.MaxSessionsPerReplica, defaultMaxSessions))
		bundleDir := activeBundleDir(store, app.ID)

		anyAlive := false
		indeterminate := false
		healable := false
		for _, r := range reps {
			if r.Status == db.ReplicaStatusLost {
				// A lost replica is never re-adopted; the next deploy re-places it.
				continue
			}
			rt := mgr.RuntimeForTier(r.Tier)
			if inv, ok := rt.(process.ReplicaInventory); ok {
				ti := getInventory(r.Tier, inv)
				if ti.allDown || ti.unreachable[r.WorkerID] {
					// The worker owning this replica could not be queried (its
					// worker failed, or the whole tier is unreachable); its
					// container may still be running, so this slot must not drive
					// the app to stopped. Keep the app out of stopped
					// (indeterminate) so it stays reconcilable.
					indeterminate = true
					// Only enter the slot into the lost-replica healing path once
					// its owning worker is actually declared down/revoked (or its
					// row is gone). A worker still up is merely unreachable for this
					// one-shot startup scan (a transient blip); marking it lost
					// would let the watcher's tier-gated healing re-place the slot
					// onto a sibling worker while the original container keeps
					// running, orphaning it. The WorkerDownMonitor owns the
					// up->down transition and will lose a still-up worker's replicas
					// only if its heartbeat genuinely goes stale. An already-down
					// worker is handled here because ListWorkersStale skips down
					// rows, so the monitor never re-loses it.
					if workerDeclaredGone(store, r.WorkerID) && markReplicaLostPreservingIdentity(store, app, r) {
						healable = true
					}
					continue
				}
				if recoverRemoteReplica(store, mgr, prx, app, r, ti.items) {
					anyAlive = true
					continue
				}
				// The replica was not adopted from a reachable inventory: the
				// owning worker was queried (it is neither allDown nor in the
				// partial-outage unreachable set) and reported no live container
				// for this slot. If that worker has since been declared
				// down/revoked, or its row was reaped, the WorkerDownMonitor will
				// never (re-)lose this slot because ListWorkersStale skips
				// already-down rows, so enter it into the lost-replica healing
				// path here and let the watcher re-place it onto a healthy
				// sibling. A still-up owner whose container merely vanished is left
				// untouched for the watcher's own reconciliation rather than forced
				// lost on this one-shot scan.
				if workerDeclaredGone(store, r.WorkerID) && markReplicaLostPreservingIdentity(store, app, r) {
					healable = true
				}
				continue
			}
			if lister, ok := rt.(ContainerLister); ok {
				if recoverContainerReplica(store, mgr, prx, app, r, lister, listContainers(lister)) {
					anyAlive = true
				}
				continue
			}
			if recoverNativeReplica(store, mgr, prx, app, r, bundleDir) {
				anyAlive = true
			}
		}
		// Keep the app reconcilable when a slot was queued for lost-replica
		// healing: the healer only scans running/degraded apps, so driving the app
		// to stopped here would strand the slot it just queued until a manual
		// restart. Only an app with no live, no indeterminate, and no healable
		// replica is genuinely down.
		if !anyAlive && !indeterminate && !healable {
			markRecoveryStopped(store, app.Slug)
		}
	}
}

// markReplicaLostPreservingIdentity enters a replica into the lost-replica
// healing path while carrying its full identity forward. UpsertReplica replaces
// every column, and the watcher's lost-replica healing is gated on the tier and
// would never re-place a slot whose tier/worker was wiped, so the
// placement-relevant fields must be preserved. It is a no-op for a slot already
// lost. A persistence failure is logged rather than dropped: a silent miss would
// leave the slot stranded-running and unhealed. It returns true when the slot is
// now in the lost-healing path (freshly marked or already lost), so the caller
// can keep the app reconcilable: the lost-replica healer only scans
// running/degraded apps, and an app driven to stopped would strand the slot it
// just queued for healing.
func markReplicaLostPreservingIdentity(store *db.Store, app *db.App, r *db.Replica) bool {
	if r.Status == db.ReplicaStatusLost {
		return true
	}
	if err := store.UpsertReplica(db.UpsertReplicaParams{
		AppID: app.ID, Index: r.Index, Status: db.ReplicaStatusLost,
		PID: r.PID, Port: r.Port, Provider: r.Provider, Tier: r.Tier,
		EndpointURL: r.EndpointURL, WorkerID: r.WorkerID,
		AppVersion: r.AppVersion, DesiredState: r.DesiredState,
		DeploymentID: r.DeploymentID,
	}); err != nil {
		slog.Warn("recovery: persist lost replica failed",
			"slug", app.Slug, "idx", r.Index, "err", err)
		return false
	}
	return true
}

// workerDeclaredGone reports whether the replica's owning worker has been
// declared down or revoked, or its row reaped, i.e. it is genuinely gone rather
// than transiently unreachable for a single inventory scan. Only then may
// recovery enter the replica into the lost-replica healing path; a still-up
// worker is left for the WorkerDownMonitor to lose if its heartbeat truly goes
// stale. A revoked worker carries status "down" (RevokeWorker), so the single
// status check covers both down and revoked. An empty worker id has no owner to
// wait on and is treated as gone.
func workerDeclaredGone(store *db.Store, workerID string) bool {
	if workerID == "" {
		return true
	}
	// ECS-managed replicas (Fargate and EC2 launch types) use a synthetic
	// constant worker identity that never corresponds to a DB worker row.
	// Treat them as never-gone so ECS inventory blips do not permanently
	// strand replicas.
	if fargate.IsECSManagedWorkerID(workerID) {
		return false
	}
	w, err := store.GetWorker(workerID)
	if err != nil {
		// Row missing/reaped, or a read error: do not assume the worker is up.
		return true
	}
	return w.Status != "up"
}

// recoverNativeReplica re-adopts a single PID-backed replica. It returns true
// when the replica was adopted, and marks crashed (so the watcher restarts it)
// when the PID is missing, dead, or fails the stale-process identity check.
func recoverNativeReplica(store *db.Store, mgr *process.Manager, prx *proxy.Proxy, app *db.App, r *db.Replica, bundleDir string) bool {
	if r.PID == nil {
		// No PID recorded → treat as crashed so the watcher can restart it.
		markReplicaCrashed(store, app, r.Index, "no PID recorded")
		return false
	}
	if r.Port == nil {
		// PID but no port → corrupted row. Log and skip without status change.
		slog.Warn("recovery: replica has PID but no port", "slug", app.Slug, "idx", r.Index)
		return false
	}
	if err := syscall.Kill(*r.PID, 0); err != nil {
		markReplicaCrashed(store, app, r.Index, "process not alive")
		return false
	}
	if err := validateNativeProcess(*r.PID, *r.Port, bundleDir); err != nil {
		slog.Warn("recovery: rejected stale/mismatched process; will restart",
			"slug", app.Slug, "idx", r.Index, "pid", *r.PID, "port", *r.Port, "err", err)
		markReplicaCrashed(store, app, r.Index, "stale/mismatched process")
		return false
	}
	mgr.Adopt(app.Slug, process.ProcessInfo{
		Slug:        app.Slug,
		Index:       r.Index,
		PID:         *r.PID,
		Port:        *r.Port,
		Status:      process.StatusRunning,
		Tier:        r.Tier,
		Provider:    r.Provider,
		EndpointURL: r.EndpointURL,
		WorkerID:    r.WorkerID,
	}, process.RunHandle{PID: *r.PID})
	targetURL := r.EndpointURL
	if targetURL == "" {
		targetURL = fmt.Sprintf("http://127.0.0.1:%d", *r.Port)
	}
	if err := prx.RegisterReplica(app.Slug, r.Index, targetURL, nil); err != nil {
		slog.Error("process recovery: register proxy", "slug", app.Slug, "idx", r.Index, "err", err)
		return false
	}
	slog.Info("process recovery: re-adopted process", "slug", app.Slug, "idx", r.Index, "pid", *r.PID)
	return true
}

// markReplicaCrashed persists a replica's "crashed" status so the watcher
// restarts it on the next tick. The write is best-effort during recovery, but a
// failure is logged rather than dropped: a silent miss would leave the replica
// un-restarted and the app permanently under-replicated. reason names why the
// replica is being marked, for operator triage.
func markReplicaCrashed(store *db.Store, app *db.App, index int, reason string) {
	if err := store.UpsertReplica(db.UpsertReplicaParams{AppID: app.ID, Index: index, Status: "crashed"}); err != nil {
		slog.Warn("recovery: persist crashed replica failed",
			"slug", app.Slug, "idx", index, "reason", reason, "err", err)
	}
}

// recoverContainerReplica re-adopts a single container-backed replica by
// matching a live container's shinyhub.slug/replica_index labels to the replica
// row. containers is the lister's full container list (already fetched once).
// It returns true when the replica was adopted; a missing container, an
// out-of-pool index, or a missing port row leaves the replica unadopted so the
// watcher relaunches it.
func recoverContainerReplica(store *db.Store, mgr *process.Manager, prx *proxy.Proxy, app *db.App, r *db.Replica, lister ContainerLister, containers []process.ContainerInfo) bool {
	if r.Index >= app.Replicas {
		slog.Warn("recovery: replica index beyond current pool; skipping", "slug", app.Slug, "idx", r.Index, "pool", app.Replicas)
		return false
	}
	var cID string
	for _, c := range containers {
		if c.Labels[process.LabelSlug] == app.Slug && c.Labels[process.LabelReplicaIndex] == strconv.Itoa(r.Index) {
			cID = c.ID
			break
		}
	}
	if cID == "" {
		return false // no live container for this replica
	}
	pid, err := lister.InspectPID(cID)
	if err != nil {
		slog.Error("recovery: inspect docker container", "slug", app.Slug, "idx", r.Index, "err", err)
		return false
	}
	if r.Port == nil || *r.Port == 0 {
		slog.Warn("recovery: no port row for adopted container", "slug", app.Slug, "idx", r.Index)
		return false
	}
	port := *r.Port
	targetURL := r.EndpointURL
	if targetURL == "" {
		targetURL = fmt.Sprintf("http://127.0.0.1:%d", port)
	}
	mgr.Adopt(app.Slug, process.ProcessInfo{
		Slug:        app.Slug,
		Index:       r.Index,
		PID:         pid,
		Port:        port,
		Status:      process.StatusRunning,
		Tier:        r.Tier,
		Provider:    r.Provider,
		EndpointURL: r.EndpointURL,
		WorkerID:    r.WorkerID,
	}, process.RunHandle{ContainerID: cID})
	if err := prx.RegisterReplica(app.Slug, r.Index, targetURL, nil); err != nil {
		slog.Error("recovery: register docker proxy", "slug", app.Slug, "idx", r.Index, "err", err)
		return false
	}
	slog.Info("recovery: adopted docker container", "slug", app.Slug, "idx", r.Index, "pid", pid)
	return true
}

// FargateTaskSweeper is implemented by fargate.Runtime to support the orphan
// sweep. It lists all ShinyHub-managed tasks on the cluster (StartedBy="shinyhub"),
// stops individual tasks by ARN, and reports the runtime's own worker identity
// so the sweep builds the correct handle prefix for live-set lookup.
// fargate.Runtime satisfies this interface.
type FargateTaskSweeper interface {
	ListManagedTasks(ctx context.Context) ([]process.TaskRef, error)
	StopTask(ctx context.Context, arn string) error
	WorkerID() string
}

// SweepOrphanFargateTasks stops ECS tasks not currently owned by any live
// replica in the Manager. It must run AFTER RecoverProcesses so tasks the
// Manager re-adopted are protected. A nil sweeper is a no-op.
//
// Tasks are identified by a handle of the form "<workerID>/<task-arn>" in the
// Manager's running-container-ID set. The workerID is obtained from the sweeper
// so both Fargate ("fargate/<arn>") and EC2 ("ecs-ec2/<arn>") handles are
// correctly matched against live replicas.
func SweepOrphanFargateTasks(ctx context.Context, mgr *process.Manager, sweeper FargateTaskSweeper) {
	if sweeper == nil {
		return
	}
	tasks, err := sweeper.ListManagedTasks(ctx)
	if err != nil {
		slog.Error("fargate sweep: list managed tasks", "err", err)
		return
	}
	live := mgr.RunningContainerIDs()
	workerID := sweeper.WorkerID()
	removed := 0
	for _, t := range tasks {
		handle := workerID + "/" + t.ARN
		if live[handle] {
			continue
		}
		if err := sweeper.StopTask(ctx, t.ARN); err != nil {
			slog.Warn("fargate sweep: stop orphan task", "arn", t.ARN, "err", err)
			continue
		}
		removed++
		slog.Info("fargate sweep: stopped orphan task", "arn", t.ARN)
	}
	if removed > 0 {
		slog.Info("fargate sweep: complete", "removed", removed)
	}
}

// ContainerSweeper is implemented by DockerRuntime so the startup sweep can
// enumerate and delete ShinyHub-managed containers. Native runtime does not
// implement it; callers pass nil and the sweep is skipped.
type ContainerSweeper interface {
	ListByLabel(labelFilter string) ([]process.ContainerInfo, error)
	RemoveHandle(process.RunHandle) error
}

// SweepOrphanContainers removes ShinyHub-managed containers that no live
// replica owns. It must run AFTER RecoverProcesses, so containers the Manager
// re-adopted are protected; everything else labeled shinyhub.managed (a
// deleted app, a scaled-down replica index, a container left by a hard crash
// that recovery rejected) is force-removed so stopped containers do not
// accumulate across restarts. A nil sweeper (native runtime) is a no-op.
func SweepOrphanContainers(mgr *process.Manager, sweeper ContainerSweeper) {
	if sweeper == nil {
		return
	}
	containers, err := sweeper.ListByLabel(`{"label":["` + process.LabelManaged + `=true"]}`)
	if err != nil {
		slog.Error("container sweep: list", "err", err)
		return
	}
	live := mgr.RunningContainerIDs()
	removed := 0
	for _, c := range containers {
		if live[c.ID] {
			continue
		}
		// Only long-running app replicas are swept. One-shot schedule-run
		// containers (RunOnce) carry shinyhub.managed but no replica_index and
		// run with AutoRemove; an in-flight scheduled run at startup must not
		// be killed by the sweep.
		if _, isReplica := c.Labels[process.LabelReplicaIndex]; !isReplica {
			continue
		}
		if err := sweeper.RemoveHandle(process.RunHandle{ContainerID: c.ID}); err != nil {
			slog.Warn("container sweep: remove orphan",
				"container", c.ID, "slug", c.Labels[process.LabelSlug], "err", err)
			continue
		}
		removed++
		slog.Info("container sweep: removed orphan",
			"container", c.ID, "slug", c.Labels[process.LabelSlug])
	}
	if removed > 0 {
		slog.Info("container sweep: complete", "removed", removed)
	}
}

func markRecoveryStopped(store *db.Store, slug string) {
	if err := store.UpdateAppStatus(db.UpdateAppStatusParams{Slug: slug, Status: "stopped"}); err != nil {
		slog.Error("process recovery: mark stopped", "slug", slug, "err", err)
	}
}
