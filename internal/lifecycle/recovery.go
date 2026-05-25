package lifecycle

import (
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
		cs, err := l.ListByLabel(`{"label":["shinyhub.slug"]}`)
		if err != nil {
			slog.Error("recovery: list docker containers", "err", err)
			cs = nil
		}
		containerCache[l] = cs
		return cs
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
		for _, r := range reps {
			if lister, ok := mgr.RuntimeForTier(r.Tier).(ContainerLister); ok {
				if recoverContainerReplica(store, mgr, prx, app, r, lister, listContainers(lister)) {
					anyAlive = true
				}
				continue
			}
			if recoverNativeReplica(store, mgr, prx, app, r, bundleDir) {
				anyAlive = true
			}
		}
		if !anyAlive {
			markRecoveryStopped(store, app.Slug)
		}
	}
}

// recoverNativeReplica re-adopts a single PID-backed replica. It returns true
// when the replica was adopted, and marks crashed (so the watcher restarts it)
// when the PID is missing, dead, or fails the stale-process identity check.
func recoverNativeReplica(store *db.Store, mgr *process.Manager, prx *proxy.Proxy, app *db.App, r *db.Replica, bundleDir string) bool {
	if r.PID == nil {
		// No PID recorded → treat as crashed so the watcher can restart it.
		_ = store.UpsertReplica(db.UpsertReplicaParams{AppID: app.ID, Index: r.Index, Status: "crashed"})
		return false
	}
	if r.Port == nil {
		// PID but no port → corrupted row. Log and skip without status change.
		slog.Warn("recovery: replica has PID but no port", "slug", app.Slug, "idx", r.Index)
		return false
	}
	if err := syscall.Kill(*r.PID, 0); err != nil {
		_ = store.UpsertReplica(db.UpsertReplicaParams{AppID: app.ID, Index: r.Index, Status: "crashed"})
		return false
	}
	if err := validateNativeProcess(*r.PID, *r.Port, bundleDir); err != nil {
		slog.Warn("recovery: rejected stale/mismatched process; will restart",
			"slug", app.Slug, "idx", r.Index, "pid", *r.PID, "port", *r.Port, "err", err)
		_ = store.UpsertReplica(db.UpsertReplicaParams{AppID: app.ID, Index: r.Index, Status: "crashed"})
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
	if err := prx.RegisterReplica(app.Slug, r.Index, targetURL); err != nil {
		slog.Error("process recovery: register proxy", "slug", app.Slug, "idx", r.Index, "err", err)
		return false
	}
	slog.Info("process recovery: re-adopted process", "slug", app.Slug, "idx", r.Index, "pid", *r.PID)
	return true
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
		if c.Labels["shinyhub.slug"] == app.Slug && c.Labels["shinyhub.replica_index"] == strconv.Itoa(r.Index) {
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
	if err := prx.RegisterReplica(app.Slug, r.Index, targetURL); err != nil {
		slog.Error("recovery: register docker proxy", "slug", app.Slug, "idx", r.Index, "err", err)
		return false
	}
	slog.Info("recovery: adopted docker container", "slug", app.Slug, "idx", r.Index, "pid", pid)
	return true
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
	containers, err := sweeper.ListByLabel(`{"label":["shinyhub.managed=true"]}`)
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
		if _, isReplica := c.Labels["shinyhub.replica_index"]; !isReplica {
			continue
		}
		if err := sweeper.RemoveHandle(process.RunHandle{ContainerID: c.ID}); err != nil {
			slog.Warn("container sweep: remove orphan",
				"container", c.ID, "slug", c.Labels["shinyhub.slug"], "err", err)
			continue
		}
		removed++
		slog.Info("container sweep: removed orphan",
			"container", c.ID, "slug", c.Labels["shinyhub.slug"])
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
