package api

import (
	"log/slog"

	"github.com/rvben/shinyhub/internal/db"
	"github.com/rvben/shinyhub/internal/deploy"
)

// tryAcquireRedeploy reports whether the caller has acquired the exclusive
// redeploy lock for slug. Returns false if a redeploy is already in flight
// for that slug, in which case the caller should skip its work.
func (s *Server) tryAcquireRedeploy(slug string) bool {
	s.redeployMu.Lock()
	defer s.redeployMu.Unlock()
	if s.redeployInFlight[slug] {
		return false
	}
	if s.redeployInFlight == nil {
		s.redeployInFlight = make(map[string]bool)
	}
	s.redeployInFlight[slug] = true
	return true
}

// releaseRedeploy releases the exclusive redeploy lock for slug.
func (s *Server) releaseRedeploy(slug string) {
	s.redeployMu.Lock()
	defer s.redeployMu.Unlock()
	delete(s.redeployInFlight, slug)
}

// redeployApp stops the current pool and restarts it at the replica count stored in the DB.
// It is called asynchronously (go s.redeployApp(slug)) when the replica count changes while
// the app is running. On failure the app status is set to "degraded".
func (s *Server) redeployApp(slug string) {
	if !s.tryAcquireRedeploy(slug) {
		slog.Info("redeploy already in flight, skipping", "slug", slug)
		return
	}
	defer s.releaseRedeploy(slug)

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
