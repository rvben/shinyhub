package main

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/rvben/shinyhub/internal/activation"
	"github.com/rvben/shinyhub/internal/api"
	"github.com/rvben/shinyhub/internal/autoscale"
	"github.com/rvben/shinyhub/internal/config"
	"github.com/rvben/shinyhub/internal/db"
	"github.com/rvben/shinyhub/internal/jobs"
	"github.com/rvben/shinyhub/internal/lifecycle"
	"github.com/rvben/shinyhub/internal/lifecycle/scheduler"
	"github.com/rvben/shinyhub/internal/process"
	"github.com/rvben/shinyhub/internal/proxy"
	"github.com/rvben/shinyhub/internal/safego"
	"github.com/rvben/shinyhub/internal/worker"
)

// Worker-down monitor timings. The monitor is built in runServe and its loop
// runs as owner work.
const (
	workerTimeout   = 90 * time.Second
	monitorInterval = 30 * time.Second
	// workerRetention is how long a down, non-revoked worker row with no
	// live replicas is kept before being reaped. It is far longer than the
	// down timeout so a briefly-flapping worker is never tombstoned.
	workerRetention = 24 * time.Hour
)

// ownerRuntime is the work a control-plane owner does for one ownership
// epoch: fenced recovery of everything a previous owner left behind, the owner
// loops, and the bounded handoff when ownership ends. The elector calls run
// once per epoch through a leader.OwnerScope; every dependency is a field, so
// what owner work can touch is visible in one place.
type ownerRuntime struct {
	cfg                        *config.Config
	store                      *db.Store
	srv                        *api.Server
	mgr                        *process.Manager
	prx                        *proxy.Proxy
	jobsMgr                    *jobs.Manager
	sched                      *scheduler.Scheduler
	watcher                    *lifecycle.Watcher
	workerReg                  *worker.Registry
	monitor                    *lifecycle.WorkerDownMonitor
	controller                 *autoscale.Controller
	activationCoordinator      *activation.Coordinator
	reconcileCleaner           lifecycle.AppSecretsCleaner
	appLogTelemetry            appLogMaintenanceMetrics
	syncDeployCredential       func() error
	retiringParentDone         <-chan struct{}
	releaseRetiringParentFence func()

	// ready reports that recovery is complete and the worker routing index
	// has been refreshed for the current ownership span. The mutation gates
	// require it (not just IsOwner): the elector flips IsOwner true before run
	// starts, so without it a freshly-acquired owner could admit
	// deploy-placement / worker mutations against a stale index.
	ready atomic.Bool
	// handoffIncomplete records that an ownership handoff drain gave up with
	// work still running; see drainOwnershipHandoff. It is sticky for the
	// process.
	handoffIncomplete atomic.Bool
}

// run is one ownership epoch. It returns when octx is cancelled (ownership
// lost or shutdown) after the handoff, or early when ownership is lost
// before recovery completes.
func (r *ownerRuntime) run(octx context.Context, epoch int64) {
	cfg, store, srv, mgr, prx := r.cfg, r.store, r.srv, r.mgr, r.prx
	jobsMgr, sched, watcher := r.jobsMgr, r.sched, r.watcher
	workerReg, monitor, controller, activationCoordinator := r.workerReg, r.monitor, r.controller, r.activationCoordinator
	reconcileCleaner, appLogTelemetry, syncDeployCredential := r.reconcileCleaner, r.appLogTelemetry, r.syncDeployCredential
	retiringParentDone, releaseRetiringParentFence := r.retiringParentDone, r.releaseRetiringParentFence
	ownerReady, handoffIncomplete := &r.ready, &r.handoffIncomplete

	ownerReady.Store(false)       // closed at the start of every ownership span
	defer ownerReady.Store(false) // and on span exit (ownership lost / shutdown)
	slog.Info("became control-plane owner", "epoch", epoch, "instance", cfg.Server.InstanceID)
	retryOwnerStep := func(label string, fn func() error) bool {
		for {
			if err := fn(); err == nil {
				return true
			} else {
				slog.Error(label, "err", err)
			}
			select {
			case <-octx.Done():
				return false
			case <-time.After(registryRefreshBackoff):
			}
		}
	}

	// Reconcile the environment-managed deploy credential before opening the
	// owner readiness gate. Standbys authenticate against the same database but
	// never write this singleton configuration state themselves.
	for {
		if err := syncDeployCredential(); err == nil {
			break
		} else {
			slog.Error("reconcile deploy token configuration", "err", err)
		}
		select {
		case <-octx.Done():
			return
		case <-time.After(registryRefreshBackoff):
		}
	}

	// Rebuild the worker routing index from the authoritative DB so this new
	// active reflects every worker row the previous owner wrote before it died.
	// Fail-closed: retry until it succeeds or ownership is lost, so owner work
	// (placement-affecting reconciles, loops, and admitted mutations) never runs
	// on a stale index. A persistent failure means the DB is unusable, where no
	// owner work could function anyway.
	if workerReg != nil && !refreshUntilReady(octx, workerReg, registryRefreshBackoff, slog.Default()) {
		return // lost ownership before a fresh index; start no owner work
	}
	// Rewrite any legacy relative bundle_dir rows to their canonical absolute
	// path under the configured apps_dir. Idempotent; must run before recovery
	// so process adoption sees absolute paths.
	if err := lifecycle.NormalizeBundleDirs(store, cfg.Storage.AppsDir); err != nil {
		slog.Error("bundle_dir backfill", "err", err)
	}
	// Fence startup's destructive reconciliation/adoption across every app.
	// This waits for lifecycle handlers that may still be unwinding on a
	// retiring process, including handlers that outlived HTTP shutdown.
	var releaseStartupFence func()
	if !retryOwnerStep("acquire startup app-operation fence", func() error {
		var err error
		releaseStartupFence, err = srv.AcquireFleetAppOperations()
		return err
	}) {
		return
	}
	startupFenceHeld := true
	defer func() {
		if startupFenceHeld {
			releaseStartupFence()
		}
	}()
	// A Docker one-shot producer survives a control-plane crash. Force-remove
	// every such orphan and retry on any daemon uncertainty before recovery can
	// adopt or boot a consumer over data that is still being mutated.
	for {
		fenceCtx, cancelFence := context.WithTimeout(octx, 60*time.Second)
		containerErr := lifecycle.FenceOrphanScheduleContainersForTiers(mgr, cfg.Runtime.TierOrder())
		taskErr := lifecycle.FenceOrphanScheduleTasksForTiers(fenceCtx, mgr, cfg.Runtime.TierOrder())
		cancelFence()
		if containerErr == nil && taskErr == nil {
			break
		}
		slog.Error("fence orphan schedule producers", "container_err", containerErr, "task_err", taskErr)
		select {
		case <-octx.Done():
			return
		case <-time.After(registryRefreshBackoff):
		}
	}
	var appsForPublicationFence []*db.App
	if !retryOwnerStep("list apps for publication recovery fence", func() error {
		var err error
		appsForPublicationFence, err = store.ListApps(0, 0)
		return err
	}) {
		return
	}
	appIDsForPublicationFence := make([]int64, 0, len(appsForPublicationFence))
	for _, app := range appsForPublicationFence {
		appIDsForPublicationFence = append(appIDsForPublicationFence, app.ID)
	}
	var releasePublicationFences func()
	if !retryOwnerStep("acquire publication recovery fences", func() error {
		var err error
		releasePublicationFences, err = jobsMgr.AcquirePublicationRecoveryFences(octx, appIDsForPublicationFence)
		return err
	}) {
		return
	}
	publicationFencesHeld := true
	defer func() {
		if publicationFencesHeld {
			releasePublicationFences()
		}
	}()
	// A previous owner may have completed while we waited for its physical
	// writer lock. Only rows still running now are truly interrupted/uncertain.
	var interruptedRuns []db.ScheduleRunTransition
	if !retryOwnerStep("terminalize inherited schedule runs", func() error {
		var err error
		interruptedRuns, err = store.MarkRunningSchedulesInterrupted()
		return err
	}) {
		return
	} else if len(interruptedRuns) > 0 {
		slog.Warn("terminalized inherited schedule runs", "count", len(interruptedRuns))
		jobsMgr.ReportScheduleRunTransitions(interruptedRuns...)
	}
	// Resolve interrupted deployments before recovery. Fail closed and retry:
	// a candidate producer may already have published incompatible shared data,
	// in which case prior consumers must never be adopted.
	for {
		if err := lifecycle.ReconcileInflightDeployments(store); err == nil {
			break
		} else {
			slog.Error("reconcile interrupted deployments", "err", err)
		}
		select {
		case <-octx.Done():
			return
		case <-time.After(registryRefreshBackoff):
		}
	}
	// Migrations may have completed while a predecessor owner still had an
	// admitted legacy deploy handler. The exclusive startup fence above now
	// proves those writers are gone, so repair tokens and active pointers from
	// the final succeeded ledger before process recovery consults them.
	if !retryOwnerStep("repair deployment generation ledger", store.RepairDeploymentGenerationLedger) {
		return
	}
	// Finish any app deletion interrupted between the 'deleting' tombstone and
	// the row removal. Reconcile first so freshly-cleaned slugs are not flagged
	// as orphans by the next step.
	lifecycle.ReconcileDeletingApps(octx, store, cfg, reconcileCleaner)
	// Report (do not delete) slug dirs with no owning row. Run AFTER
	// ReconcileDeletingApps so freshly-cleaned slugs are not reported.
	lifecycle.LogOrphanAppDirs(store, cfg)
	// Resolve every running app and its active bundle directory before
	// recovery mutates anything. Fail closed and retry: a mid-scan database
	// error must never fall through as an unresolved bundle directory, which
	// would let validateNativeProcessIdentity skip its cwd check and adopt a
	// reused PID on port evidence alone, or make reAdoptFrozenWarmReplica fail
	// closed and rewrite a still-SIGSTOPped frozen-warm row to stopped.
	var recoveryInputs *lifecycle.RecoveryInputs
	if !retryOwnerStep("prepare process recovery", func() error {
		var err error
		recoveryInputs, err = lifecycle.PrepareRecovery(store)
		return err
	}) {
		return
	}
	// Re-adopt any processes that survived a server restart. Must run after
	// ReconcileInflightDeployments so recovery adopts the last-good deployment,
	// not a half-applied one.
	var previouslyRunning []*db.App
	if !retryOwnerStep("list apps for startup warm floors", func() error {
		var err error
		previouslyRunning, err = store.ListRunningApps()
		return err
	}) {
		return
	}
	lifecycle.RecoverProcesses(store, mgr, prx, cfg.Runtime.DefaultMaxSessionsPerReplica, cfg.Auth.IdentityHeadersEnabled(), cfg.Runtime.DefaultWorkerIsolation, watcher, recoveryInputs)
	// Stop any native processes in the Manager that belong to elastic-mode
	// apps. Elastic workers are ephemeral and must not be re-adopted; the
	// pool starts empty and clients trigger fresh spawns on next request.
	lifecycle.ReapElasticOrphans(store, mgr, cfg.Runtime.DefaultWorkerIsolation)
	// Apps that ran elastic workers under a version without durable worker
	// identities carry an orphan-risk marker. With every known worker
	// stopped, a free consumer-lifetime lock proves no unknown one survived,
	// and the marker no longer needs to block producer schedules.
	lifecycle.DischargeElasticOrphanRisk(store, jobsMgr)
	// Remove ShinyHub-managed containers no live replica re-adopted across
	// every configured local container tier. Shared Docker daemons are
	// deduplicated by endpoint.
	if err := lifecycle.SweepOrphanContainersForTiersWithGenerations(store, mgr, cfg.Runtime.TierOrder()); err != nil {
		// An orphan may still hold an old result cache namespace open, so
		// pruning waits for the next deploy or retirement instead.
		slog.Warn("container sweep incomplete; skipping startup result cache prune", "err", err)
	} else {
		lifecycle.PruneAppCaches(store, cfg.Storage.AppCacheDir)
	}
	// Sweep orphan Fargate tasks across ALL registered tiers.
	sweepCtx, cancelSweep := context.WithTimeout(octx, 60*time.Second)
	for _, tierName := range cfg.Runtime.TierOrder() {
		tierRT := mgr.RuntimeForTier(tierName)
		if fts, ok := tierRT.(lifecycle.FargateTaskSweeper); ok {
			lifecycle.SweepOrphanFargateTasks(sweepCtx, mgr, fts)
		}
	}
	cancelSweep()

	// A crash may leave an activation marked running. Process/container
	// recovery above has adopted or swept any surviving surge first; generation
	// stamps then let the retried rollout skip canonical slots already replaced.
	if !isClustered(cfg) {
		if n, err := store.RequeueRunningScheduleActivations(time.Now().UTC()); err != nil {
			slog.Error("requeue interrupted schedule activations", "err", err)
		} else if n > 0 {
			slog.Info("requeued interrupted schedule activations", "count", n)
		}
	}
	// Start the scheduler before opening owner readiness. Admissions are
	// temporarily reopened while every recovery fence is still held so missed
	// and obligation dispatch can register durably but cannot cross a physical
	// writer/consumer boundary. A transient scheduler load failure is retried
	// within this ownership span rather than leaving a permanently green owner
	// with dead cron processing.
	jobsMgr.ResumeAdmissions()
	if !retryOwnerStep("start scheduler", func() error { return sched.Start(octx) }) {
		sched.Stop()
		if err := jobsMgr.InterruptAndDrain(context.Background()); err != nil {
			slog.Error("drain scheduled jobs after failed owner startup", "err", err)
		}
		return
	}
	// Ownership may have been cancelled between the successful Start call and
	// this continuation. Never briefly advertise readiness or release the
	// recovery fences for an already-retired owner.
	if octx.Err() != nil {
		sched.Stop()
		if err := jobsMgr.InterruptAndDrain(context.Background()); err != nil {
			slog.Error("drain scheduled jobs after ownership loss during startup", "err", err)
		}
		return
	}
	slog.Info("scheduler started")
	releasePublicationFences()
	publicationFencesHeld = false
	releaseStartupFence()
	startupFenceHeld = false
	if warning := srv.UpdateCPUCapacityWarning(); warning != "" {
		slog.Warn("live-update capacity after worker recovery", "warning", warning)
	}
	ownerReady.Store(true) // recovery complete and worker index is fresh
	// A settings redeploy whose process exited before reporting is still
	// owed; serve it now that this owner can boot pools. The scan retries
	// through transient store failures, so it runs beside the owner's
	// loops instead of delaying them.
	relaunchOwed := func() { safego.Go("relaunch owed redeploys", srv.RelaunchOwedRedeploys) }
	if !prx.RetiringParentAlive() {
		relaunchOwed()
	}

	var loops sync.WaitGroup
	// startLoop runs one owner loop for this ownership epoch. The shutdown
	// branch below waits on loops before ownership is released, so every
	// loop has returned before a successor can take over.
	startLoop := func(run func()) {
		loops.Add(1)
		// rungroup:exempt: joined by loops.Wait in this epoch's shutdown branch, under the ownership watchdog
		go func() {
			defer loops.Done()
			run()
		}()
	}
	if retiringParentDone != nil && prx.RetiringParentAlive() {
		// Recovery retains obsolete multiplex generations while the parent
		// proxies them. Keep the lifecycle fence until cleanup finishes,
		// so no new candidate or draining generation can enter this pass.
		startLoop(func() {
			runRetiringParentCleanup(octx, retiringParentDone, recoveryInputs.Apps,
				srv.TryAcquireAppOperation, func(recovered *db.App) error {
					app, err := store.GetAppByID(recovered.ID)
					if errors.Is(err, db.ErrNotFound) {
						return nil
					}
					if err != nil {
						return err
					}
					// Parent exit is confirmed; the separate lifecycle fence
					// stays held until every app in this pass is processed.
					lifecycle.CleanupObsoleteDeploymentGenerationsAfterParent(store, mgr, prx, app)
					return nil
				}, func() {
					releaseRetiringParentFence()
					relaunchOwed()
				})
		})
	}
	if monitor != nil {
		startLoop(func() { monitor.Run(octx, monitorInterval) })
		slog.Info("worker-down monitor started", "timeout", workerTimeout, "interval", monitorInterval, "retention", workerRetention)
	}
	startLoop(func() { watcher.Start(octx) })
	if !isClustered(cfg) {
		startLoop(func() { activationCoordinator.Start(octx) })
		slog.Info("schedule activation coordinator started", "concurrency", 1)
	}
	// Database housekeeping (audit + schedule-run retention) runs on the owner
	// only, so HA standbys never prune concurrently. A no-op unless retention
	// is configured.
	startLoop(func() { runMaintenance(octx, store, mgr, appLogTelemetry, cfg.Maintenance, cfg.Usage) })
	// A file-backed SQLite database is only ever served by one process, so
	// its WAL checkpoint and planner statistics belong with the owner loops.
	startLoop(func() { runSQLiteMaintenance(octx, store, cfg.Maintenance.Interval) })
	startLoop(func() { srv.RunDevelopmentAppReaper(octx, time.Minute) })
	// Restore serving floors independently of optional frozen warm-wake.
	// Sequence the two passes so frozen restoration cannot claim a serving app.
	startLoop(func() {
		watcher.RestoreWarmFloors(octx, previouslyRunning)
		if cfg.Runtime.Snapshot.Enabled && cfg.Runtime.Snapshot.RestoreOnStartup {
			watcher.RestoreWarm(octx)
		}
	})
	if controller != nil {
		startLoop(func() { controller.Run(octx) })
		slog.Info("autoscale controller started",
			"scan_interval", cfg.Runtime.Autoscale.ScanInterval,
			"cooldown", cfg.Runtime.Autoscale.Cooldown,
			"default_target", cfg.Runtime.Autoscale.DefaultTarget)
	}

	<-octx.Done()
	ownerReady.Store(false) // close admission before waiting for local handlers
	slog.Info("releasing control-plane ownership loops", "epoch", epoch)
	sched.Stop() // restartable on the next acquisition
	loops.Wait() // watcher/monitor/autoscale fully stopped
	// Job cancellation must never wait behind the fleet fence: a successor
	// request can hold fleet-SH while waiting for this owner's publication
	// lock. Drain first, then use fleet-EX to close the remaining HTTP/lifecycle
	// handoff. Successor startup independently waits physical writer locks.
	// Both steps share one bounded drainCtx so neither can hang the handoff
	// indefinitely. The exclusive fleet fence drains requests admitted before
	// handoff (including uploads not yet at an app lock) while the successor
	// is still excluded, so their durable terminal state lands before a new
	// scheduler can reconcile running rows. When either step gives up at the
	// deadline, the lease is left to expire on its TTL instead of being
	// released, so a successor never overlaps goroutines still writing.
	drainCtx, cancelDrain := context.WithTimeout(context.Background(), shutdownDrainBudget)
	defer cancelDrain()
	if !drainOwnershipHandoff(drainCtx, jobsMgr.InterruptAndDrain, srv.AcquireFleetHandoffBarrier, registryRefreshBackoff, slog.Default()) {
		handoffIncomplete.Store(true)
	}
}
