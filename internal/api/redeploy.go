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

// tryAcquireDeployLock is the non-blocking variant. It returns nil if the
// lock is currently held by another goroutine, or the release func when
// acquired. Use this from coalescing code paths (e.g. the async redeploy
// goroutine) where "another deploy is already running, skip this one" is the
// correct behavior.
func (s *Server) tryAcquireDeployLock(slug string) (release func()) {
	m := s.deployLockFor(slug)
	if !m.TryLock() {
		return nil
	}
	return m.Unlock
}

// redeployApp stops the current pool and restarts it at the replica count stored in the DB.
// It is called asynchronously (go s.redeployApp(slug)) when the replica count changes while
// the app is running. On failure the app status is set to "degraded".
func (s *Server) redeployApp(slug string) {
	release := s.tryAcquireDeployLock(slug)
	if release == nil {
		slog.Info("redeploy already in flight, skipping", "slug", slug)
		return
	}
	defer release()

	app, err := s.store.GetAppBySlug(slug)
	if err != nil {
		slog.Error("redeployApp: get app", "slug", slug, "err", err)
		return
	}

	deployments, err := s.store.ListDeployments(app.ID)
	if err != nil || len(deployments) == 0 {
		slog.Warn("redeployApp: no deployments", "slug", slug)
		return
	}
	current := deployments[0]

	if s.manager != nil {
		_ = s.manager.Stop(slug)
	}
	if s.proxy != nil {
		s.proxy.Deregister(slug)
	}

	result, err := deploy.Run(deploy.Params{
		Slug:            slug,
		BundleDir:       current.BundleDir,
		Replicas:        app.Replicas,
		Manager:         s.manager,
		Proxy:           s.proxy,
		MemoryLimitMB:   deploy.ResolveMemoryLimitMB(app.MemoryLimitMB, s.cfg.Runtime.Docker.DefaultMemoryMB),
		CPUQuotaPercent: deploy.ResolveCPUQuotaPercent(app.CPUQuotaPercent, s.cfg.Runtime.Docker.DefaultCPUPercent),
	})
	if err != nil {
		slog.Error("redeployApp: deploy failed", "slug", slug, "err", err)
		if err := s.store.UpdateAppStatus(db.UpdateAppStatusParams{Slug: slug, Status: "degraded"}); err != nil {
			slog.Error("redeployApp: update status", "slug", slug, "err", err)
		}
		return
	}

	for _, r := range result.Replicas {
		pid, port := r.PID, r.Port
		if err := s.store.UpsertReplica(db.UpsertReplicaParams{
			AppID:  app.ID,
			Index:  r.Index,
			PID:    &pid,
			Port:   &port,
			Status: "running",
		}); err != nil {
			slog.Error("redeployApp: upsert replica", "slug", slug, "index", r.Index, "err", err)
		}
	}
	if err := s.store.UpdateAppStatus(db.UpdateAppStatusParams{Slug: slug, Status: "running"}); err != nil {
		slog.Error("redeployApp: update status", "slug", slug, "err", err)
	}
}
