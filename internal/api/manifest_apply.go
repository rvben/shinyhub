package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"

	"github.com/rvben/shinyhub/internal/db"
	"github.com/rvben/shinyhub/internal/deploy"
	"github.com/rvben/shinyhub/internal/lifecycle/scheduler"
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
// Returns wrapped DB errors on storage failure (handler → 500 + degraded).
func (s *Server) applyManifestAppSettings(r *http.Request, app *db.App, m deploy.AppSettings) error {
	if m.IsZero() {
		return nil
	}

	if err := s.store.ApplyAppManifestSettings(db.ApplyAppManifestSettingsParams{
		AppID:                    app.ID,
		Slug:                     app.Slug,
		SetHibernate:             m.HibernateTimeoutMinutes != nil || m.HibernateResetToDefault,
		HibernateMinutes:         m.HibernateTimeoutMinutes, // nil ⇒ NULL (reset to default)
		SetReplicas:              m.Replicas != nil,
		Replicas:                 derefOrZero(m.Replicas),
		PreviousReplicas:         app.Replicas,
		SetMaxSessionsPerReplica: m.MaxSessionsPerReplica != nil,
		MaxSessionsPerReplica:    derefOrZero(m.MaxSessionsPerReplica),
	}); err != nil {
		return fmt.Errorf("apply app settings: %w", err)
	}

	if m.MaxSessionsPerReplica != nil && s.proxy != nil {
		s.proxy.SetPoolCap(app.Slug,
			deploy.ResolveMaxSessionsPerReplica(*m.MaxSessionsPerReplica, s.cfg.Runtime.DefaultMaxSessionsPerReplica))
	}

	s.audit(r, "update_app", "app", app.Slug, manifestAppDetail(m))
	return nil
}

// ManifestScheduleResult records the outcome of one [[schedule]] upsert so
// callers can surface a per-schedule action in their response.
type ManifestScheduleResult struct {
	Name       string        `json:"name"`
	Action     string        `json:"action"` // "created" or "updated"
	ScheduleID int64         `json:"schedule_id,omitempty"`
	FirstFire  *FirstFireRef `json:"first_fire,omitempty"`
}

// FirstFireRef points the CLI at the run dispatched by run_on_register so it
// can report it and (under --wait-for-warm) poll it to completion.
type FirstFireRef struct {
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
func (s *Server) applyManifestSchedules(r *http.Request, app *db.App, specs []deploy.ScheduleSpec) ([]ManifestScheduleResult, error) {
	results := make([]ManifestScheduleResult, 0, len(specs))
	for _, spec := range specs {
		cmdJSON, err := json.Marshal(spec.Command)
		if err != nil {
			return results, fmt.Errorf("schedule %q: marshal command: %w", spec.Name, err)
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
		id, created, err := s.store.UpsertScheduleByName(db.UpsertScheduleByNameParams{
			AppID:          app.ID,
			Name:           spec.Name,
			CronExpr:       spec.Cron,
			CommandJSON:    string(cmdJSON),
			Enabled:        !spec.Disabled,
			TimeoutSeconds: timeout,
			OverlapPolicy:  spec.Overlap,
			MissedPolicy:   spec.Missed,
			Timezone:       tzPtr,
		})
		if err != nil {
			return results, fmt.Errorf("schedule %q: %w", spec.Name, err)
		}
		if err := s.reloadScheduler(id, app.Slug, spec.Name); err != nil {
			return results, fmt.Errorf("scheduler reload (%s): %w", spec.Name, err)
		}
		auditAction := "schedule_update"
		resultAction := "updated"
		if created {
			auditAction = "schedule_create"
			resultAction = "created"
		}
		effectiveTZ := effectiveTZLabel(tzPtr, s.cfg.Scheduler.Location)
		s.audit(r, auditAction, "schedule", fmt.Sprintf("%d", id),
			fmt.Sprintf(`{"app":%q,"name":%q,"effective_timezone":%q}`, app.Slug, spec.Name, effectiveTZ))

		result := ManifestScheduleResult{Name: spec.Name, Action: resultAction, ScheduleID: id}
		if rid := s.maybeFirstFire(id, spec.RunOnRegister, spec.Disabled, app.Slug, spec.Name); rid != nil {
			result.FirstFire = &FirstFireRef{RunID: *rid}
		}
		results = append(results, result)
	}
	return results, nil
}

// maybeFirstFire fires the schedule once for run_on_register and returns the
// dispatched run id, or nil. It NEVER fails the caller: a disabled schedule, an
// unavailable job manager, a closed gate (the schedule has already succeeded),
// or a dispatch error all yield nil, with problems logged. The gate is "has
// this schedule ever succeeded?" so a failed first-fire self-heals on the next
// registration.
//
// Inline dispatch is safe because ownerGuard gates both the deploy POST and the
// schedule-create endpoint, so only the owner instance reaches here - the same
// invariant the scheduler relies on when it calls jobs.Run.
func (s *Server) maybeFirstFire(scheduleID int64, runOnRegister, disabled bool, slug, name string) *int64 {
	if !runOnRegister || disabled || s.jobs == nil {
		return nil
	}
	// Fire only when the schedule has never had a successful run. A non-nil
	// error that is NOT ErrNotFound means the gate check itself failed; skip
	// firing and log rather than risk a double-fire on uncertain state.
	if _, lerr := s.store.LastSuccessfulRun(scheduleID); !errors.Is(lerr, db.ErrNotFound) {
		if lerr != nil {
			slog.Warn("run_on_register: gate check failed; skipping first-fire",
				"slug", slug, "schedule", name, "err", lerr)
		}
		return nil
	}
	runID, rerr := s.jobs.Run(scheduleID, "register", nil)
	if rerr != nil {
		slog.Warn("run_on_register: first-fire dispatch failed",
			"slug", slug, "schedule", name, "err", rerr)
		return nil
	}
	return &runID
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

// ManifestApplied summarises what the manifest changed during this deploy.
// Returned alongside the app in the deploy response so CLI / UI can show
// the operator a concrete record of what landed.
type ManifestApplied struct {
	App       map[string]any           `json:"app,omitempty"`
	Schedules []ManifestScheduleResult `json:"schedules,omitempty"`
}

// IsEmpty reports whether nothing was applied. The handler omits the field
// from the response in that case so the wire shape stays clean.
func (m *ManifestApplied) IsEmpty() bool {
	return m == nil || (len(m.App) == 0 && len(m.Schedules) == 0)
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
	b, _ := json.Marshal(d)
	return string(b)
}
