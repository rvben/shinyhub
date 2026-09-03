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

	"github.com/rvben/shinyhub/internal/config"
	"github.com/rvben/shinyhub/internal/db"
	"github.com/rvben/shinyhub/internal/deploy"
	"github.com/rvben/shinyhub/internal/fargate"
	"github.com/rvben/shinyhub/internal/process"
	"github.com/rvben/shinyhub/internal/proxy"
)

// ReconcileInflightDeployments fails any deployment still in 'pending' at
// startup. A pending row normally falls back to the last good deployment. If
// its producer barrier was entered, however, shared data may already belong to
// the candidate; the app is durably failed before the row is failed so prior
// consumers can never be recovered against possibly incompatible data.
//
// Any persistence error is returned and startup must retry this function before
// process recovery or background reconcilers are started.
func ReconcileInflightDeployments(store *db.Store) error {
	inflight, err := store.ListInflightDeployments()
	if err != nil {
		return fmt.Errorf("deploy reconcile: list inflight deployments: %w", err)
	}
	for _, d := range inflight {
		if _, err := store.RestoreDeploymentPriorScheduleSnapshot(d.ID, d.AppID); err != nil {
			if errors.Is(err, db.ErrNotFound) {
				reason := "server interrupted a deployment created before prior schedule snapshots were supported; app left stopped because its declaration provenance is unknown"
				if qerr := store.QuarantineAndFailDeployment(d.ID, reason); qerr != nil {
					return fmt.Errorf("deploy reconcile: quarantine legacy deployment %d: %w", d.ID, qerr)
				}
				slog.Warn("deploy reconcile: quarantined interrupted legacy deployment with unknown declarations",
					"id", d.ID, "app_id", d.AppID, "version", d.Version)
				continue
			}
			return fmt.Errorf("deploy reconcile: restore prior declarations for deployment %d: %w", d.ID, err)
		}
		if d.ProducerBarrierEntered {
			if err := store.QuarantineAndFailDeployment(d.ID, "server interrupted after candidate compatibility barrier; app left stopped because shared data may be incompatible with the previous deployment"); err != nil {
				return fmt.Errorf("deploy reconcile: fail producer-published deployment %d: %w", d.ID, err)
			}
			slog.Warn("deploy reconcile: quarantined interrupted producer deployment",
				"id", d.ID, "app_id", d.AppID, "version", d.Version)
			continue
		}
		if err := store.FailDeployment(d.ID); err != nil {
			return fmt.Errorf("deploy reconcile: fail interrupted deployment %d: %w", d.ID, err)
		}
		slog.Warn("deploy reconcile: failed interrupted deployment", "id", d.ID, "app_id", d.AppID, "version", d.Version)
	}
	if err := store.EnforceCompatibilityQuarantines(); err != nil {
		return fmt.Errorf("deploy reconcile: %w", err)
	}
	return nil
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
// cannot be read, validation fails closed: a live port is not enough evidence
// to signal or route a PID that may have been reused by another process.
func validateNativeProcess(pid, port int, bundleDir string) error {
	if err := validateNativeProcessIdentity(pid, bundleDir); err != nil {
		return err
	}
	addr := net.JoinHostPort("127.0.0.1", strconv.Itoa(port))
	conn, err := net.DialTimeout("tcp", addr, 750*time.Millisecond)
	if err != nil {
		return fmt.Errorf("port %d not accepting connections: %w", port, err)
	}
	_ = conn.Close()
	return nil
}

// validateNativeProcessIdentity proves that pid belongs to the active bundle
// without requiring its HTTP port to be ready. Activation recovery uses this
// for a process persisted during the pre-health window. Ordinary legacy
// recovery permits a PID-only fallback when no deployment bundle is available;
// destructive activation cleanup checks for a concrete bundle before calling
// this helper and otherwise quarantines a live PID.
func validateNativeProcessIdentity(pid int, bundleDir string) error {
	p, err := gops.NewProcess(int32(pid))
	if err != nil {
		return fmt.Errorf("pid %d not found: %w", pid, err)
	}
	if bundleDir != "" {
		return validateNativeProcessCWD(pid, bundleDir, p.Cwd)
	}
	return nil
}

func validateNativeProcessCWD(pid int, bundleDir string, readCWD func() (string, error)) error {
	cwd, err := readCWD()
	if err != nil {
		return fmt.Errorf("read pid %d cwd: %w", pid, err)
	}
	// Normalize both sides with EvalSymlinks before comparing. On macOS /tmp is
	// a symlink to /private/tmp; fall back to absolute paths when a target was
	// deleted during recovery.
	want, _ := filepath.Abs(bundleDir)
	got, _ := filepath.Abs(cwd)
	if rw, err := filepath.EvalSymlinks(want); err == nil {
		want = rw
	}
	if rg, err := filepath.EvalSymlinks(got); err == nil {
		got = rg
	}
	if want != got {
		return fmt.Errorf("pid %d cwd %q does not match bundle %q (pid reuse?)", pid, got, want)
	}
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
	// RemoveContainer force-removes a container by ID (force handles a paused
	// container). Recovery uses it to reap an orphaned frozen warm container that
	// survived a restart and cannot be re-adopted.
	RemoveContainer(containerID string) error
}

// RecoverProcesses re-adopts running app processes after a server restart.
// Each replica is routed to its tier's runtime (via mgr.RuntimeForTier): a
// container-backed runtime (one that implements ContainerLister) is recovered
// by matching live containers to the replica's labels, every other runtime by
// validating the recorded PID. A single app's replicas may therefore span a
// native default tier and a container-backed burst tier. defaultMaxSessions is
// the runtime-wide session-cap fallback applied when an app has
// max_sessions_per_replica == 0. identityGlobal is the global
// auth.identity_headers enabled flag used to resolve each app's effective
// identity-forwarding setting.
// inventoryRecoveryTimeout bounds how long recovery waits for an off-host
// tier's worker inventory. Inventory fans out to the tier's workers
// concurrently, so this caps the whole per-tier fetch. Well under the worker
// dialer's ~120s header timeout: a hung worker must not stall fleet recovery.
const inventoryRecoveryTimeout = 15 * time.Second

func RecoverProcesses(store *db.Store, mgr *process.Manager, prx *proxy.Proxy, defaultMaxSessions int, identityGlobal bool, defaultWorkerIsolation string) {
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
		// Bound the fan-out: one hung off-host worker must not stall fleet
		// recovery for the multi-minute HTTP header timeout. Inventory queries
		// the tier's workers concurrently, so this caps the whole tier fetch.
		ctx, cancel := context.WithTimeout(context.Background(), inventoryRecoveryTimeout)
		items, err := inv.Inventory(ctx)
		cancel()
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
		quarantined, qerr := store.AppCompatibilityQuarantined(app.ID)
		if qerr != nil {
			slog.Error("process recovery: compatibility quarantine unavailable; skipping app", "slug", app.Slug, "err", qerr)
			continue
		}
		if quarantined {
			if err := store.UpdateAppStatus(db.UpdateAppStatusParams{Slug: app.Slug, Status: "failed"}); err != nil {
				slog.Error("process recovery: persist compatibility quarantine", "slug", app.Slug, "err", err)
			}
			slog.Warn("process recovery: skipped compatibility-quarantined app", "slug", app.Slug)
			continue
		}
		if ok := reconcileDeploymentGenerationProjection(store, app); !ok {
			if err := store.UpdateAppStatus(db.UpdateAppStatusParams{Slug: app.Slug, Status: "failed"}); err != nil {
				slog.Error("generation recovery: persist failed state", "slug", app.Slug, "err", err)
			}
			continue
		}
		// Generation tokens live only in proxy memory. Restore the active token
		// before any recovered backend can serve injected navigation, otherwise a
		// tab opened before the next deploy cannot recognize that it is stale.
		if active, err := store.GetActiveDeploymentGeneration(app.ID); err == nil {
			prx.SetGenerationActivationToken(app.Slug, active.DeploymentID, active.ActivationToken)
		} else if !errors.Is(err, db.ErrNotFound) {
			slog.Warn("process recovery: load active generation token", "slug", app.Slug, "err", err)
		}
		// Elastic-mode apps (grouped or per_session) are demand-driven: workers
		// are ephemeral and are never persisted to the replicas table. Set up
		// the elastic proxy pool and keep the app status as "running" so the
		// first incoming request can trigger a fresh spawn. Skip the normal
		// replica-adoption loop entirely.
		// Resolve once so the guard and SetPoolMode use the same effective mode
		// (fleet default applies when the per-app field is empty).
		resolvedIso := deploy.ResolveWorkerIsolation(app.WorkerIsolation, defaultWorkerIsolation)
		if isElasticIsolation(resolvedIso) {
			prx.SetPoolAppID(app.Slug, app.ID)
			prx.SetPoolMode(app.Slug,
				config.WorkerIsolationMode(resolvedIso),
				app.WorkerGroupedSize,
				app.WorkerMaxWorkers)
			prx.SetPoolWarmSpares(app.Slug, app.WorkerWarmSpares)
			prx.SetPoolIdentityHeaders(app.Slug, deploy.ResolveIdentityHeaders(app.IdentityHeaders, identityGlobal))
			prx.ReconcileElasticWarmSpares(app.Slug)
			// Mark the app running: the empty elastic pool is live and will
			// spawn workers on demand. A crashed/failed app that was in elastic
			// mode is re-exposed as "running" here; the next spawn attempt will
			// surface any boot failure as a "crashed" transition.
			if err := store.UpdateAppStatus(db.UpdateAppStatusParams{
				Slug: app.Slug, Status: "running",
			}); err != nil {
				slog.Error("process recovery: mark elastic app running", "slug", app.Slug, "err", err)
			}
			continue
		}

		reps, err := store.ListReplicas(app.ID)
		if err != nil || len(reps) == 0 {
			markRecoveryStopped(store, app.Slug)
			continue
		}
		unfinishedRuns, err := store.ListUnfinishedAppLogRuns(app.ID)
		if err != nil {
			slog.Error("process recovery: list unfinished log runs", "slug", app.Slug, "err", err)
		}
		logRunByReplica := make(map[int]string, len(unfinishedRuns))
		for _, run := range unfinishedRuns {
			if _, exists := logRunByReplica[run.ReplicaIndex]; !exists {
				logRunByReplica[run.ReplicaIndex] = run.RunID
			}
		}
		poolSize := app.Replicas
		recoverableSurges := make(map[int]bool)
		for _, r := range reps {
			if r.Index >= app.Replicas && r.ActivationID != nil {
				a, aerr := store.GetScheduleActivation(*r.ActivationID)
				if aerr != nil {
					// Unknown is not terminal. Preserve both the runtime and its
					// durable handle so the activation coordinator can retry once the
					// store is readable again.
					slog.Warn("recovery: activation state unknown; preserving surge",
						"slug", app.Slug, "idx", r.Index, "activation_id", *r.ActivationID, "err", aerr)
					recoverableSurges[r.Index] = true
				} else if isNonterminalActivationStatus(a.Status) {
					recoverableSurges[r.Index] = true
				}
			}
			if r.Index >= poolSize && recoverableSurges[r.Index] {
				poolSize = r.Index + 1
			}
		}
		prx.SetPoolAppID(app.Slug, app.ID)
		prx.SetPoolSize(app.Slug, poolSize)
		prx.SetPoolCap(app.Slug, deploy.ResolveMaxSessionsPerReplica(app.MaxSessionsPerReplica, defaultMaxSessions))
		prx.SetPoolIdentityHeaders(app.Slug, deploy.ResolveIdentityHeaders(app.IdentityHeaders, identityGlobal))
		bundleDir := activeBundleDir(store, app.ID)

		anyAlive := false
		indeterminate := false
		healable := false
		for _, r := range reps {
			logRunID := logRunByReplica[r.Index]
			if r.Index >= app.Replicas && r.ActivationID != nil && !recoverableSurges[r.Index] {
				// A terminal action never owns a serving surge after restart. Leave
				// containers unadopted for the orphan sweep; stop recorded native
				// processes directly, then remove the stale row.
				canDelete := true
				if _, containerBacked := mgr.RuntimeForTier(r.Tier).(ContainerLister); !containerBacked {
					canDelete = stopNativeActivationReplica(mgr, app, r, bundleDir, logRunID)
				}
				if !canDelete {
					indeterminate = true
					continue
				}
				if err := store.DeleteReplica(app.ID, r.Index); err != nil && !errors.Is(err, db.ErrNotFound) {
					slog.Warn("recovery: delete terminal activation surge", "slug", app.Slug, "idx", r.Index, "err", err)
				}
				continue
			}
			if r.Status == "starting" && r.ActivationID != nil {
				// The server died after runtime start but before health completed.
				// Never adopt an unproven endpoint into routing. Docker is left
				// unowned for the post-recovery container sweep; native is adopted
				// only long enough to perform a confirmed stop.
				rt := mgr.RuntimeForTier(r.Tier)
				if _, containerBacked := rt.(ContainerLister); !containerBacked {
					if stopNativeActivationReplica(mgr, app, r, bundleDir, logRunID) {
						markReplicaCrashed(store, app, r.Index, "activation start interrupted before health check", logRunID)
					} else {
						// Retain the PID and starting row. Losing either would let the
						// watcher launch a duplicate while this process may still exist.
						indeterminate = true
					}
				} else {
					markReplicaCrashed(store, app, r.Index, "activation start interrupted before health check", logRunID)
				}
				continue
			}
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
				if recoverRemoteReplica(store, mgr, prx, app, r, ti.items, logRunID) {
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
				if recoverContainerReplica(store, mgr, prx, app, r, lister, listContainers(lister), logRunID) {
					anyAlive = true
				}
				continue
			}
			if recoverNativeReplica(store, mgr, prx, app, r, bundleDir, logRunID) {
				anyAlive = true
			}
		}
		// Keep the app reconcilable when a slot was queued for lost-replica
		// healing: the healer only scans running/degraded apps, so driving the app
		// down here would strand the slot it just queued until a manual restart.
		// Only an app with no live, no indeterminate, and no healable replica is
		// genuinely down: its processes did not survive the restart. Mark it
		// "hibernated" (not "stopped") so the warm-restore pass re-boots and
		// re-freezes it - or, with warm-wake off, so the next request cold-wakes it
		// - instead of silently dropping a running app to a dead "stopped". A
		// genuinely-broken app fails its re-boot and is surfaced as "crashed" by the
		// restore/wake paths.
		if !anyAlive && !indeterminate && !healable {
			markRecoveryDown(store, app.Slug)
		}
		if anyAlive {
			cleanupObsoleteDeploymentGenerations(store, app)
		}
	}

	parkStrandedReplicas(store)
}

// parkStrandedReplicas repairs replica rows that contradict an app the loop
// above never visits. Recovery iterates running and degraded apps, so a crashed
// replica row left under an already-parked app is structurally out of its
// reach: an older control plane could park the app and fail to park the row,
// and a restored backup can carry the same pair. The app stays wakeable and
// real traffic still wakes it, but the API projects the replica onto the app
// and reports it crashed, which every readiness gate reads as terminal.
//
// Runs after the per-app loop so the sweep only ever sees apps this recovery
// pass has finished deciding about.
func parkStrandedReplicas(store *db.Store) {
	repaired, err := store.ParkStrandedReplicas()
	if err != nil {
		slog.Error("process recovery: park stranded replicas", "err", err)
		return
	}
	for _, r := range repaired {
		slog.Info("process recovery: parked a stranded replica row under a non-serving app",
			"slug", r.Slug, "idx", r.Index)
	}
}

// reconcileDeploymentGenerationProjection closes the crash window where the
// durable active pointer names a healthy candidate while the legacy replicas
// projection still names the previous pool. It performs only the authoritative
// projection here; obsolete process cleanup runs after adoption/routing so a
// stale or unkillable old identity can never veto restoration of healthy
// service.
func reconcileDeploymentGenerationProjection(store *db.Store, app *db.App) bool {
	generationRows, err := store.ListDeploymentReplicas(app.ID)
	if err != nil {
		slog.Error("generation recovery: list replicas", "slug", app.Slug, "err", err)
		return false
	}
	if len(generationRows) == 0 {
		return true
	}
	active, activeErr := store.GetActiveDeploymentGeneration(app.ID)
	activeID := int64(0)
	if activeErr == nil {
		activeID = active.DeploymentID
	} else if !errors.Is(activeErr, db.ErrNotFound) {
		slog.Error("generation recovery: load active generation", "slug", app.Slug, "err", activeErr)
		return false
	}
	activeRows := make([]*db.DeploymentReplica, 0, len(generationRows))
	for _, replica := range generationRows {
		if replica.DeploymentID == activeID {
			activeRows = append(activeRows, replica)
		}
	}
	// With no active-generation row, the legacy projection already names the
	// authority. The remaining rows are cleanup-only (an abandoned pre-commit
	// candidate or a post-cutover old generation) and must not replace it.
	if len(activeRows) == 0 {
		return true
	}
	activeDeployment, err := store.GetDeploymentByID(activeID)
	if err != nil {
		slog.Error("generation recovery: load active deployment", "slug", app.Slug, "deployment_id", activeID, "err", err)
		return false
	}

	for _, replica := range activeRows {
		pid, port, deploymentID := replica.PID, replica.Port, replica.DeploymentID
		if err := store.UpsertReplica(db.UpsertReplicaParams{
			AppID: app.ID, Index: replica.Index, PID: pid, Port: port,
			Status: db.ReplicaStatusRunning, Provider: replica.Provider, Tier: replica.Tier,
			EndpointURL: replica.EndpointURL, WorkerID: replica.WorkerID,
			AppVersion: activeDeployment.Version, DesiredState: "running",
			DeploymentID: &deploymentID, ConsumerBooted: true,
		}); err != nil {
			slog.Error("generation recovery: publish active replica projection", "slug", app.Slug, "index", replica.Index, "err", err)
			return false
		}
	}
	if err := store.DeleteDeploymentReplicas(activeID); err != nil {
		slog.Error("generation recovery: clear projected active identities", "slug", app.Slug, "deployment_id", activeID, "err", err)
		return false
	}
	return true
}

// cleanupObsoleteDeploymentGenerations runs only after the authoritative
// legacy projection has been adopted and routed. Each deployment ledger is
// deleted as a unit only when every recorded native identity is confirmed gone;
// failures remain durable and block another handoff until a later recovery.
func cleanupObsoleteDeploymentGenerations(store *db.Store, app *db.App) {
	rows, err := store.ListDeploymentReplicas(app.ID)
	if err != nil {
		slog.Error("generation recovery: list cleanup identities", "slug", app.Slug, "err", err)
		return
	}
	activeID := int64(0)
	if active, err := store.GetActiveDeploymentGeneration(app.ID); err == nil {
		activeID = active.DeploymentID
	} else if !errors.Is(err, db.ErrNotFound) {
		slog.Error("generation recovery: load authority before cleanup", "slug", app.Slug, "err", err)
		return
	}
	byDeployment := make(map[int64][]*db.DeploymentReplica)
	for _, row := range rows {
		if row.DeploymentID != activeID {
			byDeployment[row.DeploymentID] = append(byDeployment[row.DeploymentID], row)
		}
	}
	for deploymentID, replicas := range byDeployment {
		allStopped := true
		for _, replica := range replicas {
			id := deploymentID
			if !stopRecordedNativeReplica(store, app, replica.PID, replica.Provider, &id) {
				allStopped = false
			}
		}
		if !allStopped {
			slog.Warn("generation recovery: obsolete generation cleanup deferred",
				"slug", app.Slug, "deployment_id", deploymentID)
			continue
		}
		if err := store.DeleteDeploymentReplicas(deploymentID); err != nil {
			slog.Error("generation recovery: clear obsolete identities", "slug", app.Slug, "deployment_id", deploymentID, "err", err)
		}
	}
}

func stopRecordedNativeReplica(store *db.Store, app *db.App, pid *int, provider string, deploymentID *int64) bool {
	if pid == nil || *pid <= 0 || (provider != "" && provider != "native") {
		return true
	}
	pidGone := errors.Is(syscall.Kill(*pid, 0), syscall.ESRCH)
	groupGone := errors.Is(syscall.Kill(-*pid, 0), syscall.ESRCH)
	if pidGone && groupGone {
		return true
	}
	if deploymentID == nil {
		slog.Error("generation recovery: refusing to signal native process without deployment identity", "slug", app.Slug, "pid", *pid)
		return false
	}
	deployment, err := store.GetDeploymentByID(*deploymentID)
	if err != nil || deployment.BundleDir == "" || validateNativeProcessIdentity(*pid, deployment.BundleDir) != nil {
		slog.Error("generation recovery: refusing to signal native process whose bundle identity cannot be proven", "slug", app.Slug, "pid", *pid, "deployment_id", *deploymentID, "err", err)
		return false
	}
	if err := syscall.Kill(-*pid, syscall.SIGTERM); err != nil && !errors.Is(err, syscall.ESRCH) {
		slog.Error("generation recovery: terminate old native generation", "slug", app.Slug, "pid", *pid, "err", err)
		return false
	}
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		pidGone = errors.Is(syscall.Kill(*pid, 0), syscall.ESRCH)
		groupGone = errors.Is(syscall.Kill(-*pid, 0), syscall.ESRCH)
		if pidGone && groupGone {
			return true
		}
		time.Sleep(100 * time.Millisecond)
	}
	// The numeric PID/PGID may have been recycled during the TERM grace. Never
	// escalate to SIGKILL unless the leader still exists and its bundle identity
	// is revalidated immediately before the signal.
	if pidGone || validateNativeProcessIdentity(*pid, deployment.BundleDir) != nil {
		slog.Error("generation recovery: refusing SIGKILL after native process identity changed", "slug", app.Slug, "pid", *pid)
		return false
	}
	if err := syscall.Kill(-*pid, syscall.SIGKILL); err != nil && !errors.Is(err, syscall.ESRCH) {
		slog.Error("generation recovery: kill old native generation", "slug", app.Slug, "pid", *pid, "err", err)
		return false
	}
	deadline = time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		pidGone = errors.Is(syscall.Kill(*pid, 0), syscall.ESRCH)
		groupGone = errors.Is(syscall.Kill(-*pid, 0), syscall.ESRCH)
		if pidGone && groupGone {
			return true
		}
		time.Sleep(100 * time.Millisecond)
	}
	slog.Error("generation recovery: native process group survived SIGKILL", "slug", app.Slug, "pid", *pid)
	return false
}

func stopNativeActivationReplica(mgr *process.Manager, app *db.App, r *db.Replica, bundleDir, logRunID string) bool {
	if r.PID == nil {
		return true
	}
	if err := syscall.Kill(*r.PID, 0); err != nil {
		return errors.Is(err, syscall.ESRCH)
	}
	if bundleDir == "" || validateNativeProcessIdentity(*r.PID, bundleDir) != nil {
		slog.Warn("recovery: activation replica PID failed cwd identity check; preserving row",
			"slug", app.Slug, "idx", r.Index, "pid", *r.PID)
		return false
	}
	port := 0
	if r.Port != nil {
		port = *r.Port
	}
	mgr.Adopt(app.Slug, process.ProcessInfo{
		Slug: app.Slug, AppID: app.ID, Index: r.Index, PID: *r.PID, Port: port,
		Status: process.StatusRunning, Tier: r.Tier, Provider: r.Provider,
		EndpointURL: r.EndpointURL, WorkerID: r.WorkerID, LogRunID: logRunID,
	}, process.RunHandle{PID: *r.PID})
	if err := mgr.StopReplicaConfirmed(app.Slug, r.Index); err != nil {
		slog.Warn("recovery: stop interrupted activation replica", "slug", app.Slug, "idx", r.Index, "err", err)
		return false
	}
	return true
}

func isNonterminalActivationStatus(status string) bool {
	switch status {
	case "pending", "deferred_interval", "deferred_capacity", "repairing", "running":
		return true
	default:
		return false
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
//
// Only an affirmatively "down" worker is gone. A "joining" worker (registered
// but not yet promoted by its first heartbeat) is coming up, not gone, so its
// slots are left for the WorkerDownMonitor rather than stranded here; in
// practice a joining worker owns no replicas yet, but the conservative check
// keeps recovery correct should that ever change.
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
	return w.Status == "down"
}

// recoverNativeReplica re-adopts a single PID-backed replica. It returns true
// when the replica was adopted, and marks crashed (so the watcher restarts it)
// when the PID is missing, dead, or fails the stale-process identity check.
func recoverNativeReplica(store *db.Store, mgr *process.Manager, prx *proxy.Proxy, app *db.App, r *db.Replica, bundleDir, logRunID string) bool {
	if r.DesiredState == db.ReplicaDesiredWarm {
		// Warm-parked by the idle shrink: expansion boots it, not recovery. A
		// 'suspended' warm row is a frozen (SIGSTOP'd) process that survived this
		// restart. Re-adopt it warm so its next wake SIGCONT-resumes it instead of
		// cold-booting; only when it cannot be re-adopted (gone, or fails the cwd
		// identity check) is it reaped and the row downgraded so expansion
		// cold-boots. Re-adopting returns true so the app is not driven to stopped:
		// it stays hibernated and wakeable, and the watcher's wake already falls
		// back to cold-boot if the resume ever fails.
		if r.Status == "suspended" {
			if reAdoptFrozenWarmReplica(mgr, app, r, bundleDir, logRunID) {
				return true
			}
			cleanupFrozenWarmReplica(store, app, r, bundleDir)
		}
		return false
	}
	if r.PID == nil {
		// No PID recorded → treat as crashed so the watcher can restart it.
		markReplicaCrashed(store, app, r.Index, "no PID recorded", logRunID)
		return false
	}
	if r.Port == nil {
		// PID but no port → corrupted row. Log and skip without status change.
		slog.Warn("recovery: replica has PID but no port", "slug", app.Slug, "idx", r.Index)
		return false
	}
	if err := syscall.Kill(*r.PID, 0); err != nil {
		markReplicaCrashed(store, app, r.Index, "process not alive", logRunID)
		return false
	}
	if err := validateNativeProcessIdentity(*r.PID, bundleDir); err != nil {
		slog.Warn("recovery: rejected stale/mismatched process identity; retaining durable identity",
			"slug", app.Slug, "idx", r.Index, "pid", *r.PID, "err", err)
		markReplicaCrashed(store, app, r.Index, "stale/mismatched process identity", logRunID)
		return false
	}
	if err := validateNativeProcess(*r.PID, *r.Port, bundleDir); err != nil {
		// The PID was proved to belong to this bundle, but it has not reached
		// readiness. Adopt it solely so confirmed stop can terminate its complete
		// process group before making the slot restartable; otherwise a guarded
		// pre-health survivor could be laundered by overwriting this row.
		mgr.Adopt(app.Slug, process.ProcessInfo{
			Slug: app.Slug, AppID: app.ID, Index: r.Index, PID: *r.PID, Port: *r.Port,
			Status: process.StatusStarting, Tier: r.Tier, Provider: r.Provider,
			EndpointURL: r.EndpointURL, WorkerID: r.WorkerID, AppVersion: r.AppVersion,
			DeploymentID: derefInt64(r.DeploymentID), LogRunID: logRunID,
		}, process.RunHandle{PID: *r.PID})
		stopErr := mgr.StopReplicaConfirmed(app.Slug, r.Index)
		if stopErr == nil {
			if clearErr := store.ClearReplicaRuntimeIdentity(app.ID, r.Index); clearErr != nil && !errors.Is(clearErr, db.ErrNotFound) {
				slog.Error("recovery: clear stopped pre-health replica identity", "slug", app.Slug, "idx", r.Index, "err", clearErr)
			}
		}
		slog.Warn("recovery: stopped unready process before allowing restart",
			"slug", app.Slug, "idx", r.Index, "pid", *r.PID, "port", *r.Port, "err", err)
		markReplicaCrashed(store, app, r.Index, "process did not recover ready", logRunID)
		return false
	}
	mgr.Adopt(app.Slug, process.ProcessInfo{
		Slug:         app.Slug,
		AppID:        app.ID,
		Index:        r.Index,
		PID:          *r.PID,
		Port:         *r.Port,
		Status:       process.StatusRunning,
		Tier:         r.Tier,
		Provider:     r.Provider,
		EndpointURL:  r.EndpointURL,
		WorkerID:     r.WorkerID,
		AppVersion:   r.AppVersion,
		DeploymentID: derefInt64(r.DeploymentID),
		LogRunID:     logRunID,
	}, process.RunHandle{PID: *r.PID})
	targetURL := r.EndpointURL
	if targetURL == "" {
		targetURL = fmt.Sprintf("http://127.0.0.1:%d", *r.Port)
	}
	if err := prx.RegisterReplica(app.Slug, r.Index, targetURL, nil, derefInt64(r.DeploymentID), app.ID); err != nil {
		slog.Error("process recovery: register proxy", "slug", app.Slug, "idx", r.Index, "err", err)
		return false
	}
	slog.Info("process recovery: re-adopted process", "slug", app.Slug, "idx", r.Index, "pid", *r.PID, "log_run_id", logRunID)
	return true
}

// reAdoptFrozenWarmReplica re-adopts a SIGSTOP-frozen warm replica that survived
// a restart, so its next wake SIGCONT-resumes it warm instead of cold-booting. It
// registers the replica in the Manager as suspended (which re-registers its
// per-app cgroup via WarmReadopter) and leaves the DB row suspended/warm so the
// wake path picks the resume branch. The proxy route is intentionally not
// registered: a frozen process accepts no connections, and the wake registers the
// route once it resumes.
//
// Identity is verified by cwd (a frozen process cannot be port-probed), so a
// reused/unrelated PID is never adopted. Returns false - leaving the caller to
// reap and downgrade - when there is no live, verified process to re-adopt
// (manager absent, PID/port missing, process gone, or identity mismatch).
func reAdoptFrozenWarmReplica(mgr *process.Manager, app *db.App, r *db.Replica, bundleDir, logRunID string) bool {
	if mgr == nil || r.PID == nil || r.Port == nil || bundleDir == "" {
		return false
	}
	if syscall.Kill(*r.PID, 0) != nil {
		return false // process is gone
	}
	if !frozenReplicaIdentityOK(*r.PID, bundleDir) {
		return false // unverified PID; caller downgrades without killing
	}
	mgr.Adopt(app.Slug, process.ProcessInfo{
		Slug:         app.Slug,
		AppID:        app.ID,
		Index:        r.Index,
		PID:          *r.PID,
		Port:         *r.Port,
		Status:       process.StatusSuspended,
		Tier:         r.Tier,
		Provider:     r.Provider,
		EndpointURL:  r.EndpointURL,
		WorkerID:     r.WorkerID,
		AppVersion:   r.AppVersion,
		DeploymentID: derefInt64(r.DeploymentID),
		LogRunID:     logRunID,
	}, process.RunHandle{PID: *r.PID})
	slog.Info("recovery: re-adopted frozen warm replica (warm-resumable on next wake)",
		"slug", app.Slug, "idx", r.Index, "pid", *r.PID)
	return true
}

// cleanupFrozenWarmReplica tears down a native warm replica left SIGSTOP-frozen by
// a prior control plane and downgrades its row to stopped/warm so a later
// expansion cold-boots the slot. It is the fallback for a frozen replica that
// reAdoptFrozenWarmReplica could not re-adopt warm (the process is gone or fails
// the identity check); leaving such a process would leak a frozen process holding
// swap. Identity is checked by cwd only (NOT the port
// probe validateNativeProcess uses: a frozen process accepts no connections), so
// a reused PID is never killed; the row is downgraded regardless of kill outcome.
// With no resolvable bundle dir to match against, the process is left alone (the
// row still downgrades) rather than risk killing an unverified PID.
func cleanupFrozenWarmReplica(store *db.Store, app *db.App, r *db.Replica, bundleDir string) {
	if r.PID != nil && bundleDir != "" {
		if alive := syscall.Kill(*r.PID, 0); alive == nil {
			if frozenReplicaIdentityOK(*r.PID, bundleDir) {
				// Confirmed our frozen replica: SIGKILL the whole group (a stopped
				// process is reaped immediately even without a prior SIGCONT). ESRCH
				// means it raced away already.
				if kerr := syscall.Kill(-*r.PID, syscall.SIGKILL); kerr != nil && kerr != syscall.ESRCH {
					slog.Warn("recovery: kill frozen warm replica", "slug", app.Slug, "idx", r.Index, "pid", *r.PID, "err", kerr)
				} else {
					slog.Info("recovery: reaped frozen warm replica", "slug", app.Slug, "idx", r.Index, "pid", *r.PID)
				}
			} else {
				slog.Warn("recovery: frozen warm replica PID failed cwd identity check; not killing",
					"slug", app.Slug, "idx", r.Index, "pid", *r.PID)
			}
		}
	}
	if err := store.UpsertReplica(db.UpsertReplicaParams{
		AppID:        app.ID,
		Index:        r.Index,
		Status:       "stopped",
		Provider:     r.Provider,
		Tier:         r.Tier,
		AppVersion:   r.AppVersion,
		DesiredState: db.ReplicaDesiredWarm,
		DeploymentID: r.DeploymentID,
	}); err != nil {
		slog.Warn("recovery: downgrade frozen warm replica row", "slug", app.Slug, "idx", r.Index, "err", err)
	}
}

// frozenReplicaIdentityOK reports whether the process at pid is our frozen
// replica, by comparing its working directory to bundleDir. It deliberately omits
// the TCP port probe validateNativeProcess performs: a SIGSTOP-frozen process
// accepts no connections, so a port probe would always reject it. cwd comes from
// /proc and is readable while the process is stopped. Symlinks are resolved on
// both sides (e.g. macOS /tmp -> /private/tmp) before comparing.
func frozenReplicaIdentityOK(pid int, bundleDir string) bool {
	p, err := gops.NewProcess(int32(pid))
	if err != nil {
		return false
	}
	cwd, err := p.Cwd()
	if err != nil {
		return false
	}
	want, _ := filepath.Abs(bundleDir)
	got, _ := filepath.Abs(cwd)
	if rw, err := filepath.EvalSymlinks(want); err == nil {
		want = rw
	}
	if rg, err := filepath.EvalSymlinks(got); err == nil {
		got = rg
	}
	return want == got
}

// markReplicaCrashed persists a replica's "crashed" status so the watcher
// restarts it on the next tick. The write is best-effort during recovery, but a
// failure is logged rather than dropped: a silent miss would leave the replica
// un-restarted and the app permanently under-replicated. reason names why the
// replica is being marked, for operator triage.
func markReplicaCrashed(store *db.Store, app *db.App, index int, reason, logRunID string) {
	// RecordReplicaCrash updates status and diagnostics in place. In particular,
	// it preserves a pre-health activation's tier, PID/container ID, endpoint,
	// generation, and activation owner until exact runtime absence is proved.
	// A sparse UpsertReplica here would erase that recovery identity and could
	// allow a replacement to launch alongside the surviving process.
	observedAt := time.Now().UTC()
	if err := store.RecordReplicaCrash(db.UpsertReplicaParams{
		AppID: app.ID, Index: index, Status: "crashed", Reason: reason,
		ExitObservedAt: observedAt, ExitRunID: logRunID,
	}); err != nil {
		slog.Warn("recovery: persist crashed replica failed",
			"slug", app.Slug, "idx", index, "reason", reason, "err", err)
	}
	if logRunID != "" {
		if err := store.FinishAppLogRunWithExit(logRunID, "crashed", observedAt, false, nil, "", reason); err != nil && !errors.Is(err, db.ErrNotFound) {
			slog.Warn("recovery: close crashed log run failed",
				"slug", app.Slug, "idx", index, "run_id", logRunID, "err", err)
		}
	}
}

// derefInt64 dereferences a nullable int64 pointer, returning 0 for nil.
func derefInt64(p *int64) int64 {
	if p == nil {
		return 0
	}
	return *p
}

// recoverContainerReplica re-adopts a single container-backed replica by
// matching a live container's shinyhub.slug/replica_index labels to the replica
// row. containers is the lister's full container list (already fetched once).
// It returns true when the replica was adopted; a missing container, an
// out-of-pool index, or a missing port row leaves the replica unadopted so the
// watcher relaunches it.
func recoverContainerReplica(store *db.Store, mgr *process.Manager, prx *proxy.Proxy, app *db.App, r *db.Replica, lister ContainerLister, containers []process.ContainerInfo, logRunID string) bool {
	if r.DesiredState == db.ReplicaDesiredWarm {
		// Warm-parked by the idle shrink: expansion boots it, not recovery. A
		// 'suspended' warm row is a paused container that survived the restart and
		// cannot be re-adopted warm. Reap it (force-remove handles the paused state)
		// so it does not leak, then downgrade the row to stopped/warm so a later
		// expansion cold-boots a fresh container.
		if r.Status == "suspended" {
			for _, c := range containers {
				if c.Labels[process.LabelSlug] == app.Slug && c.Labels[process.LabelReplicaIndex] == strconv.Itoa(r.Index) {
					if rerr := lister.RemoveContainer(c.ID); rerr != nil {
						slog.Warn("recovery: reap frozen warm container", "slug", app.Slug, "idx", r.Index, "container", c.ID, "err", rerr)
					} else {
						slog.Info("recovery: reaped frozen warm container", "slug", app.Slug, "idx", r.Index, "container", c.ID)
					}
					break
				}
			}
			if err := store.UpsertReplica(db.UpsertReplicaParams{
				AppID:        app.ID,
				Index:        r.Index,
				Status:       "stopped",
				Provider:     r.Provider,
				Tier:         r.Tier,
				AppVersion:   r.AppVersion,
				DesiredState: db.ReplicaDesiredWarm,
				DeploymentID: r.DeploymentID,
			}); err != nil {
				slog.Warn("recovery: downgrade suspended warm container row", "slug", app.Slug, "idx", r.Index, "err", err)
			}
		}
		return false
	}
	if r.Index >= app.Replicas && r.ActivationID == nil {
		slog.Warn("recovery: replica index beyond current pool; skipping", "slug", app.Slug, "idx", r.Index, "pool", app.Replicas)
		return false
	}
	var matches []process.ContainerInfo
	wantDeployment := ""
	if r.DeploymentID != nil {
		wantDeployment = strconv.FormatInt(*r.DeploymentID, 10)
	}
	for _, c := range containers {
		if c.State != "" && c.State != "running" {
			// All-state inventory is intentional so the later orphan sweep can
			// remove exited containers. Only a running container is adoptable.
			continue
		}
		labels := c.Labels
		if labels[process.LabelSlug] != app.Slug || labels[process.LabelReplicaIndex] != strconv.Itoa(r.Index) {
			continue
		}
		if r.WorkerID != "" && c.ID != r.WorkerID {
			continue
		}
		if wantDeployment != "" && labels[process.LabelDeploymentID] != wantDeployment {
			continue
		}
		if r.AppVersion != "" && labels[process.LabelAppVersion] != "" && labels[process.LabelAppVersion] != r.AppVersion {
			continue
		}
		if r.Tier != "" && labels[process.LabelTier] != "" && labels[process.LabelTier] != r.Tier {
			continue
		}
		matches = append(matches, c)
	}
	if len(matches) == 0 {
		return false // no live container for this replica
	}
	if len(matches) != 1 {
		slog.Warn("recovery: ambiguous docker identity; refusing adoption",
			"slug", app.Slug, "idx", r.Index, "matches", len(matches))
		return false
	}
	cID := matches[0].ID
	pid, err := lister.InspectPID(cID)
	if err != nil {
		slog.Error("recovery: inspect docker container", "slug", app.Slug, "idx", r.Index, "err", err)
		return false
	}
	if pid <= 0 {
		slog.Warn("recovery: docker container is not running; refusing adoption", "slug", app.Slug, "idx", r.Index, "container", cID, "pid", pid)
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
		Slug:         app.Slug,
		AppID:        app.ID,
		Index:        r.Index,
		PID:          pid,
		Port:         port,
		Status:       process.StatusRunning,
		Tier:         r.Tier,
		Provider:     r.Provider,
		EndpointURL:  targetURL,
		WorkerID:     cID,
		AppVersion:   r.AppVersion,
		DeploymentID: derefInt64(r.DeploymentID),
		LogRunID:     logRunID,
	}, process.RunHandle{ContainerID: cID})
	if err := prx.RegisterReplica(app.Slug, r.Index, targetURL, nil, derefInt64(r.DeploymentID), app.ID); err != nil {
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

type fargateTaskTerminationObserver interface {
	TaskStopped(ctx context.Context, arn string) (bool, error)
}

type taskInventoryStabilizer interface {
	TaskInventoryStabilizationWindow() time.Duration
}

type taskInventoryPoller interface {
	TaskInventoryPollInterval() time.Duration
}

type taskSweepScoper interface {
	TaskSweepScope() string
}

// FenceOrphanScheduleTasksForTiers stops and then observes the disappearance
// of every ECS one-shot producer left by an earlier owner. A successful StopTask
// response is not enough: ECS may keep the task alive during its stop grace, so
// this loops until inventory proves no producer remains or ctx expires.
func FenceOrphanScheduleTasksForTiers(ctx context.Context, mgr *process.Manager, tierNames []string) error {
	if len(tierNames) == 0 {
		tierNames = []string{""}
	}
	seenScopes := make(map[string]struct{})
	for _, tierName := range tierNames {
		sweeper, ok := mgr.RuntimeForTier(tierName).(FargateTaskSweeper)
		if !ok {
			continue
		}
		observer, ok := sweeper.(fargateTaskTerminationObserver)
		if !ok {
			return fmt.Errorf("schedule task runtime for tier %q cannot observe physical task termination", tierName)
		}
		if scoped, ok := sweeper.(taskSweepScoper); ok {
			if scope := scoped.TaskSweepScope(); scope != "" {
				if _, seen := seenScopes[scope]; seen {
					continue
				}
				seenScopes[scope] = struct{}{}
			}
		}
		stableFor := 2 * time.Second
		if configured, ok := sweeper.(taskInventoryStabilizer); ok {
			stableFor = configured.TaskInventoryStabilizationWindow()
		}
		pollEvery := 500 * time.Millisecond
		if configured, ok := sweeper.(taskInventoryPoller); ok {
			pollEvery = configured.TaskInventoryPollInterval()
		}
		pendingStops := make(map[string]struct{})
		var emptySince time.Time
		for {
			tasks, err := sweeper.ListManagedTasks(ctx)
			if err != nil {
				return fmt.Errorf("list orphan schedule tasks for tier %q: %w", tierName, err)
			}
			producers := make([]process.TaskRef, 0)
			for _, task := range tasks {
				if task.Labels[process.LabelKind] == process.KindScheduleRun {
					producers = append(producers, task)
				}
			}
			for _, task := range producers {
				if _, alreadyStopped := pendingStops[task.ARN]; !alreadyStopped {
					if err := sweeper.StopTask(ctx, task.ARN); err != nil {
						return fmt.Errorf("stop orphan schedule task %s for tier %q: %w", task.ARN, tierName, err)
					}
					pendingStops[task.ARN] = struct{}{}
					slog.Info("task publication fence: stopping orphan producer", "arn", task.ARN, "tier", tierName)
				}
			}
			for arn := range pendingStops {
				stopped, err := observer.TaskStopped(ctx, arn)
				if err != nil {
					return fmt.Errorf("observe orphan schedule task %s for tier %q: %w", arn, tierName, err)
				}
				if stopped {
					delete(pendingStops, arn)
				}
			}
			if len(producers) == 0 && len(pendingStops) == 0 {
				if emptySince.IsZero() {
					emptySince = time.Now()
				}
				if time.Since(emptySince) >= stableFor {
					break
				}
			} else {
				emptySince = time.Time{}
			}
			select {
			case <-ctx.Done():
				return fmt.Errorf("wait for orphan schedule tasks on tier %q: %w", tierName, ctx.Err())
			case <-time.After(pollEvery):
			}
		}
	}
	return nil
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

// containerSweepScoper lets runtimes that share one backing container daemon
// expose a stable deduplication key. DockerRuntime uses its daemon endpoint, so
// multiple configured tiers do not enumerate and sweep the same daemon twice.
type containerSweepScoper interface {
	ContainerSweepScope() string
}

// FenceOrphanScheduleContainersForTiers synchronously removes every local
// container producer left by a previous control-plane owner. Unlike the later
// best-effort replica sweep, this is a correctness barrier: callers must not
// admit consumers when enumeration or removal is uncertain.
func FenceOrphanScheduleContainersForTiers(mgr *process.Manager, tierNames []string) error {
	if len(tierNames) == 0 {
		tierNames = []string{""}
	}
	seenScopes := make(map[string]struct{})
	for _, tierName := range tierNames {
		sweeper, ok := mgr.RuntimeForTier(tierName).(ContainerSweeper)
		if !ok {
			continue
		}
		if scoped, ok := sweeper.(containerSweepScoper); ok {
			if scope := scoped.ContainerSweepScope(); scope != "" {
				if _, seen := seenScopes[scope]; seen {
					continue
				}
				seenScopes[scope] = struct{}{}
			}
		}
		containers, err := sweeper.ListByLabel(process.ManagedContainerFilterJSON)
		if err != nil {
			return fmt.Errorf("list orphan schedule containers for tier %q: %w", tierName, err)
		}
		for _, c := range containers {
			if c.Labels[process.LabelKind] != process.KindScheduleRun {
				continue
			}
			if err := sweeper.RemoveHandle(process.RunHandle{ContainerID: c.ID}); err != nil {
				return fmt.Errorf("remove orphan schedule container %s for tier %q: %w", c.ID, tierName, err)
			}
			slog.Info("container publication fence: removed orphan producer",
				"container", c.ID, "slug", c.Labels[process.LabelSlug], "tier", tierName)
		}
	}
	return nil
}

// SweepOrphanContainersForTiers sweeps every registered container-backed tier.
// It complements durable per-replica recovery; it is not ownership proof.
func SweepOrphanContainersForTiers(mgr *process.Manager, tierNames []string) {
	if len(tierNames) == 0 {
		tierNames = []string{""}
	}
	seenScopes := make(map[string]struct{})
	for _, tierName := range tierNames {
		sweeper, ok := mgr.RuntimeForTier(tierName).(ContainerSweeper)
		if !ok {
			continue
		}
		if scoped, ok := sweeper.(containerSweepScoper); ok {
			if scope := scoped.ContainerSweepScope(); scope != "" {
				if _, seen := seenScopes[scope]; seen {
					continue
				}
				seenScopes[scope] = struct{}{}
			}
		}
		SweepOrphanContainers(mgr, sweeper)
	}
}

// SweepOrphanContainers removes ShinyHub-managed containers that no live
// replica owns. It must run AFTER RecoverProcesses, so containers the Manager
// re-adopted are protected. One-shot schedule containers are deliberately
// never adopted: a successor force-removes them here before opening consumer
// admission, so an orphan producer cannot keep mutating startup data after
// failover. A nil sweeper (native runtime) is a no-op.
func SweepOrphanContainers(mgr *process.Manager, sweeper ContainerSweeper) {
	if sweeper == nil {
		return
	}
	containers, err := sweeper.ListByLabel(process.ManagedContainerFilterJSON)
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

// markRecoveryDown handles an app whose replicas were all confirmed dead after a
// restart (their processes did not survive). It marks the app "hibernated" so the
// warm-restore pass re-boots and re-freezes it - pre-warming it exactly like an
// app that was already hibernated - rather than stranding a previously-running
// app in a dead "stopped" that never comes back on its own. With warm-wake off
// (no restore pass) the "hibernated" status still lets the next request cold-wake
// it. A genuinely-broken app fails to boot and is surfaced as "crashed".
func markRecoveryDown(store *db.Store, slug string) {
	if err := store.MarkRecoveryHibernated(slug); err != nil {
		slog.Error("process recovery: mark hibernated", "slug", slug, "err", err)
	}
}
