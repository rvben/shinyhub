package api

import (
	"errors"
	"fmt"
	"reflect"
	"syscall"

	"github.com/rvben/shinyhub/internal/db"
	"github.com/rvben/shinyhub/internal/deploy"
	"github.com/rvben/shinyhub/internal/process"
)

// Grouped workers may already have been stopped by their disconnect/lifetime
// callback. A missing manager entry alone is not proof of termination; confirm
// every durable native PID and process group before deleting the ledger.
func (s *Server) stopGenerationForCleanup(slug string, deploymentID int64) error {
	err := s.manager.StopGeneration(slug, deploymentID)
	if err != nil && !errors.Is(err, process.ErrReplicaNotFound) {
		return err
	}
	app, loadErr := s.store.GetAppBySlug(slug)
	if loadErr != nil {
		return errors.Join(err, loadErr)
	}
	if deploy.ResolveWorkerIsolation(app.WorkerIsolation, s.cfg.Runtime.DefaultWorkerIsolation) != "grouped" {
		return err
	}
	rows, readErr := s.store.ListDeploymentReplicas(app.ID)
	if readErr != nil {
		return readErr
	}
	for _, row := range rows {
		if row.DeploymentID != deploymentID {
			continue
		}
		if row.Provider != "" && row.Provider != "native" {
			return fmt.Errorf("cannot confirm grouped provider %q stopped", row.Provider)
		}
		if row.PID != nil && *row.PID > 0 {
			if !errors.Is(syscall.Kill(*row.PID, 0), syscall.ESRCH) || !errors.Is(syscall.Kill(-*row.PID, 0), syscall.ESRCH) {
				return fmt.Errorf("grouped worker %d termination is unconfirmed", row.Index)
			}
		}
	}
	return nil
}

func hasBlockingGenerationRows(rows []*db.DeploymentReplica, activeID int64, grouped bool) bool {
	for _, row := range rows {
		if !grouped || row.DeploymentID != activeID {
			return true
		}
	}
	return false
}

func (s *Server) groupedHandoffRuntimeReason(app *db.App) string {
	if len(app.PlacementMap()) > 0 {
		return "grouped parallel handoff requires the default native tier; explicit placement requires a stop-first deploy"
	}
	runtimeMode, ok := s.cfg.Runtime.RuntimeForTier(s.cfg.Runtime.DefaultTierName())
	if !ok {
		runtimeMode = s.cfg.Runtime.Mode
	}
	if runtimeMode != "" && runtimeMode != "native" {
		return "grouped parallel handoff supports native workers only"
	}
	for _, worker := range s.manager.AllForSlug(app.Slug) {
		if worker == nil || worker.Status == process.StatusStopped || worker.Status == process.StatusCrashed {
			continue
		}
		if worker.Provider != "" && worker.Provider != "native" {
			return fmt.Sprintf("grouped worker %d uses provider %q; parallel handoff supports native workers only", worker.Index, worker.Provider)
		}
	}
	return ""
}

func (s *Server) persistGroupedGeneration(app *db.App, deployment *db.Deployment) error {
	for _, worker := range s.manager.AllForSlug(app.Slug) {
		if worker == nil || worker.DeploymentID != deployment.ID {
			continue
		}
		pid, port := worker.PID, worker.Port
		if err := s.store.UpsertDeploymentReplica(db.UpsertDeploymentReplicaParams{
			AppID: app.ID, DeploymentID: deployment.ID, Index: worker.Index,
			PID: &pid, Port: &port, Status: string(worker.Status), Provider: worker.Provider,
			Tier: worker.Tier, EndpointURL: worker.EndpointURL, WorkerID: worker.WorkerID,
		}); err != nil {
			return fmt.Errorf("record grouped worker %d: %w", worker.Index, err)
		}
	}
	return nil
}

func (s *Server) activateDeployManagerGeneration(app *db.App, candidateID, previousID int64, result *deploy.PoolResult, grouped bool) (int64, error) {
	if !grouped {
		return s.manager.ActivateGeneration(app.Slug, candidateID)
	}
	// Grouped workers share a monotonic slot space. There is no manager pointer
	// to switch: proxy publication alone changes which workers accept clients.
	if len(result.Replicas) != 1 {
		return 0, fmt.Errorf("grouped candidate requires one ready worker")
	}
	worker, ok := s.manager.GetGenerationReplica(app.Slug, candidateID, result.Replicas[0].Index)
	if !ok || worker.Status != process.StatusRunning {
		return 0, fmt.Errorf("grouped candidate is no longer running")
	}
	if worker.Provider != "" && worker.Provider != "native" {
		return 0, fmt.Errorf("grouped candidate uses unsupported provider %q", worker.Provider)
	}
	return previousID, nil
}

func unchangedOptional[T comparable](desired *T, current T) bool {
	return desired == nil || *desired == current
}

func (s *Server) selectDeployManagerGeneration(slug string, deploymentID int64, grouped bool) error {
	if grouped {
		// Grouped slots never changed manager pools.
		return nil
	}
	return s.manager.SelectGeneration(slug, deploymentID)
}

// A code-only grouped update can carry the same manifest. Check both the
// declaration and live settings: comparing files alone would miss overrides
// changed since the last deploy. Hooks may mutate shared state and must never
// run beside an old generation, even when their declaration is unchanged.
func (s *Server) groupedManifestHandoffSafe(app *db.App, previous *db.Deployment, manifest *deploy.Manifest) bool {
	if previous == nil || len(manifest.Hooks) != 0 {
		return false
	}
	old, err := deploy.LoadManifest(previous.BundleDir)
	if err != nil || !reflect.DeepEqual(old, manifest) {
		return false
	}
	m := manifest.App
	if !reflect.DeepEqual(m.IdentityHeaders, app.IdentityHeaders) || !reflect.DeepEqual(m.UsageIdentityMode, app.UsageIdentityMode) {
		return false
	}
	if m.HibernateResetToDefault && app.HibernateTimeoutMinutes != nil {
		return false
	}
	for _, pair := range [][2]*int{{m.HibernateTimeoutMinutes, app.HibernateTimeoutMinutes}, {m.MemoryLimitMB, app.MemoryLimitMB}, {m.CPUQuotaPercent, app.CPUQuotaPercent}} {
		if pair[0] != nil && !reflect.DeepEqual(pair[0], pair[1]) {
			return false
		}
	}
	if !unchangedOptional(m.Replicas, app.Replicas) || !unchangedOptional(m.MaxSessionsPerReplica, app.MaxSessionsPerReplica) ||
		!unchangedOptional(m.MinWarmReplicas, app.MinWarmReplicas) || !unchangedOptional(m.RenderSeconds, app.RenderSeconds) ||
		!unchangedOptional(m.Name, app.Name) || !unchangedOptional(m.Description, app.Description) ||
		!unchangedOptional(m.Icon, app.IconEmoji) || !unchangedOptional(m.Project, app.ProjectSlug) {
		return false
	}
	if a := m.Autoscale; a != nil && (!unchangedOptional(a.Enabled, app.AutoscaleEnabled) || a.MinReplicas != app.AutoscaleMinReplicas || a.MaxReplicas != app.AutoscaleMaxReplicas || a.Target != app.AutoscaleTarget) {
		return false
	}
	if w := m.Worker; w != nil {
		if !unchangedOptional(w.Isolation, deploy.ResolveWorkerIsolation(app.WorkerIsolation, s.cfg.Runtime.DefaultWorkerIsolation)) ||
			!unchangedOptional(w.GroupedSize, app.WorkerGroupedSize) || !unchangedOptional(w.MaxWorkers, app.WorkerMaxWorkers) ||
			!unchangedOptional(w.WarmSpares, app.WorkerWarmSpares) || !unchangedOptional(w.MaxSessionLifetimeSecs, app.WorkerMaxSessionLifetimeSecs) {
			return false
		}
	}
	return true
}
