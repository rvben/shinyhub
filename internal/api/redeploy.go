package api

import (
	"log/slog"
	"sync"
	"time"

	"github.com/rvben/shinyhub/internal/db"
	"github.com/rvben/shinyhub/internal/deploy"
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
	m := s.deployLockFor(slug)
	m.Lock()
	s.deployLocksMu.Lock()
	if s.deployInFlight == nil {
		s.deployInFlight = make(map[string]struct{})
	}
	s.deployInFlight[slug] = struct{}{}
	s.deployLocksMu.Unlock()
	return func() {
		s.deployLocksMu.Lock()
		delete(s.deployInFlight, slug)
		s.deployLocksMu.Unlock()
		m.Unlock()
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
	app.SessionsCeiling = configuredSessionsCeiling(app)
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

// decorateAppObservation overlays process reality onto the stored lifecycle
// state. desired_status preserves the database value; status becomes the field
// operators expect: whether the app is actually serving, idle, or unhealthy.
func (s *Server) decorateAppObservation(app *db.App, replicas []*db.Replica, elasticKnown bool, workersRunning, workersTotal int) {
	if app.DesiredStatus == "" {
		app.DesiredStatus = app.Status
	}
	app.ReplicasRunning = 0
	app.WorkersRunning = workersRunning
	app.LastReplicaError = ""

	if elasticKnown {
		// Elastic pools are demand-driven and have no durable replica rows. Their
		// empty healthy state is idle, while transitional workers are starting.
		switch {
		case workersRunning > 0:
			app.Status = "running"
		case workersTotal > 0:
			app.Status = "starting"
		case app.DesiredStatus == "running" || app.DesiredStatus == "degraded":
			app.Status = "idle"
		default:
			app.Status = app.DesiredStatus
		}
		return
	}

	crashed, lost, starting, intentionallyParked := 0, 0, 0, 0
	var lastReplicaErrorAt time.Time
	for _, rep := range replicas {
		if rep == nil {
			continue
		}
		// Exit diagnostics deliberately survive a successful restart. Keep the
		// newest one visible at app level even after this slot is serving again.
		if rep.Reason != "" && (app.LastReplicaError == "" || rep.UpdatedAt.After(lastReplicaErrorAt)) {
			app.LastReplicaError = rep.Reason
			lastReplicaErrorAt = rep.UpdatedAt
		}
		switch rep.Status {
		case db.ReplicaStatusRunning:
			app.ReplicasRunning++
		case "crashed":
			if rep.DesiredState == "" || rep.DesiredState == "running" {
				crashed++
			}
		case db.ReplicaStatusLost:
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
		app.Status = "degraded"
	case app.ReplicasRunning > 0:
		app.Status = "running"
	case crashed > 0:
		app.Status = "crashed"
	case lost > 0:
		app.Status = "degraded"
	case starting > 0:
		app.Status = "starting"
	case intentionallyParked > 0 && app.DesiredStatus != "stopped":
		app.Status = "hibernated"
	case len(replicas) == 0 && (app.DesiredStatus == "running" || app.DesiredStatus == "degraded"):
		app.Status = "degraded"
	default:
		app.Status = app.DesiredStatus
	}
	if app.LastReplicaError == "" && app.Status == "crashed" {
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
	result, err := s.deployRun(s.withTierPlacement(deploy.Params{
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
	}, app))
	if err != nil {
		slog.Error("redeployApp: deploy failed", "slug", slug, "err", err)
		if err := s.store.UpdateAppStatus(db.UpdateAppStatusParams{Slug: slug, Status: "degraded"}); err != nil {
			slog.Error("redeployApp: update status", "slug", slug, "err", err)
		}
		return
	}

	for _, r := range result.Replicas {
		pid, port := r.PID, r.Port
		depID := current.ID
		if err := s.store.UpsertReplica(db.UpsertReplicaParams{
			AppID:        app.ID,
			Index:        r.Index,
			PID:          &pid,
			Port:         &port,
			Status:       "running",
			Provider:     r.Provider,
			Tier:         r.Tier,
			EndpointURL:  r.EndpointURL,
			WorkerID:     r.WorkerID,
			AppVersion:   current.Version,
			DesiredState: "running",
			DeploymentID: &depID,
		}); err != nil {
			slog.Error("redeployApp: upsert replica", "slug", slug, "index", r.Index, "err", err)
		}
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
