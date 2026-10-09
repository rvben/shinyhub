package api

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/rvben/shinyhub/internal/db"
	"github.com/rvben/shinyhub/internal/deploy"
	"github.com/rvben/shinyhub/internal/process"
)

func (s *Server) replicaPlacementTier(row *db.Replica) string {
	if row.Tier == "" {
		return s.cfg.Runtime.DefaultTierName()
	}
	// Legacy single-tier rows used "default" before tiers were configurable.
	if row.Tier == "default" {
		if _, known := s.cfg.Runtime.RuntimeForTier("default"); !known {
			return s.cfg.Runtime.DefaultTierName()
		}
	}
	return row.Tier
}

func (s *Server) placementDiffers(rows []*db.Replica) bool {
	for _, row := range rows {
		if s.replicaPlacementTier(row) != s.cfg.Runtime.DefaultTierName() {
			return true
		}
	}
	return false
}

func (s *Server) replicaRowsMatchTarget(app *db.App, rows []*db.Replica) bool {
	if len(rows) != app.Replicas || replicaRowSize(rows) != app.Replicas {
		return false
	}
	assignments, err := process.ExpandPlacement(app.PlacementMap(), s.cfg.Runtime.TierOrder(), app.Replicas, s.cfg.Runtime.DefaultTierName())
	if err != nil {
		return false
	}
	for _, row := range rows {
		if row.Index < 0 || row.Index >= len(assignments) || s.replicaPlacementTier(row) != assignments[row.Index].Tier {
			return false
		}
	}
	return true
}

// The runtime identity includes the global slot index. Preserve identical
// index/tier pairs; moving an index requires draining that slot, not relabeling
// a running process. Provision additional capacity before moving old slots.
func (s *Server) reconcilePlacementLocked(app *db.App, rows []*db.Replica) error {
	assignments, err := process.ExpandPlacement(app.PlacementMap(), s.cfg.Runtime.TierOrder(), app.Replicas, s.cfg.Runtime.DefaultTierName())
	if err != nil {
		return err
	}
	if err := s.checkColocatedShared(app.ID, s.tiersForApp(app)); err != nil {
		return err
	}
	current, err := s.store.GetServingDeployment(app.ID)
	if err != nil {
		return err
	}
	existing := make(map[int]*db.Replica, len(rows))
	for _, row := range rows {
		existing[row.Index] = row
	}
	size := max(replicaRowSize(rows), len(assignments))
	if s.proxy != nil {
		s.proxy.SetPoolSize(app.Slug, size)
	}
	for _, a := range assignments {
		if existing[a.Index] == nil {
			if err := s.startPlacementReplica(app, current, a.Index, size); err != nil {
				return err
			}
		}
	}
	for _, a := range assignments {
		row := existing[a.Index]
		if row == nil {
			continue
		}
		tier := s.replicaPlacementTier(row)
		if tier == a.Tier {
			continue
		}
		if err := s.removePlacementReplica(app, a.Index); err != nil {
			return err
		}
		if err := s.startPlacementReplica(app, current, a.Index, size); err != nil {
			return err
		}
	}
	for i := size - 1; i >= len(assignments); i-- {
		if existing[i] != nil {
			if err := s.removePlacementReplica(app, i); err != nil {
				return err
			}
		}
	}
	if s.proxy != nil {
		s.proxy.SetPoolSize(app.Slug, len(assignments))
	}
	rows, err = s.store.ListReplicas(app.ID)
	if err != nil {
		return err
	}
	for _, row := range rows {
		if row.Status != db.ReplicaStatusRunning && !(row.DesiredState == db.ReplicaDesiredWarm && (row.Status == "stopped" || row.Status == "suspended")) {
			return nil
		}
	}
	if app.Status == "degraded" {
		return s.store.UpdateAppStatus(db.UpdateAppStatusParams{Slug: app.Slug, Status: "running"})
	}
	return nil
}

func (s *Server) removePlacementReplica(app *db.App, index int) error {
	var endpoint string
	if s.proxy != nil {
		endpoint = s.proxy.ReplicaTargetURL(app.Slug, index)
		s.proxy.DrainReplica(app.Slug, index)
	}
	if err := s.store.SetReplicaDesiredState(app.ID, index, "draining"); err != nil {
		if s.proxy != nil {
			s.proxy.UndrainReplica(app.Slug, index)
		}
		return err
	}
	grace := s.cfg.Server.DrainTimeout
	if grace <= 0 {
		grace = 60 * time.Second
	}
	if s.proxy != nil {
		s.waitForDrain(app.Slug, index, grace, s.clusteredFleetWait(app.ID, index))
	}
	if s.manager != nil {
		if err := s.manager.StopReplicaConfirmed(app.Slug, index); err != nil && !errors.Is(err, process.ErrReplicaNotFound) {
			if s.proxy != nil {
				s.proxy.UndrainReplica(app.Slug, index)
			}
			_ = s.store.SetReplicaDesiredState(app.ID, index, "running")
			return err
		}
	}
	if s.proxy != nil {
		s.proxy.DeregisterReplicaIfTarget(app.Slug, index, endpoint)
	}
	return s.store.DeleteReplica(app.ID, index)
}

func (s *Server) startPlacementReplica(app *db.App, current *db.Deployment, index, poolSize int) error {
	mem, cpu := s.cfg.Runtime.DefaultResourcesForApp(app)
	p := s.withTierPlacement(deploy.Params{
		Slug: app.Slug, BundleDir: current.BundleDir, Replicas: poolSize,
		Manager: s.manager, Proxy: s.proxy,
		MemoryLimitMB:         deploy.ResolveMemoryLimitMB(app.MemoryLimitMB, mem),
		CPUQuotaPercent:       deploy.ResolveCPUQuotaPercent(app.CPUQuotaPercent, cpu),
		MaxSessionsPerReplica: deploy.ResolveMaxSessionsPerReplica(app.MaxSessionsPerReplica, s.cfg.Runtime.DefaultMaxSessionsPerReplica),
		IdentityHeaders:       deploy.ResolveIdentityHeaders(app.IdentityHeaders, s.cfg.Auth.IdentityHeadersEnabled()),
		ContentDigest:         current.ContentDigest, DeploymentID: current.ID, AppVersion: current.Version,
	}, app)
	p = s.guardDeploymentConsumerStart(app, current, p)
	release, err := s.acquireConsumerBootGate(app.ID)
	if err != nil {
		return err
	}
	defer release()
	p = s.traceDeploy(context.Background(), p)
	r, err := s.deployReplica(p, index)
	if err != nil {
		return fmt.Errorf("resize %s: boot replica %d: %w", app.Slug, index, err)
	}
	pid, port, depID := r.PID, r.Port, current.ID
	err = s.store.UpsertReplica(db.UpsertReplicaParams{
		AppID: app.ID, Index: index, PID: &pid, Port: &port, Status: "running",
		Provider: r.Provider, Tier: r.Tier, EndpointURL: r.EndpointURL, WorkerID: r.WorkerID,
		AppVersion: current.Version, DesiredState: "running", DeploymentID: &depID,
		StartupPeakRSSBytes: r.StartupPeakRSSBytes, ConsumerBooted: true,
	})
	if err != nil {
		if s.manager != nil {
			_ = s.manager.StopReplicaConfirmed(app.Slug, index)
		}
		if s.proxy != nil {
			s.proxy.DeregisterReplicaIfTarget(app.Slug, index, r.EndpointURL)
		}
	}
	return err
}
