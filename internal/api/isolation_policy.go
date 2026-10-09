package api

import (
	"fmt"

	"github.com/rvben/shinyhub/internal/config"
	"github.com/rvben/shinyhub/internal/db"
	"github.com/rvben/shinyhub/internal/deploy"
	"github.com/rvben/shinyhub/internal/lifecycle"
)

func (s *Server) servingIsolation(app *db.App) (string, error) {
	if s.store == nil {
		return "", fmt.Errorf("serving worker policy store is unavailable")
	}
	return s.store.ServingWorkerIsolation(app, s.cfg.Runtime.DefaultWorkerIsolation)
}

func (s *Server) servingRuntimeApp(app *db.App) (*db.App, error) {
	mode, err := s.servingIsolation(app)
	if err != nil {
		return nil, err
	}
	serving := *app
	serving.WorkerIsolation = mode
	return &serving, nil
}

// recordLaunchIsolation is called before staging or boot, while the app
// operation is fenced. A pending deployment owns a new immutable policy;
// an existing deployment can change policy only at a verified cold start.
func (s *Server) recordLaunchIsolation(app *db.App, deploymentID int64, desired string) error {
	if deploymentID <= 0 {
		return fmt.Errorf("deployment identity is required before worker startup")
	}
	recorded, err := s.store.GetDeploymentWorkerIsolation(deploymentID)
	if err != nil {
		return err
	}
	if recorded == "" || recorded == desired {
		return s.store.RecordDeploymentWorkerIsolation(app.ID, deploymentID, desired)
	}
	// Replacement of an existing version is a separate cold lifecycle action.
	mode, err := lifecycle.PrepareColdWorkerIsolation(s.store, s.manager, s.proxy, app, s.cfg.Runtime.DefaultWorkerIsolation)
	if err != nil {
		return err
	}
	if mode != desired {
		return fmt.Errorf("recorded worker isolation %q differs from launch mode %q", mode, desired)
	}
	return nil
}

func (s *Server) guardGenerationDrain(slug string) error {
	if s.proxy != nil && s.proxy.HasDrainingGeneration(slug) {
		return fmt.Errorf("previous generation is still draining; retry after its retirement completes")
	}
	return nil
}

// CheckElasticLaunchCapacity is called under the host launch reservation by
// demand/warm-spare starts while a previous generation remains live.
func (s *Server) CheckElasticLaunchCapacity(app *db.App) error {
	projected := *app
	projected.Replicas = 1
	projected.WorkerWarmSpares = 0
	return s.generationHandoffCapacityCheck(&projected)
}

func groupedCandidateWorkers(app *db.App) int {
	workers := 1 + max(0, app.WorkerWarmSpares)
	if app.WorkerMaxWorkers > 0 {
		workers = min(workers, app.WorkerMaxWorkers)
	}
	return max(1, workers)
}

func (s *Server) generationSlotFloor(app *db.App) (int, error) {
	floor := max(app.Replicas, s.manager.NextReplicaIndex(app.Slug))
	rows, err := s.store.ListDeploymentReplicas(app.ID)
	if err != nil {
		return 0, err
	}
	for _, row := range rows {
		floor = max(floor, row.Index+1)
	}
	return floor, nil
}

func supportedHandoffIsolation(mode string) bool {
	return mode == "grouped" || mode == "multiplex"
}

func (s *Server) recordServingIsolation(app *db.App, deployment *db.Deployment) error {
	mode, err := s.servingIsolation(app)
	if err != nil {
		return err
	}
	if s.proxy != nil {
		if actual, ok := s.proxy.ServingMode(app.Slug); ok && string(actual) != mode {
			return fmt.Errorf("serving route isolation %q differs from recorded policy %q", actual, mode)
		}
		if err := s.proxy.EnsureServingGeneration(app.Slug, deployment.ID, config.WorkerIsolationMode(mode)); err != nil {
			return err
		}
	}
	return s.store.RecordDeploymentWorkerIsolation(app.ID, deployment.ID, deploy.ResolveWorkerIsolation(mode, s.cfg.Runtime.DefaultWorkerIsolation))
}
