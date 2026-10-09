package api

import (
	"errors"
	"fmt"
	"time"

	"github.com/rvben/shinyhub/internal/db"
	"github.com/rvben/shinyhub/internal/process"
)

// stopForSettings gives all sessions one shared drain deadline. Call only with
// the app operation and startup compatibility fences held. A failed stop keeps
// the pool tracked and restores routing instead of orphaning live processes.
func (s *Server) stopForSettings(app *db.App) error {
	rows, err := s.store.ListReplicas(app.ID)
	if err != nil {
		return err
	}
	managedBefore := make(map[int]bool)
	var elasticIndices []int
	if s.proxy != nil {
		if pool, ok := s.proxy.ElasticWorkersSnapshot(app.Slug); ok {
			for _, worker := range pool.Workers {
				elasticIndices = append(elasticIndices, worker.SlotID)
				if s.manager != nil {
					_, managedBefore[worker.SlotID] = s.manager.GetReplica(app.Slug, worker.SlotID)
				}
			}
		}
	}
	if s.manager != nil {
		for _, row := range rows {
			_, managedBefore[row.Index] = s.manager.GetReplica(app.Slug, row.Index)
		}
	}
	undo := func() {}
	if s.proxy != nil {
		undo = s.proxy.BeginSettingsDrain(app.Slug)
	}
	restore := func() {
		undo()
		for _, row := range rows {
			_ = s.store.SetReplicaDesiredState(app.ID, row.Index, row.DesiredState)
		}
	}
	for _, row := range rows {
		if err := s.store.SetReplicaDesiredState(app.ID, row.Index, "draining"); err != nil {
			restore()
			return err
		}
	}
	grace := s.cfg.Server.DrainTimeout
	if grace <= 0 {
		grace = 60 * time.Second
	}
	deadline := time.Now().Add(grace)
	for s.proxy != nil {
		drained := s.proxy.SettingsDrainComplete(app.Slug)
		for _, row := range rows {
			if fleet := s.clusteredFleetWait(app.ID, row.Index); fleet != nil && !fleet() {
				drained = false
			}
		}
		if drained || !time.Now().Before(deadline) {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if s.manager != nil {
		if err := s.manager.StopConfirmed(app.Slug); err != nil && !errors.Is(err, process.ErrReplicaNotFound) {
			// Some slots may already have stopped. Remove those routes before
			// restoring admission on survivors; never advertise a dead slot as
			// running merely because another runtime refused its stop.
			for _, row := range rows {
				if info, ok := s.manager.GetReplica(app.Slug, row.Index); managedBefore[row.Index] && (!ok || info.Status == process.StatusStopped || info.Status == process.StatusCrashed) {
					if s.proxy != nil {
						s.proxy.DeregisterReplicaIfTarget(app.Slug, row.Index, s.proxy.ReplicaTargetURL(app.Slug, row.Index))
					}
					_ = s.store.UpsertReplica(db.UpsertReplicaParams{AppID: app.ID, Index: row.Index, Status: "stopped", DesiredState: "stopped", Tier: row.Tier, Provider: row.Provider, AppVersion: row.AppVersion, DeploymentID: row.DeploymentID})
					row.DesiredState = "stopped"
				}
			}
			for _, index := range elasticIndices {
				if info, ok := s.manager.GetReplica(app.Slug, index); managedBefore[index] && (!ok || info.Status == process.StatusStopped || info.Status == process.StatusCrashed) {
					s.proxy.DeregisterElasticWorker(app.Slug, index)
				}
			}
			restore()
			_ = s.store.UpdateAppStatus(db.UpdateAppStatusParams{Slug: app.Slug, Status: "degraded", LastError: "stop for settings: " + err.Error()})
			return fmt.Errorf("stop for settings: %w", err)
		}
	}
	if s.proxy != nil {
		s.proxy.Deregister(app.Slug)
	}
	// A structural restart may reuse the deployment ID. Its next cold start
	// must not inherit stale durable identities from the confirmed old pool.
	for _, row := range rows {
		if err := s.store.ClearReplicaRuntimeIdentity(app.ID, row.Index); err != nil {
			return fmt.Errorf("clear stopped replica identity: %w", err)
		}
	}
	generationRows, err := s.store.ListDeploymentReplicas(app.ID)
	if err != nil {
		return err
	}
	seen := make(map[int64]bool)
	for _, row := range generationRows {
		seen[row.DeploymentID] = true
	}
	for id := range seen {
		if err := s.stopGenerationForCleanup(app.Slug, id); err != nil {
			return err
		}
		if err := s.store.DeleteDeploymentReplicas(id); err != nil {
			return err
		}
	}
	return nil
}
