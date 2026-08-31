package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/rvben/shinyhub/internal/config"
	"github.com/rvben/shinyhub/internal/db"
	"github.com/rvben/shinyhub/internal/deploy"
	"github.com/rvben/shinyhub/internal/lifecycle/scheduler"
	"github.com/rvben/shinyhub/internal/schedulespec"
)

// validationError signals "user-provided manifest is invalid"; the handler
// maps it to 400. Anything else from applyManifest* maps to 500.
type validationError struct{ msg string }

func (e *validationError) Error() string { return e.msg }

func newValidationError(format string, args ...any) *validationError {
	return &validationError{msg: fmt.Sprintf(format, args...)}
}

// validateManifestForServer applies server-policy checks (MaxReplicas, tier
// placement) that depend on runtime config or stored app state, not just on
// the manifest itself. Called by the deploy handler BEFORE tearing down the
// running pool so that a manifest rejected by policy returns 400 without
// disturbing live traffic. The basic per-field bounds (Replicas >= 1,
// MaxSessions 0..1000) are already enforced at parse time in
// deploy.LoadManifest.
func (s *Server) validateManifestForServer(app *db.App, m deploy.AppSettings) *validationError {
	if m.IsZero() {
		return nil
	}
	if m.Replicas != nil && len(app.PlacementMap()) > 0 {
		return newValidationError("app uses tier placement; update placement instead of setting replicas")
	}
	if m.Replicas != nil && s.cfg.Runtime.MaxReplicas > 0 && *m.Replicas > s.cfg.Runtime.MaxReplicas {
		return newValidationError("replicas must be between 1 and %d", s.cfg.Runtime.MaxReplicas)
	}
	if m.UsageIdentityMode != nil {
		mode := config.UsageIdentityMode(*m.UsageIdentityMode)
		if *m.UsageIdentityMode != "disabled" && config.UsageIdentityRank(mode) > config.UsageIdentityRank(s.cfg.Usage.IdentityMode) {
			return newValidationError("usage_identity_mode cannot collect more identity than the hub policy")
		}
	}
	// The runtime MaxReplicas ceiling needs server config, so it is enforced
	// here rather than at parse time (matching the replicas check above and the
	// PATCH /api/apps autoscale handler). Only meaningful when enabled.
	if m.Autoscale != nil && m.Autoscale.Enabled != nil && *m.Autoscale.Enabled &&
		s.cfg.Runtime.MaxReplicas > 0 && m.Autoscale.MaxReplicas > s.cfg.Runtime.MaxReplicas {
		return newValidationError("autoscale.max_replicas must be <= %d", s.cfg.Runtime.MaxReplicas)
	}
	// Reject a manifest resource limit that exceeds the Fargate task ceiling BEFORE
	// Phase A writes it / the pool is torn down - otherwise the limit persists and
	// Fargate silently clamps it. Gated on allTiersFargate, matching the API PATCH
	// path. msg already contains rendered "%"/digits, so pass it as an arg.
	if msg := s.fargateLimitViolation(m.MemoryLimitMB, m.CPUQuotaPercent); msg != "" {
		return newValidationError("%s", msg)
	}
	if m.Worker != nil || m.MemoryLimitMB != nil {
		// Validate the POST-deploy state: stored worker columns overlaid with
		// the declared fields (nil = unchanged, matching apply semantics), the
		// isolation resolved through the fleet default exactly like the
		// runtime does, and a declared memory limit replacing the stored one.
		// A declared limit alone re-runs the math too: on an elastic app it
		// moves the per-worker budget term.
		ws := config.WorkerSettings{
			Isolation:          config.WorkerIsolationMode(app.WorkerIsolation),
			GroupedSize:        app.WorkerGroupedSize,
			MaxWorkers:         app.WorkerMaxWorkers,
			WarmSpares:         app.WorkerWarmSpares,
			MaxSessionLifetime: app.WorkerMaxSessionLifetimeSecs,
		}
		if m.Worker != nil {
			if m.Worker.Isolation != nil {
				ws.Isolation = config.WorkerIsolationMode(*m.Worker.Isolation)
			}
			if m.Worker.GroupedSize != nil {
				ws.GroupedSize = *m.Worker.GroupedSize
			}
			if m.Worker.MaxWorkers != nil {
				ws.MaxWorkers = *m.Worker.MaxWorkers
			}
			if m.Worker.WarmSpares != nil {
				ws.WarmSpares = *m.Worker.WarmSpares
			}
			if m.Worker.MaxSessionLifetimeSecs != nil {
				ws.MaxSessionLifetime = *m.Worker.MaxSessionLifetimeSecs
			}
		}
		ws.Isolation = config.WorkerIsolationMode(deploy.ResolveWorkerIsolation(
			string(ws.Isolation), s.cfg.Runtime.DefaultWorkerIsolation))
		declaredLimit := app.MemoryLimitMB
		if m.MemoryLimitMB != nil {
			declaredLimit = m.MemoryLimitMB
		}
		memMB, _ := s.cfg.Runtime.DefaultResourcesForApp(app)
		effMemMB := deploy.ResolveMemoryLimitMB(declaredLimit, memMB)
		if err := config.ValidateWorkerSettings(ws, s.clustered, effMemMB, s.cfg.HostBudgetMB()); err != nil {
			return newValidationError("%s", err.Error())
		}
		// The deploy response has no warning channel, so an unguarded elastic
		// configuration is surfaced in the server log instead (the PATCH path
		// additionally sends X-ShinyHub-Warning).
		if warn := config.WorkerBudgetWarning(ws, effMemMB, s.cfg.HostBudgetMB(), s.cfg.MinAvailableMemoryMB()); warn != "" {
			slog.Warn("manifest worker settings accepted without a memory guard",
				"app", app.Slug, "detail", warn)
		}
	}
	return nil
}

// validateManifestActivationTopology evaluates post-success actions against the
// projected post-manifest app, before the live pool is touched. This covers
// both newly declared roll schedules and existing roll schedules that a worker
// isolation change would otherwise strand after deployment.
func (s *Server) validateManifestActivationTopology(app *db.App, manifest *deploy.Manifest) *validationError {
	if manifest == nil {
		return nil
	}
	projected := *app
	if manifest.App.Worker != nil && manifest.App.Worker.Isolation != nil {
		projected.WorkerIsolation = *manifest.App.Worker.Isolation
	}
	type projectedPolicy struct {
		name          string
		deployTrigger string
		onSuccess     string
		existing      bool
	}
	policies := make(map[string]projectedPolicy, len(manifest.Schedules))
	policyOrder := make([]string, 0, len(manifest.Schedules))
	if manifest.App.Worker != nil && manifest.App.Worker.Isolation != nil {
		existing, err := s.store.ListSchedulesByApp(app.ID)
		if err != nil {
			return newValidationError("validate existing schedules: %v", err)
		}
		for _, schedule := range existing {
			policyOrder = append(policyOrder, schedule.Name)
			policies[schedule.Name] = projectedPolicy{
				name: schedule.Name, deployTrigger: schedule.DeployTrigger,
				onSuccess: schedule.OnSuccess, existing: true,
			}
		}
	}
	for _, spec := range manifest.Schedules {
		deployTrigger, err := schedulespec.NormalizeDeployTrigger(spec.DeployTrigger)
		if err != nil {
			return newValidationError("schedule %q: %v", spec.Name, err)
		}
		// Manifest declarations override same-name stored declarations. Validate
		// the projected final set once so an atomic policy+topology transition is
		// judged by what will actually be committed, not by stale live policy.
		if _, exists := policies[spec.Name]; !exists {
			policyOrder = append(policyOrder, spec.Name)
		}
		policies[spec.Name] = projectedPolicy{
			name: spec.Name, deployTrigger: deployTrigger, onSuccess: spec.OnSuccess,
		}
	}
	for _, name := range policyOrder {
		policy := policies[name]
		label := "schedule"
		if policy.existing {
			label = "existing schedule"
		}
		if err := s.validateScheduleActivationForApp(&projected, policy.onSuccess); err != nil {
			return newValidationError("%s %q: %v", label, policy.name, err)
		}
		if err := s.validateScheduleProducerTopology(&projected, policy.deployTrigger, policy.onSuccess); err != nil {
			return newValidationError("%s %q: %v", label, policy.name, err)
		}
	}
	return nil
}

// applyManifestAppSettings (Phase A) writes [app] settings to the DB in a
// single transaction. Replica shrink (delete obsolete replica rows) is
// part of that transaction.
//
// Caller contract:
//   - requireManageApp has already authorized r.
//   - validateManifestForServer has already returned nil.
//   - manager.Stop(app.Slug) has already run, so no process holds a
//     replica index that may be deleted.
//
// identity_headers is reconciled UNCONDITIONALLY (even when m.IsZero()): nil
// reverts the column to NULL so removing the key from the manifest restores
// the app to the global default. This function is only called when a manifest
// is present; the call site in apps.go gates on manifest != nil.
//
// Returns wrapped DB errors on storage failure (handler → 500 + degraded).
func (s *Server) applyManifestAppSettings(r *http.Request, app *db.App, m deploy.AppSettings) error {
	usagePolicyChanged := (app.UsageIdentityMode == nil) != (m.UsageIdentityMode == nil) ||
		(app.UsageIdentityMode != nil && m.UsageIdentityMode != nil && *app.UsageIdentityMode != *m.UsageIdentityMode)
	if usagePolicyChanged && s.usagePolicy == nil {
		return errors.New("usage privacy policy unavailable")
	}
	// Autoscale reconciles atomically only when the block is declared; the
	// zero values below are inert because SetAutoscale gates the DB write.
	// Resolve worker fields: nil pointer means "absent, leave stored value unchanged".
	var workerIsolation string
	var workerGroupedSize, workerMaxWorkers, workerWarmSpares, workerMaxSessionLifetimeSecs int
	if m.Worker != nil {
		if m.Worker.Isolation != nil {
			workerIsolation = *m.Worker.Isolation
		}
		if m.Worker.GroupedSize != nil {
			workerGroupedSize = *m.Worker.GroupedSize
		}
		if m.Worker.MaxWorkers != nil {
			workerMaxWorkers = *m.Worker.MaxWorkers
		}
		if m.Worker.WarmSpares != nil {
			workerWarmSpares = *m.Worker.WarmSpares
		}
		if m.Worker.MaxSessionLifetimeSecs != nil {
			workerMaxSessionLifetimeSecs = *m.Worker.MaxSessionLifetimeSecs
		}
	}
	var asEnabled bool
	var asMin, asMax int
	var asTarget float64
	if m.Autoscale != nil {
		// Enabled is guaranteed non-nil here: LoadManifest rejects a declared
		// autoscale block that omits it.
		asEnabled = m.Autoscale.Enabled != nil && *m.Autoscale.Enabled
		asMin = m.Autoscale.MinReplicas
		asMax = m.Autoscale.MaxReplicas
		asTarget = m.Autoscale.Target
	}
	if _, err := s.store.ApplyAppManifestSettings(db.ApplyAppManifestSettingsParams{
		AppID:                        app.ID,
		Slug:                         app.Slug,
		SetHibernate:                 m.HibernateTimeoutMinutes != nil || m.HibernateResetToDefault,
		HibernateMinutes:             m.HibernateTimeoutMinutes, // nil => NULL (reset to default)
		SetReplicas:                  m.Replicas != nil,
		Replicas:                     derefOrZero(m.Replicas),
		PreviousReplicas:             app.Replicas,
		SetMaxSessionsPerReplica:     m.MaxSessionsPerReplica != nil,
		MaxSessionsPerReplica:        derefOrZero(m.MaxSessionsPerReplica),
		SetRenderSeconds:             m.RenderSeconds != nil,
		RenderSeconds:                derefFloatOrZero(m.RenderSeconds),
		SetIdentityHeaders:           true,
		IdentityHeaders:              m.IdentityHeaders,
		SetUsageIdentityMode:         usagePolicyChanged,
		UsageIdentityMode:            m.UsageIdentityMode,
		SetMinWarmReplicas:           m.MinWarmReplicas != nil,
		MinWarmReplicas:              derefOrZero(m.MinWarmReplicas),
		SetMemoryLimitMB:             m.MemoryLimitMB != nil,
		MemoryLimitMB:                m.MemoryLimitMB,
		SetCPUQuotaPercent:           m.CPUQuotaPercent != nil,
		CPUQuotaPercent:              m.CPUQuotaPercent,
		SetAutoscale:                 m.Autoscale != nil,
		AutoscaleEnabled:             asEnabled,
		AutoscaleMinReplicas:         asMin,
		AutoscaleMaxReplicas:         asMax,
		AutoscaleTarget:              asTarget,
		SetWorkerIsolation:           m.Worker != nil && m.Worker.Isolation != nil,
		WorkerIsolation:              workerIsolation,
		SetWorkerGroupedSize:         m.Worker != nil && m.Worker.GroupedSize != nil,
		WorkerGroupedSize:            workerGroupedSize,
		SetWorkerMaxWorkers:          m.Worker != nil && m.Worker.MaxWorkers != nil,
		WorkerMaxWorkers:             workerMaxWorkers,
		SetWorkerWarmSpares:          m.Worker != nil && m.Worker.WarmSpares != nil,
		WorkerWarmSpares:             workerWarmSpares,
		SetWorkerMaxSessionLifetime:  m.Worker != nil && m.Worker.MaxSessionLifetimeSecs != nil,
		WorkerMaxSessionLifetimeSecs: workerMaxSessionLifetimeSecs,
		SetIconEmoji:                 m.Icon != nil,
		IconEmoji:                    derefStringOrEmpty(m.Icon),
		SetName:                      m.Name != nil,
		Name:                         derefStringOrEmpty(m.Name),
		SetDescription:               m.Description != nil,
		Description:                  derefStringOrEmpty(m.Description),
		SetProjectSlug:               m.Project != nil,
		ProjectSlug:                  derefStringOrEmpty(m.Project),
	}); err != nil {
		return fmt.Errorf("apply app settings: %w", err)
	}
	if usagePolicyChanged {
		newOverride := ""
		if m.UsageIdentityMode != nil {
			newOverride = *m.UsageIdentityMode
		}
		if _, err := s.usagePolicy.ApplyCommittedAppPolicy(s.store, app.ID, app.Slug, newOverride); err != nil {
			return fmt.Errorf("usage privacy policy committed but retained-data reconciliation failed: %w", err)
		}
	}

	if m.MaxSessionsPerReplica != nil && s.proxy != nil {
		s.proxy.SetPoolCap(app.Slug,
			deploy.ResolveMaxSessionsPerReplica(*m.MaxSessionsPerReplica, s.cfg.Runtime.DefaultMaxSessionsPerReplica))
	}
	if m.RenderSeconds != nil && s.proxy != nil {
		s.proxy.ApplyRenderPacing(app.Slug, *m.RenderSeconds)
	}
	// SetPoolMode: propagate any worker-isolation change from the manifest so
	// the live pool adopts the new routing strategy immediately. The subsequent
	// deploy.Run (triggered by the caller's redeploy path) will set it again,
	// but this call covers apps that are stopped or not yet deployed.
	// Use the resolved manifest values (not app, which is the pre-write snapshot).
	if m.Worker != nil && (m.Worker.Isolation != nil || m.Worker.GroupedSize != nil || m.Worker.MaxWorkers != nil || m.Worker.WarmSpares != nil) && s.proxy != nil {
		effectiveIsolation := app.WorkerIsolation
		if workerIsolation != "" {
			effectiveIsolation = workerIsolation
		}
		effectiveGroupedSize := app.WorkerGroupedSize
		if workerGroupedSize != 0 {
			effectiveGroupedSize = workerGroupedSize
		}
		effectiveMaxWorkers := app.WorkerMaxWorkers
		if workerMaxWorkers != 0 {
			effectiveMaxWorkers = workerMaxWorkers
		}
		effectiveWarmSpares := app.WorkerWarmSpares
		if m.Worker.WarmSpares != nil {
			effectiveWarmSpares = workerWarmSpares
		}
		s.proxy.SetPoolMode(app.Slug,
			config.WorkerIsolationMode(deploy.ResolveWorkerIsolation(effectiveIsolation, s.cfg.Runtime.DefaultWorkerIsolation)),
			effectiveGroupedSize, effectiveMaxWorkers)
		s.proxy.SetPoolWarmSpares(app.Slug, effectiveWarmSpares)
	}
	// Unconditional: a removed key must revert the live pool too (an
	// atomic store; unconditional costs nothing).
	if s.proxy != nil {
		s.proxy.SetPoolIdentityHeaders(app.Slug,
			deploy.ResolveIdentityHeaders(m.IdentityHeaders, s.cfg.Auth.IdentityHeadersEnabled()))
	}

	if !m.IsZero() {
		s.audit(r, "update_app", "app", app.Slug, manifestAppDetail(m))
	}
	return nil
}

// ManifestScheduleResult records the outcome of one [[schedule]] upsert so
// callers can surface a per-schedule action in their response.
type ManifestScheduleResult struct {
	Name       string        `json:"name"`
	Action     string        `json:"action"` // "created" or "updated"
	ScheduleID int64         `json:"schedule_id,omitempty"`
	DeployRun  *DeployRunRef `json:"deploy_run,omitempty"`
}

// DeployRunRef points the CLI at the run dispatched by a schedule's
// deploy_trigger so it can report it and wait for bundle convergence.
type DeployRunRef struct {
	RunID int64 `json:"run_id"`
}

// applyManifestSchedules (Phase B) upserts each [[schedule]] from the
// manifest. Must be called after CreateDeployment succeeds — a scheduler
// tick between Reload and CreateDeployment could otherwise fire a job
// against the previous bundle.
//
// scheduler.ErrNotStarted is logged but does not fail the apply: the
// persisted row activates on the next Start.
//
// Returns one result entry per spec in input order. On error the slice
// contains the results processed so far.
func (s *Server) applyManifestSchedules(r *http.Request, app *db.App, deployment *db.Deployment, specs []deploy.ScheduleSpec, plannedCreates ...map[string]int64) ([]ManifestScheduleResult, error) {
	if deployment == nil || deployment.AppID != app.ID || deployment.ContentDigest == "" {
		return nil, errors.New("manifest schedule apply requires the target deployment")
	}
	params := make([]db.UpsertScheduleByNameParams, 0, len(specs))
	timezones := make([]*string, 0, len(specs))
	for _, spec := range specs {
		deployTrigger, err := schedulespec.NormalizeDeployTrigger(spec.DeployTrigger)
		if err != nil {
			return nil, fmt.Errorf("schedule %q: %w", spec.Name, err)
		}
		if err := s.validateScheduleActivationForApp(app, spec.OnSuccess); err != nil {
			return nil, fmt.Errorf("schedule %q: %w", spec.Name, err)
		}
		if err := s.validateScheduleProducerTopology(app, deployTrigger, spec.OnSuccess); err != nil {
			return nil, fmt.Errorf("schedule %q: %w", spec.Name, err)
		}
		cmdJSON, err := json.Marshal(spec.Command)
		if err != nil {
			return nil, fmt.Errorf("schedule %q: marshal command: %w", spec.Name, err)
		}
		timeout := 3600
		if spec.TimeoutSeconds != nil {
			timeout = *spec.TimeoutSeconds
		}
		// Convert empty timezone to nil (NULL = inherit server default).
		var tzPtr *string
		if spec.Timezone != "" {
			tzPtr = &spec.Timezone
		}
		params = append(params, db.UpsertScheduleByNameParams{
			AppID:                  app.ID,
			Name:                   spec.Name,
			CronExpr:               spec.Cron,
			CommandJSON:            string(cmdJSON),
			Enabled:                !spec.Disabled,
			TimeoutSeconds:         timeout,
			OverlapPolicy:          spec.Overlap,
			MissedPolicy:           spec.Missed,
			DeployTrigger:          deployTrigger,
			Timezone:               tzPtr,
			OnSuccess:              spec.OnSuccess,
			MinRollIntervalSeconds: int(spec.MinRollInterval / time.Second),
			RollFallback:           spec.RollFallback,
			MaxDeferAgeSeconds:     int(spec.MaxDeferAge / time.Second),
		})
		timezones = append(timezones, tzPtr)
	}
	upserts, err := s.store.UpsertSchedulesByName(params)
	if err != nil {
		return nil, err
	}
	results := make([]ManifestScheduleResult, 0, len(specs))
	for i, spec := range specs {
		id, created := upserts[i].ID, upserts[i].Created
		if len(plannedCreates) > 0 && plannedCreates[0][spec.Name] == id {
			created = true
		}
		auditAction := "schedule_update"
		resultAction := "updated"
		if created {
			auditAction = "schedule_create"
			resultAction = "created"
		}
		effectiveTZ := effectiveTZLabel(timezones[i], s.cfg.Scheduler.Location)
		s.audit(r, auditAction, "schedule", fmt.Sprintf("%d", id),
			fmt.Sprintf(`{"app":%q,"name":%q,"effective_timezone":%q}`, app.Slug, spec.Name, effectiveTZ))

		results = append(results, ManifestScheduleResult{Name: spec.Name, Action: resultAction, ScheduleID: id})
	}
	// Persistence is already atomic. Reload every committed row; if one reload
	// fails the caller keeps the app stopped rather than exposing this new
	// declaration set against the previous bundle.
	for i, spec := range specs {
		if err := s.reloadScheduler(upserts[i].ID, app.Slug, spec.Name); err != nil {
			return results, fmt.Errorf("scheduler reload (%s): %w", spec.Name, err)
		}
	}
	return results, nil
}

// reloadScheduler re-registers a schedule with the cron engine after a create or
// update. scheduler.ErrNotStarted is soft: the row is already persisted and will
// activate when the scheduler starts, so it logs a warning and returns nil. A nil
// scheduler is likewise a no-op. Any other error is returned to the caller.
//
// This is the single reload policy shared by the create/patch handlers and
// manifest apply, so the brief "scheduler not started yet" startup window behaves
// identically on every path (the row is persisted; the caller does not fail).
func (s *Server) reloadScheduler(id int64, slug, name string) error {
	if s.scheduler == nil {
		return nil
	}
	if err := s.scheduler.Reload(id); err != nil {
		if errors.Is(err, scheduler.ErrNotStarted) {
			slog.Warn("scheduler not started; schedule row persisted, will activate on scheduler start",
				"slug", slug, "schedule", name)
			return nil
		}
		return err
	}
	return nil
}

func derefOrZero(p *int) int {
	if p == nil {
		return 0
	}
	return *p
}

func derefFloatOrZero(p *float64) float64 {
	if p == nil {
		return 0
	}
	return *p
}

func derefStringOrEmpty(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

// applyManifestAccessGroups flattens the manifest [access] block to desired
// group rules (manager wins when a group appears in both lists) and reconciles
// them into app_group_access as source='manifest', preserving any manual rules.
func (s *Server) applyManifestAccessGroups(app *db.App, access deploy.AppAccess) ([]ManifestAccessGroupResult, error) {
	desired := map[string]string{}
	for _, g := range access.ViewerGroups {
		desired[g] = db.HigherMemberRole(desired[g], "viewer")
	}
	for _, g := range access.ManagerGroups {
		desired[g] = db.HigherMemberRole(desired[g], "manager")
	}
	rules := make([]db.AppGroupRule, 0, len(desired))
	for g, role := range desired {
		rules = append(rules, db.AppGroupRule{Group: g, Role: role})
	}
	skipped, err := s.store.ReconcileAppGroupAccessFromManifest(app.Slug, rules)
	if err != nil {
		return nil, err
	}
	skippedSet := map[string]struct{}{}
	for _, g := range skipped {
		skippedSet[g] = struct{}{}
	}
	results := make([]ManifestAccessGroupResult, 0, len(rules))
	for _, r := range rules {
		_, sk := skippedSet[r.Group]
		results = append(results, ManifestAccessGroupResult{Group: r.Group, Role: r.Role, Skipped: sk})
	}
	return results, nil
}

// ManifestAccessGroupResult records the outcome of one [access] group rule
// reconciled from the manifest into app_group_access.
type ManifestAccessGroupResult struct {
	Group   string `json:"group"`
	Role    string `json:"role"`
	Skipped bool   `json:"skipped,omitempty"` // true when a manual rule preempted this manifest rule
}

// ManifestApplied summarises what the manifest changed during this deploy.
// Returned alongside the app in the deploy response so CLI / UI can show
// the operator a concrete record of what landed.
type ManifestApplied struct {
	App                map[string]any              `json:"app,omitempty"`
	Schedules          []ManifestScheduleResult    `json:"schedules,omitempty"`
	AccessGroups       []ManifestAccessGroupResult `json:"access_groups,omitempty"`
	IconShadowedUpload bool                        `json:"icon_shadowed_upload,omitempty"`
	// Warnings are advisories about settings the manifest declared that were
	// accepted but cannot take effect as written (for example a keep-warm
	// floor under elastic isolation). The deploy itself succeeded; the CLI
	// prints each one so the operator learns it at deploy time rather than
	// from a health wait that never ends.
	Warnings []string `json:"warnings,omitempty"`
}

// IsEmpty reports whether nothing was applied. The handler omits the field
// from the response in that case so the wire shape stays clean.
func (m *ManifestApplied) IsEmpty() bool {
	return m == nil || (len(m.App) == 0 && len(m.Schedules) == 0 && len(m.AccessGroups) == 0 && len(m.Warnings) == 0)
}

// manifestAppliedSummary computes the per-field record of [app] changes. It
// mirrors manifestAppDetail (the audit-event detail blob) but returns a
// structured map suitable for JSON serialisation.
func manifestAppliedSummary(m deploy.AppSettings) map[string]any {
	if m.IsZero() {
		return nil
	}
	d := map[string]any{}
	if m.HibernateResetToDefault {
		d["hibernate_timeout_minutes"] = nil
	} else if m.HibernateTimeoutMinutes != nil {
		d["hibernate_timeout_minutes"] = *m.HibernateTimeoutMinutes
	}
	if m.Replicas != nil {
		d["replicas"] = *m.Replicas
	}
	if m.MaxSessionsPerReplica != nil {
		d["max_sessions_per_replica"] = *m.MaxSessionsPerReplica
	}
	if m.RenderSeconds != nil {
		d["render_seconds"] = *m.RenderSeconds
	}
	if m.IdentityHeaders != nil {
		d["identity_headers"] = *m.IdentityHeaders
	}
	if m.UsageIdentityMode != nil {
		d["usage_identity_mode"] = *m.UsageIdentityMode
	}
	if m.MinWarmReplicas != nil {
		d["min_warm_replicas"] = *m.MinWarmReplicas
	}
	if m.MemoryLimitMB != nil {
		d["memory_limit_mb"] = *m.MemoryLimitMB
	}
	if m.CPUQuotaPercent != nil {
		d["cpu_quota_percent"] = *m.CPUQuotaPercent
	}
	if m.Autoscale != nil {
		d["autoscale"] = map[string]any{
			"enabled":      m.Autoscale.Enabled != nil && *m.Autoscale.Enabled,
			"min_replicas": m.Autoscale.MinReplicas,
			"max_replicas": m.Autoscale.MaxReplicas,
			"target":       m.Autoscale.Target,
		}
	}
	if len(m.Command) > 0 {
		d["command"] = m.Command
	}
	if m.Worker != nil {
		w := map[string]any{}
		if m.Worker.Isolation != nil {
			w["isolation"] = *m.Worker.Isolation
		}
		if m.Worker.GroupedSize != nil {
			w["grouped_size"] = *m.Worker.GroupedSize
		}
		if m.Worker.MaxWorkers != nil {
			w["max_workers"] = *m.Worker.MaxWorkers
		}
		if m.Worker.WarmSpares != nil {
			w["warm_spares"] = *m.Worker.WarmSpares
		}
		if m.Worker.MaxSessionLifetimeSecs != nil {
			w["max_session_lifetime_secs"] = *m.Worker.MaxSessionLifetimeSecs
		}
		d["worker"] = w
	}
	if m.Icon != nil {
		d["icon"] = *m.Icon
	}
	if m.Name != nil {
		d["name"] = *m.Name
	}
	if m.Description != nil {
		d["description"] = *m.Description
	}
	if m.Project != nil {
		d["project"] = *m.Project
	}
	return d
}

func manifestAppDetail(m deploy.AppSettings) string {
	d := map[string]any{}
	if m.HibernateResetToDefault {
		d["hibernate_timeout_minutes"] = nil
	} else if m.HibernateTimeoutMinutes != nil {
		d["hibernate_timeout_minutes"] = *m.HibernateTimeoutMinutes
	}
	if m.Replicas != nil {
		d["replicas"] = *m.Replicas
	}
	if m.MaxSessionsPerReplica != nil {
		d["max_sessions_per_replica"] = *m.MaxSessionsPerReplica
	}
	if m.RenderSeconds != nil {
		d["render_seconds"] = *m.RenderSeconds
	}
	if m.IdentityHeaders != nil {
		d["identity_headers"] = *m.IdentityHeaders
	}
	if m.UsageIdentityMode != nil {
		d["usage_identity_mode"] = *m.UsageIdentityMode
	}
	if m.MinWarmReplicas != nil {
		d["min_warm_replicas"] = *m.MinWarmReplicas
	}
	if m.MemoryLimitMB != nil {
		d["memory_limit_mb"] = *m.MemoryLimitMB
	}
	if m.CPUQuotaPercent != nil {
		d["cpu_quota_percent"] = *m.CPUQuotaPercent
	}
	if m.Autoscale != nil {
		d["autoscale"] = map[string]any{
			"enabled":      m.Autoscale.Enabled != nil && *m.Autoscale.Enabled,
			"min_replicas": m.Autoscale.MinReplicas,
			"max_replicas": m.Autoscale.MaxReplicas,
			"target":       m.Autoscale.Target,
		}
	}
	if len(m.Command) > 0 {
		d["command"] = m.Command
	}
	if m.Worker != nil {
		w := map[string]any{}
		if m.Worker.Isolation != nil {
			w["isolation"] = *m.Worker.Isolation
		}
		if m.Worker.GroupedSize != nil {
			w["grouped_size"] = *m.Worker.GroupedSize
		}
		if m.Worker.MaxWorkers != nil {
			w["max_workers"] = *m.Worker.MaxWorkers
		}
		if m.Worker.WarmSpares != nil {
			w["warm_spares"] = *m.Worker.WarmSpares
		}
		if m.Worker.MaxSessionLifetimeSecs != nil {
			w["max_session_lifetime_secs"] = *m.Worker.MaxSessionLifetimeSecs
		}
		d["worker"] = w
	}
	if m.Icon != nil {
		d["icon"] = *m.Icon
	}
	if m.Name != nil {
		d["name"] = *m.Name
	}
	if m.Description != nil {
		d["description"] = *m.Description
	}
	if m.Project != nil {
		d["project"] = *m.Project
	}
	b, _ := json.Marshal(d)
	return string(b)
}
