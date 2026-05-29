package api

import (
	"log/slog"
	"sync"

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
func (s *Server) acquireDeployLock(slug string) (release func()) {
	m := s.deployLockFor(slug)
	m.Lock()
	return m.Unlock
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

	redeployDefaultMem, redeployDefaultCPU := s.cfg.Runtime.DefaultResourcesForTier(s.cfg.Runtime.DefaultTierName())
	result, err := s.deployRun(s.withTierPlacement(deploy.Params{
		Slug:                  slug,
		BundleDir:             current.BundleDir,
		Replicas:              app.Replicas,
		Manager:               s.manager,
		Proxy:                 s.proxy,
		MemoryLimitMB:         deploy.ResolveMemoryLimitMB(app.MemoryLimitMB, redeployDefaultMem),
		CPUQuotaPercent:       deploy.ResolveCPUQuotaPercent(app.CPUQuotaPercent, redeployDefaultCPU),
		MaxSessionsPerReplica: deploy.ResolveMaxSessionsPerReplica(app.MaxSessionsPerReplica, s.cfg.Runtime.DefaultMaxSessionsPerReplica),
		ContentDigest:         current.ContentDigest,
		DeploymentID:          current.ID,
		AppVersion:            current.Version,
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
