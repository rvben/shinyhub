package api

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"reflect"
	"strings"
	"syscall"
	"time"

	"github.com/rvben/shinyhub/internal/db"
	"github.com/rvben/shinyhub/internal/deploy"
	"github.com/rvben/shinyhub/internal/process"
)

// Grouped workers may already have been stopped by their disconnect/lifetime
// callback. A missing manager entry alone is not proof of termination; confirm
// every durable native PID and process group before deleting the ledger.
func (s *Server) stopGenerationForCleanup(slug string, deploymentID int64) (resultErr error) {
	clear := s.proxy.MarkGenerationDrain(slug, deploymentID)
	defer func() {
		if resultErr != nil {
			clear()
		}
	}()
	err := s.manager.StopGeneration(slug, deploymentID)
	if err != nil && !errors.Is(err, process.ErrReplicaNotFound) {
		return err
	}
	app, loadErr := s.store.GetAppBySlug(slug)
	if loadErr != nil {
		return errors.Join(err, loadErr)
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
			return fmt.Errorf("cannot confirm generation provider %q stopped", row.Provider)
		}
		if row.PID != nil && *row.PID > 0 {
			if row.ProcessStartIdentity > 0 {
				started, err := process.NativeProcessStartIdentity(*row.PID)
				if err == nil && started != row.ProcessStartIdentity {
					continue
				}
			}
			if !errors.Is(syscall.Kill(*row.PID, 0), syscall.ESRCH) || !errors.Is(syscall.Kill(-*row.PID, 0), syscall.ESRCH) {
				return fmt.Errorf("generation worker %d termination is unconfirmed", row.Index)
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
	for _, worker := range s.manager.AllGenerationsForSlug(app.Slug) {
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
	for _, worker := range s.manager.AllGenerationsForSlug(app.Slug) {
		if worker == nil || worker.DeploymentID != deployment.ID {
			continue
		}
		pid, port := worker.PID, worker.Port
		var started int64
		if _, native := s.manager.RuntimeForTier(worker.Tier).(*process.NativeRuntime); native && pid > 0 {
			var err error
			started, err = process.NativeProcessStartIdentity(pid)
			if err != nil {
				return fmt.Errorf("record grouped worker identity: %w", err)
			}
		}
		if err := s.store.UpsertDeploymentReplica(db.UpsertDeploymentReplicaParams{
			AppID: app.ID, DeploymentID: deployment.ID, Index: worker.Index,
			PID: &pid, Port: &port, Status: string(worker.Status), Provider: worker.Provider,
			Tier: worker.Tier, EndpointURL: worker.EndpointURL, WorkerID: worker.WorkerID, ProcessStartIdentity: started,
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

// manifestHandoffReasons classifies effective settings, retaining omitted-key
// reset semantics and live-override checks. Newly added fields fail closed.
// Producer dispatch is classified separately from the actual convergence plan.
func (s *Server) manifestHandoffReasons(app *db.App, previous *db.Deployment, manifest *deploy.Manifest) []string {
	if previous == nil {
		return []string{"previous manifest is unavailable"}
	}
	old, err := deploy.LoadManifest(previous.BundleDir)
	if err != nil || old == nil {
		return []string{"previous manifest is unavailable or unreadable"}
	}
	var reasons []string
	if len(manifest.Hooks) != 0 {
		reasons = append(reasons, "hook: hooks may mutate shared state")
	}
	// These settings can be staged for candidate boot and committed at cutover.
	safe := map[string]bool{
		"name": true, "description": true, "icon": true, "project": true,
		"hibernate_timeout_minutes": true, "min_warm_replicas": true,
		"max_sessions_per_replica": true, "autoscale": true,
		"memory_limit_mb": true, "cpu_quota_percent": true, "render_seconds": true,
		"worker": true,
	}
	before, after := reflect.ValueOf(old.App), reflect.ValueOf(manifest.App)
	for i := 0; i < after.NumField(); i++ {
		field := after.Type().Field(i).Tag.Get("toml")
		if safe[field] || after.Type().Field(i).Name == "HibernateResetToDefault" {
			continue
		}
		if !reflect.DeepEqual(before.Field(i).Interface(), after.Field(i).Interface()) {
			reasons = append(reasons, "app."+field+": configuration change is not supported by parallel handoff")
		}
	}
	m := manifest.App
	if !reflect.DeepEqual(m.IdentityHeaders, app.IdentityHeaders) {
		reasons = append(reasons, "app.identity_headers: reconciliation changes live identity forwarding")
	}
	if !reflect.DeepEqual(m.UsageIdentityMode, app.UsageIdentityMode) {
		reasons = append(reasons, "app.usage_identity_mode: reconciliation changes live privacy policy")
	}
	if !unchangedOptional(m.Replicas, app.Replicas) {
		reasons = append(reasons, "app.replicas: reconciliation changes the live pool layout")
	}
	if w := m.Worker; w != nil {
		current, isolationErr := s.servingIsolation(app)
		target := deploy.ResolveWorkerIsolation(app.WorkerIsolation, s.cfg.Runtime.DefaultWorkerIsolation)
		if w.Isolation != nil {
			target = *w.Isolation
		}
		if isolationErr != nil || !supportedHandoffIsolation(current) || !supportedHandoffIsolation(target) {
			reasons = append(reasons, "app.worker: parallel handoff supports grouped and multiplex worker policy only")
		}
	}
	if !reflect.DeepEqual(old.Access, manifest.Access) {
		reasons = append(reasons, "access: access policy change requires stop-first reconciliation")
	}
	rules, err := s.store.ListAppGroupAccess(app.Slug)
	if err != nil {
		reasons = append(reasons, "access: cannot inspect effective group policy")
	} else {
		desired, live := map[string]string{}, map[string]string{}
		for _, g := range manifest.Access.ViewerGroups {
			desired[g] = db.HigherMemberRole(desired[g], "viewer")
		}
		for _, g := range manifest.Access.ManagerGroups {
			desired[g] = db.HigherMemberRole(desired[g], "manager")
		}
		for _, r := range rules {
			if r.Source == "manual" {
				delete(desired, r.Group)
			} else if r.Source == "manifest" {
				live[r.Group] = r.Role
			}
		}
		if !reflect.DeepEqual(desired, live) {
			reasons = append(reasons, "access: reconciliation changes effective manifest group rules")
		}
	}
	return reasons
}

func (s *Server) manifestHandoffSafe(app *db.App, previous *db.Deployment, manifest *deploy.Manifest) bool {
	return len(s.manifestHandoffReasons(app, previous, manifest)) == 0
}

func (s *Server) manifestHandoffReason(app *db.App, previous *db.Deployment, manifest *deploy.Manifest) string {
	return "manifest whose configuration must be reconciled by an explicit stop-first deploy: " + strings.Join(s.manifestHandoffReasons(app, previous, manifest), "; ")
}

// projectHandoffApp overlays only fields admitted above, without publishing
// settings to the serving pool while the candidate is still being prepared.
func projectHandoffApp(app *db.App, m deploy.AppSettings) *db.App {
	projected := *app
	if w := m.Worker; w != nil {
		if w.Isolation != nil {
			projected.WorkerIsolation = *w.Isolation
		}
		if w.GroupedSize != nil {
			projected.WorkerGroupedSize = *w.GroupedSize
		}
		if w.MaxWorkers != nil {
			projected.WorkerMaxWorkers = *w.MaxWorkers
		}
		if w.WarmSpares != nil {
			projected.WorkerWarmSpares = *w.WarmSpares
		}
		if w.MaxSessionLifetimeSecs != nil {
			projected.WorkerMaxSessionLifetimeSecs = *w.MaxSessionLifetimeSecs
		}
	}
	if m.HibernateResetToDefault {
		projected.HibernateTimeoutMinutes = nil
	}
	if m.HibernateTimeoutMinutes != nil {
		projected.HibernateTimeoutMinutes = m.HibernateTimeoutMinutes
	}
	if m.MemoryLimitMB != nil {
		projected.MemoryLimitMB = m.MemoryLimitMB
	}
	if m.CPUQuotaPercent != nil {
		projected.CPUQuotaPercent = m.CPUQuotaPercent
	}
	if m.MaxSessionsPerReplica != nil {
		projected.MaxSessionsPerReplica = *m.MaxSessionsPerReplica
	}
	if m.MinWarmReplicas != nil {
		projected.MinWarmReplicas = *m.MinWarmReplicas
	}
	if m.RenderSeconds != nil {
		projected.RenderSeconds = *m.RenderSeconds
	}
	if m.Name != nil {
		projected.Name = *m.Name
	}
	if m.Description != nil {
		projected.Description = *m.Description
	}
	if m.Icon != nil {
		projected.IconEmoji = *m.Icon
	}
	if m.Project != nil {
		projected.ProjectSlug = *m.Project
	}
	if a := m.Autoscale; a != nil {
		if a.Enabled != nil {
			projected.AutoscaleEnabled = *a.Enabled
		}
		projected.AutoscaleMinReplicas, projected.AutoscaleMaxReplicas, projected.AutoscaleTarget = a.MinReplicas, a.MaxReplicas, a.Target
	}
	return &projected
}

func (s *Server) applyHandoffProxySettings(app *db.App) {
	s.proxy.SetPoolCap(app.Slug, deploy.ResolveMaxSessionsPerReplica(app.MaxSessionsPerReplica, s.cfg.Runtime.DefaultMaxSessionsPerReplica))
	s.proxy.ApplyRenderPacing(app.Slug, app.RenderSeconds)
}

func (s *Server) generationDrainTimeout() time.Duration {
	if s.cfg.Server.DrainTimeout > 0 {
		return s.cfg.Server.DrainTimeout
	}
	return time.Minute
}

func (s *Server) generationDrainTimeoutSeconds() int {
	return int((s.generationDrainTimeout() + time.Second - 1) / time.Second)
}

// Wait for the existing retirement owner; never force a second drain or discard
// an unconfirmed process ledger merely to make the next deployment fit.
func (s *Server) waitForPreviousGeneration(ctx context.Context, app *db.App, previousID int64, grouped bool) error {
	if s.proxy == nil {
		return nil
	}
	timer := time.NewTimer(s.generationDrainTimeout() + 2*s.cfg.Server.StopGrace + 10*time.Second)
	defer timer.Stop()
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	for {
		rows, err := s.store.ListDeploymentReplicas(app.ID)
		if err != nil {
			return fmt.Errorf("inspect previous generation cleanup: %w", err)
		}
		if !s.proxy.HasDrainingGeneration(app.Slug) && !hasBlockingGenerationRows(rows, previousID, grouped) {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-timer.C:
			return errors.New("previous generation drain or process cleanup did not finish within the wait deadline")
		case <-ticker.C:
		}
	}
}

func (s *Server) reconcileHandoffPolicy(slug string, appID int64) {
	app, err := s.store.GetAppByID(appID)
	if err != nil {
		slog.Error("handoff policy reconciliation failed", "slug", slug, "err", err)
		return
	}
	s.applyHandoffProxySettings(app)
	schedules, err := s.store.ListSchedulesByApp(appID)
	if err != nil {
		slog.Error("handoff schedule reconciliation failed", "slug", slug, "err", err)
		return
	}
	for _, schedule := range schedules {
		if err := s.reloadScheduler(schedule.ID, slug, schedule.Name); err != nil {
			slog.Error("handoff schedule reload failed", "slug", slug, "schedule", schedule.Name, "err", err)
		}
	}
}
