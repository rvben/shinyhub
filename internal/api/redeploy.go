package api

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/rvben/shinyhub/internal/appstatus"
	"github.com/rvben/shinyhub/internal/auth"
	"github.com/rvben/shinyhub/internal/db"
	"github.com/rvben/shinyhub/internal/deploy"
	"golang.org/x/sys/unix"
)

// deployLockFor returns the per-slug mutex used to serialize all
// deploy/restart/rollback/stop/delete operations against the same app. The
// map grows by one *sync.Mutex per distinct slug observed; that's bounded by
// the app catalog and small enough to leave in place even after an app is
// deleted (re-creating the same slug gets the same mutex, which is fine).
func (s *Server) deployLockFor(slug string) *sync.Mutex {
	s.deployLocksMu.Lock()
	defer s.deployLocksMu.Unlock()
	if s.deployLocks == nil {
		s.deployLocks = make(map[string]*sync.Mutex)
	}
	m, ok := s.deployLocks[slug]
	if !ok {
		m = &sync.Mutex{}
		s.deployLocks[slug] = m
	}
	return m
}

// acquireDeployLock blocks until the per-slug deploy lock is held. The
// returned func releases it; pair with `defer release()` at the call site.
// Use this from HTTP handlers, which should provide backpressure (the second
// concurrent deploy waits for the first) rather than silently dropping work.
// While held, the slug is tracked in deployInFlight so DeployInFlight can
// report whether this instance is actively executing a lock-holding operation.
func (s *Server) acquireDeployLock(slug string) (release func()) {
	release, err := s.acquireAppOperation(slug, false)
	if err != nil {
		// Every caller treats successful return as authority to mutate runtime and
		// durable state. Panicking is fail-closed and is recovered by HTTP/background
		// operation boundaries; proceeding without the cross-process fence is not.
		panic(fmt.Sprintf("acquire app operation fence for %s: %v", slug, err))
	}
	return release
}

func (s *Server) acquireAppOperation(slug string, nonblocking bool) (release func(), err error) {
	return s.acquireAppOperationWithFleetFence(slug, nonblocking, true)
}

func (s *Server) acquireAppOperationWithFleetFence(slug string, nonblocking, joinFleetFence bool) (release func(), err error) {
	releaseFleet := func() {}
	if joinFleetFence {
		mode := unix.LOCK_SH
		if nonblocking {
			mode |= unix.LOCK_NB
		}
		var fleetErr error
		releaseFleet, fleetErr = s.acquireFleetMutationFence(mode)
		if fleetErr != nil {
			if nonblocking && (errors.Is(fleetErr, unix.EWOULDBLOCK) || errors.Is(fleetErr, unix.EAGAIN)) {
				return nil, nil
			}
			return nil, fleetErr
		}
	}
	m := s.deployLockFor(slug)
	if nonblocking {
		if !m.TryLock() {
			releaseFleet()
			return nil, nil
		}
	} else {
		m.Lock()
	}
	lockName := fmt.Sprintf("%x.lifecycle.lock", sha256.Sum256([]byte(slug)))
	path := filepath.Join(s.appOperationLockDir, lockName)
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o640)
	if err != nil {
		m.Unlock()
		releaseFleet()
		return nil, fmt.Errorf("open lifecycle lock: %w", err)
	}
	mode := unix.LOCK_EX
	if nonblocking {
		mode |= unix.LOCK_NB
	}
	if err := unix.Flock(int(f.Fd()), mode); err != nil {
		_ = f.Close()
		m.Unlock()
		releaseFleet()
		if nonblocking && (err == unix.EWOULDBLOCK || err == unix.EAGAIN) {
			return nil, nil
		}
		return nil, fmt.Errorf("flock lifecycle lock: %w", err)
	}
	releaseLocal := s.markAppOperationHeld(slug, m)
	var once sync.Once
	return func() {
		once.Do(func() {
			_ = unix.Flock(int(f.Fd()), unix.LOCK_UN)
			_ = f.Close()
			releaseLocal()
			releaseFleet()
		})
	}, nil
}

func (s *Server) acquireFleetMutationFence(mode int) (func(), error) {
	path := filepath.Join(s.appOperationLockDir, "fleet.lifecycle.lock")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o640)
	if err != nil {
		return nil, fmt.Errorf("open fleet lifecycle lock: %w", err)
	}
	if err := unix.Flock(int(f.Fd()), mode); err != nil {
		_ = f.Close()
		return nil, err
	}
	var once sync.Once
	return func() {
		once.Do(func() {
			_ = unix.Flock(int(f.Fd()), unix.LOCK_UN)
			_ = f.Close()
		})
	}, nil
}

// TryAcquireAppOperation atomically acquires the same per-app exclusion used by
// deploy, scale, stop, warm maintenance, and schedule activation. Background
// lifecycle work uses the non-blocking form so one slow app never stalls the
// fleet watcher; a busy app is reconsidered on its next tick.
func (s *Server) TryAcquireAppOperation(slug string) (release func(), ok bool) {
	release, err := s.acquireAppOperation(slug, true)
	if err != nil {
		slog.Error("acquire background app operation fence", "slug", slug, "err", err)
		return nil, false
	}
	return release, release != nil
}

// AcquireAppOperation serializes a foreground demand operation with every
// deploy, rollback, scale, stop, and activation for the same app. Unlike the
// watcher-oriented Try form, demand paths may wait because they already run in
// dedicated goroutines and must not abandon an admitted client reservation.
func (s *Server) AcquireAppOperation(slug string) (func(), error) {
	return s.acquireAppOperation(slug, false)
}

// WaitForAppOperations blocks until every lifecycle mutation admitted by this
// server has released its per-app fence. Ownership shutdown uses it before the
// durable lease is released, so a long deploy handler cannot outlive graceful
// handoff and race successor recovery.
func (s *Server) WaitForAppOperations(ctx context.Context) error {
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		s.deployLocksMu.Lock()
		idle := len(s.deployInFlight) == 0
		s.deployLocksMu.Unlock()
		if idle {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

// AcquireFleetAppOperations forms a startup barrier across every catalogued
// app. A successor holds it while reconciling pending deployments and adopting
// processes, preventing those destructive scans from racing a handler still
// alive on a retiring process. The catalog is re-read after acquisition so an
// app committed while the first snapshot was waiting is fenced too.
func (s *Server) AcquireFleetAppOperations() (func(), error) {
	releaseFleet, err := s.acquireFleetMutationFence(unix.LOCK_EX)
	if err != nil {
		return nil, fmt.Errorf("acquire fleet lifecycle startup fence: %w", err)
	}
	held := make(map[string]func())
	releaseAll := func() {
		slugs := make([]string, 0, len(held))
		for slug := range held {
			slugs = append(slugs, slug)
		}
		sort.Sort(sort.Reverse(sort.StringSlice(slugs)))
		for _, slug := range slugs {
			held[slug]()
		}
		releaseFleet()
	}
	for {
		apps, err := s.store.ListApps(0, 0)
		if err != nil {
			releaseAll()
			return nil, fmt.Errorf("list apps for lifecycle startup fence: %w", err)
		}
		missing := make([]string, 0)
		for _, app := range apps {
			if _, ok := held[app.Slug]; !ok {
				missing = append(missing, app.Slug)
			}
		}
		if len(missing) == 0 {
			return releaseAll, nil
		}
		sort.Strings(missing)
		for _, slug := range missing {
			release, err := s.acquireAppOperationWithFleetFence(slug, false, false)
			if err != nil {
				releaseAll()
				return nil, err
			}
			held[slug] = release
		}
	}
}

func (s *Server) markAppOperationHeld(slug string, m *sync.Mutex) func() {
	s.deployLocksMu.Lock()
	if s.deployInFlight == nil {
		s.deployInFlight = make(map[string]struct{})
	}
	s.deployInFlight[slug] = struct{}{}
	s.deployLocksMu.Unlock()
	var once sync.Once
	return func() {
		once.Do(func() {
			s.deployLocksMu.Lock()
			delete(s.deployInFlight, slug)
			s.deployLocksMu.Unlock()
			m.Unlock()
		})
	}
}

// DeployInFlight reports whether this instance currently holds the per-slug
// deploy lock for slug (a deploy, rollback, restart, stop, or delete is
// executing). The proxy's miss-status lookup combines it with the pending
// deployment row to tell a live deploy window apart from a stale pending row
// left by a PromoteDeployment failure. Cheap: one small map read.
func (s *Server) DeployInFlight(slug string) bool {
	s.deployLocksMu.Lock()
	defer s.deployLocksMu.Unlock()
	_, ok := s.deployInFlight[slug]
	return ok
}

// appDeploying reports whether a deployment or rollback for app is actively
// executing right now: its newest deployment row is pending AND this instance
// holds the app's deploy lock. It feeds the dashboard's "Deploying" badge.
//
// Deliberately stricter than db.App.MissStatus, which stays row-only for
// non-terminal statuses so clustered standbys (which never hold the lock)
// keep serving the deploying wait page on a miss. The badge renders even
// while a pool is live and healthy, so a row-only rule would pin a false
// "Deploying" badge on a running app whose pending row went stale after a
// PromoteDeployment failure. Requiring the lock keeps the badge exact on the
// instance executing the deploy; standbys simply do not light it.
func (s *Server) appDeploying(app *db.App) bool {
	return app.LastDeploymentStatus == db.DeploymentPending && s.DeployInFlight(app.Slug)
}

// decorateApp fills the transient, per-request fields on an app payload that are
// computed rather than stored. Every handler returning an app to a client calls
// it, so a consumer sees the same derived view whichever endpoint it read.
func (s *Server) decorateApp(app *db.App) {
	app.Deploying = s.appDeploying(app)
	app.EffectiveWorkerIsolation = deploy.ResolveWorkerIsolation(
		app.WorkerIsolation, s.cfg.Runtime.DefaultWorkerIsolation)
	if app.HibernateTimeoutMinutes != nil {
		app.EffectiveHibernateTimeoutMinutes = float64(*app.HibernateTimeoutMinutes)
	} else {
		app.EffectiveHibernateTimeoutMinutes = s.cfg.Lifecycle.HibernateTimeout.Minutes()
	}
	app.EffectiveMaxSessionsPerReplica = deploy.ResolveMaxSessionsPerReplica(
		app.MaxSessionsPerReplica, s.cfg.Runtime.DefaultMaxSessionsPerReplica)
	defaultMem, defaultCPU := s.cfg.Runtime.DefaultResourcesForApp(app)
	app.EffectiveMemoryLimitMB = deploy.ResolveMemoryLimitMB(app.MemoryLimitMB, defaultMem)
	app.EffectiveCPUQuotaPercent = deploy.ResolveCPUQuotaPercent(app.CPUQuotaPercent, defaultCPU)
	app.SessionsCeiling = configuredSessionsCeiling(app)
}

// decorateAppForCaller is decorateApp plus the two derived views a plain
// database read cannot supply: the live process observation, and can_manage.
// Every handler that answers a mutation with a freshly read app row calls it,
// so the row a client merges back onto its model says the same thing the list
// and detail payloads say about the same app.
//
// Without it the row is not merely incomplete, it is wrong: GetAppBySlug leaves
// every computed field at its zero value and none of them is omitempty, so the
// response states positively that the app has no session ceiling, no effective
// hibernate timeout, no running replicas, and that the caller may not manage it.
// A client merging that row hides every management control on an app the caller
// has just successfully managed.
//
// An observation failure is logged and skipped rather than surfaced. The
// mutation already succeeded; failing its response would push the caller into
// retrying an action that is done.
func (s *Server) decorateAppForCaller(u *auth.ContextUser, app *db.App) {
	s.decorateApp(app)
	app.CanManage = s.effectiveCanManageApp(u, app)
	replicas, err := s.store.ListReplicas(app.ID)
	if err != nil {
		slog.Error("decorate app: list replicas", "slug", app.Slug, "err", err)
		return
	}
	replicas = s.liveReplicaView(app.Slug, replicas)
	pool := s.elasticObservation(app.Slug)
	// A server built without a process manager (unit tests and API-only
	// embeddings) has no live authority. Preserve the stored status when there
	// are no durable replica facts to contradict it.
	if s.manager != nil || len(replicas) > 0 || pool.Known {
		s.decorateAppObservation(app, replicas, pool)
	}
}

// configuredSessionsCeiling gives sessions_ceiling one stable meaning on every
// isolation mode: configured admission capacity, independent of how many
// processes happen to be live at the instant of the read. A zero cap is
// uncapped and therefore remains zero rather than pretending to be capacity.
func configuredSessionsCeiling(app *db.App) int {
	switch app.EffectiveWorkerIsolation {
	case "grouped":
		if app.WorkerMaxWorkers <= 0 || app.WorkerGroupedSize <= 0 {
			return 0
		}
		return app.WorkerMaxWorkers * app.WorkerGroupedSize
	case "per_session":
		if app.WorkerMaxWorkers <= 0 {
			return 0
		}
		return app.WorkerMaxWorkers
	default:
		cap := app.EffectiveMaxSessionsPerReplica
		if cap <= 0 {
			return 0
		}
		return app.Replicas * cap
	}
}

// elasticPool is the live shape of an elastic (grouped / per_session) pool as
// the app observation needs it. Known is false when the proxy has no elastic
// pool registered for the app: multiplex apps, and elastic apps that are
// stopped or not yet deployed.
type elasticPool struct {
	Known   bool
	Running int // workers serving right now; reported as workers_running
	Booting int // booting, suspending or resuming: not assignable yet
	Total   int // every worker slot the proxy tracks, whatever its state
}

// observe folds one worker's status into p. The proxy's routing labels are
// booting, running, suspending, suspended, resuming and draining; the metrics
// path may substitute the manager's health view (crashed, stopped) for a
// tracked worker. A frozen warm spare (suspended) is ready but not running:
// it resumes on the first request, so a pool holding only frozen spares is
// idle, never stuck in starting. A spare still being frozen (suspending) is
// mid-provisioning and unassignable, exactly like a booting worker, so it
// counts as booting; otherwise a pool whose only slot is freezing would read
// idle while the allocator still rejects. Draining workers are on their way
// out and count toward neither.
func (p *elasticPool) observe(status string) {
	p.Total++
	switch status {
	case "running":
		p.Running++
	case "booting", "suspending", "resuming":
		p.Booting++
	}
}

// decorateAppObservation overlays process reality onto the stored lifecycle
// state. desired_status preserves the database value; status becomes the field
// operators expect: whether the app is actually serving, idle, or unhealthy.
// Every value written to app.Status is a constant from internal/appstatus, the
// vocabulary the CLI's readiness gates classify.
func (s *Server) decorateAppObservation(app *db.App, replicas []*db.Replica, pool elasticPool) {
	if app.DesiredStatus == "" {
		app.DesiredStatus = app.Status
	}
	app.ReplicasRunning = 0
	app.WorkersRunning = pool.Running
	app.LastReplicaError = ""
	app.LastReplicaExit = nil

	if pool.Known {
		// Elastic pools are demand-driven and have no durable replica rows.
		// Their empty healthy state is idle: the first request boots a worker,
		// so idle is a serving state, not a gap. Workers on their way up make
		// the app starting; a live worker makes it running.
		switch {
		case pool.Running > 0:
			app.Status = appstatus.Running
		case pool.Booting > 0:
			app.Status = appstatus.Starting
		case app.DesiredStatus == appstatus.Running || app.DesiredStatus == appstatus.Degraded:
			app.Status = appstatus.Idle
		default:
			app.Status = app.DesiredStatus
		}
		return
	}

	crashed, lost, starting, intentionallyParked := 0, 0, 0, 0
	var lastReplicaErrorAt time.Time
	var lastReplicaExitAt time.Time
	for _, rep := range replicas {
		if rep == nil {
			continue
		}
		if rep.LastExit != nil {
			observed := time.Time{}
			if rep.LastExit.ObservedAt != nil {
				observed = *rep.LastExit.ObservedAt
			}
			if app.LastReplicaExit == nil || observed.After(lastReplicaExitAt) {
				copy := *rep.LastExit
				app.LastReplicaExit = &copy
				lastReplicaExitAt = observed
			}
		}
		switch rep.Status {
		case db.ReplicaStatusRunning:
			app.ReplicasRunning++
		case "crashed":
			if rep.Reason != "" && (app.LastReplicaError == "" || rep.UpdatedAt.After(lastReplicaErrorAt)) {
				app.LastReplicaError = rep.Reason
				lastReplicaErrorAt = rep.UpdatedAt
			}
			if rep.DesiredState == "" || rep.DesiredState == "running" {
				crashed++
			}
		case db.ReplicaStatusLost:
			if rep.Reason != "" && (app.LastReplicaError == "" || rep.UpdatedAt.After(lastReplicaErrorAt)) {
				app.LastReplicaError = rep.Reason
				lastReplicaErrorAt = rep.UpdatedAt
			}
			if rep.DesiredState == "" || rep.DesiredState == "running" {
				lost++
			}
		case "starting", "booting", "resuming":
			starting++
		default:
			if rep.DesiredState == db.ReplicaDesiredWarm {
				intentionallyParked++
			}
		}
	}

	switch {
	case app.ReplicasRunning > 0 && crashed+lost > 0:
		app.Status = appstatus.Degraded
	case app.ReplicasRunning > 0:
		app.Status = appstatus.Running
	case crashed > 0:
		app.Status = appstatus.Crashed
	case lost > 0:
		app.Status = appstatus.Degraded
	case starting > 0:
		app.Status = appstatus.Starting
	case intentionallyParked > 0 && app.DesiredStatus != appstatus.Stopped:
		app.Status = appstatus.Hibernated
	case len(replicas) == 0 && (app.DesiredStatus == appstatus.Running || app.DesiredStatus == appstatus.Degraded):
		app.Status = appstatus.Degraded
	default:
		app.Status = app.DesiredStatus
	}
	if app.LastReplicaError == "" && app.Status == appstatus.Crashed {
		app.LastReplicaError = app.LastError
	}
}

// dataLockFor returns the per-slug mutex used to serialize the quota check
// and disk write inside handleDataPut. Without it two concurrent uploads can
// each read the same pre-write usage, both pass the quota check, and the
// resulting on-disk total exceeds the per-app cap.
func (s *Server) dataLockFor(slug string) *sync.Mutex {
	s.dataLocksMu.Lock()
	defer s.dataLocksMu.Unlock()
	if s.dataLocks == nil {
		s.dataLocks = make(map[string]*sync.Mutex)
	}
	m, ok := s.dataLocks[slug]
	if !ok {
		m = &sync.Mutex{}
		s.dataLocks[slug] = m
	}
	return m
}

// acquireDataLock blocks until the per-slug data write lock is held. The
// returned func releases it; pair with `defer release()` at the call site.
func (s *Server) acquireDataLock(slug string) (release func()) {
	m := s.dataLockFor(slug)
	m.Lock()
	return m.Unlock
}

// redeployApp stops the current pool and restarts it at the replica count stored in the DB.
// It is called asynchronously (go s.redeployApp(slug)) when the replica count changes while
// the app is running. On failure the app status is set to "degraded".
func (s *Server) redeployApp(slug string) {
	// Drop the reference the PATCH handler added before launching this
	// goroutine, on every return path. Each launched goroutine holds exactly
	// one reference, so the marker stays set until the last redeploy for this
	// slug finishes - never wedged, never cleared early.
	defer s.clearRedeployInFlight(slug)

	// Block for the per-slug deploy lock instead of skipping when it is held.
	// A replica change MUST be applied even when an unrelated operation (upload
	// deploy, restart, rollback, stop, delete) is holding the lock: skipping
	// would drop the change while the DB and the readiness signal both claim it
	// is done, leaving the pool stuck at the old replica count. Waiting also
	// keeps the in-flight marker honest - it stays set until the redeploy
	// actually runs, so `apps set --replicas --wait` polls until the new pool
	// is up rather than returning against the old one.
	release := s.acquireDeployLock(slug)
	defer release()

	app, err := s.store.GetAppBySlug(slug)
	if err != nil {
		slog.Error("redeployApp: get app", "slug", slug, "err", err)
		return
	}
	if err := s.guardActivationLifecycle(app.ID, "redeploy "+slug); err != nil {
		slog.Info("redeployApp: deferred for scheduled data activation", "slug", slug, "err", err)
		return
	}
	if err := s.guardCompatibilityQuarantine(app.ID, "redeploy "+slug); err != nil {
		slog.Warn("redeployApp: compatibility quarantine prevents consumer boot", "slug", slug, "err", err)
		return
	}

	// A concurrent stop, hibernate, or delete may have changed the app's intent
	// while this goroutine waited for the lock. Only cycle the pool for an app
	// that is still running (or degraded, where a previous redeploy failed and a
	// retry is wanted); honour a terminal state instead of resurrecting a pool
	// the operator just tore down.
	if app.Status != "running" && app.Status != "degraded" {
		slog.Info("redeployApp: app no longer running, skipping pool cycle", "slug", slug, "status", app.Status)
		return
	}

	deployments, err := s.store.ListDeployments(app.ID)
	if err != nil || len(deployments) == 0 {
		slog.Warn("redeployApp: no deployments", "slug", slug)
		return
	}
	current := deployments[0]

	if err := s.checkColocatedShared(app.ID, s.tiersForApp(app)); err != nil {
		slog.Error("redeploy: cross-node shared mount rejected", "slug", slug, "err", err)
		return
	}

	if s.manager != nil {
		_ = s.manager.Stop(slug)
	}
	if s.proxy != nil {
		s.proxy.Deregister(slug)
	}

	redeployDefaultMem, redeployDefaultCPU := s.cfg.Runtime.DefaultResourcesForApp(app)
	releaseConsumerBoot, gateErr := s.acquireConsumerBootGate(app.ID)
	if gateErr != nil {
		slog.Error("redeploy: acquire startup-data compatibility fence", "slug", slug, "err", gateErr)
		_ = s.store.UpdateAppStatus(db.UpdateAppStatusParams{Slug: slug, Status: "degraded"})
		return
	}
	defer releaseConsumerBoot()
	redeployParams := s.withTierPlacement(deploy.Params{
		Slug:                  slug,
		BundleDir:             current.BundleDir,
		Replicas:              app.Replicas,
		Manager:               s.manager,
		Proxy:                 s.proxy,
		MemoryLimitMB:         deploy.ResolveMemoryLimitMB(app.MemoryLimitMB, redeployDefaultMem),
		CPUQuotaPercent:       deploy.ResolveCPUQuotaPercent(app.CPUQuotaPercent, redeployDefaultCPU),
		MaxSessionsPerReplica: deploy.ResolveMaxSessionsPerReplica(app.MaxSessionsPerReplica, s.cfg.Runtime.DefaultMaxSessionsPerReplica),
		IdentityHeaders:       deploy.ResolveIdentityHeaders(app.IdentityHeaders, s.cfg.Auth.IdentityHeadersEnabled()),
		ContentDigest:         current.ContentDigest,
		DeploymentID:          current.ID,
		AppVersion:            current.Version,
		// A scaling or resource change re-activates the deployment that is
		// already live: same bundle, same env, nothing the build or the hooks
		// read has changed. Re-running app-controlled hooks to apply a replica
		// count would be a side effect nobody asked for.
		//
		// The env-change restart in env.go deliberately does NOT do this. There
		// the app's env vars are what changed, and hooks receive that env (as do
		// dependency builds, via private-index credentials), so its inputs really
		// are different and re-preparing is the correct behaviour.
		Preparation: activationPreparation(current.Prepared),
	}, app)
	redeployParams = s.guardDeploymentConsumerStart(app, current, redeployParams)
	result, err := s.deployRun(redeployParams)
	if err != nil {
		slog.Error("redeployApp: deploy failed", "slug", slug, "err", err)
		if err := s.store.UpdateAppStatus(db.UpdateAppStatusParams{Slug: slug, Status: "degraded"}); err != nil {
			slog.Error("redeployApp: update status", "slug", slug, "err", err)
		}
		return
	}

	var replicaPersistenceErr error
	for _, r := range result.Replicas {
		pid, port := r.PID, r.Port
		depID := current.ID
		if err := s.store.UpsertReplica(db.UpsertReplicaParams{
			AppID:               app.ID,
			Index:               r.Index,
			PID:                 &pid,
			Port:                &port,
			Status:              "running",
			Provider:            r.Provider,
			Tier:                r.Tier,
			EndpointURL:         r.EndpointURL,
			WorkerID:            r.WorkerID,
			AppVersion:          current.Version,
			DesiredState:        "running",
			DeploymentID:        &depID,
			StartupPeakRSSBytes: r.StartupPeakRSSBytes,
			ConsumerBooted:      true,
		}); err != nil {
			slog.Error("redeployApp: upsert replica", "slug", slug, "index", r.Index, "err", err)
			replicaPersistenceErr = errors.Join(replicaPersistenceErr, err)
		}
	}
	if replicaPersistenceErr != nil {
		_ = s.manager.Stop(slug)
		if s.proxy != nil {
			s.proxy.Deregister(slug)
		}
		_ = s.store.UpdateAppStatus(db.UpdateAppStatusParams{Slug: slug, Status: "degraded"})
		slog.Error("redeployApp: consumer provenance persistence failed; pool stopped", "slug", slug, "err", replicaPersistenceErr)
		return
	}
	for _, idx := range result.Failed {
		if err := s.store.UpsertReplica(db.UpsertReplicaParams{
			AppID:  app.ID,
			Index:  idx,
			Status: "crashed",
		}); err != nil {
			slog.Error("redeployApp: upsert failed replica", "slug", slug, "index", idx, "err", err)
		}
	}
	if err := s.store.UpdateAppStatus(db.UpdateAppStatusParams{Slug: slug, Status: "running"}); err != nil {
		slog.Error("redeployApp: update status", "slug", slug, "err", err)
	}
}
