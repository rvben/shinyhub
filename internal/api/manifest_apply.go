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

// validateManifestForServer applies server-policy checks (e.g. MaxReplicas)
// that depend on runtime config, not just on the manifest itself. Called by
// the deploy handler BEFORE tearing down the running pool so that a manifest
// rejected by policy returns 400 without disturbing live traffic. The basic
// per-field bounds (Replicas >= 1, MaxSessions 0..1000) are already enforced
// at parse time in deploy.LoadManifest.
func (s *Server) validateManifestForServer(m deploy.AppSettings) *validationError {
	if m.IsZero() {
		return nil
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

// applyManifestSchedules (Phase B) upserts each [[schedule]] from the
// manifest. Must be called after CreateDeployment succeeds — a scheduler
// tick between Reload and CreateDeployment could otherwise fire a job
// against the previous bundle.
//
// scheduler.ErrNotStarted is logged but does not fail the apply: the
// persisted row activates on the next Start.
func (s *Server) applyManifestSchedules(r *http.Request, app *db.App, specs []deploy.ScheduleSpec) error {
	for _, spec := range specs {
		cmdJSON, err := json.Marshal(spec.Command)
		if err != nil {
			return fmt.Errorf("schedule %q: marshal command: %w", spec.Name, err)
		}
		timeout := 3600
		if spec.TimeoutSeconds != nil {
			timeout = *spec.TimeoutSeconds
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
		})
		if err != nil {
			return fmt.Errorf("schedule %q: %w", spec.Name, err)
		}
		if s.scheduler != nil {
			if err := s.scheduler.Reload(id); err != nil {
				if errors.Is(err, scheduler.ErrNotStarted) {
					slog.Warn("manifest: scheduler not started; row persisted, will activate on scheduler start",
						"slug", app.Slug, "schedule", spec.Name)
				} else {
					return fmt.Errorf("scheduler reload (%s): %w", spec.Name, err)
				}
			}
		}
		action := "schedule_update"
		if created {
			action = "schedule_create"
		}
		s.audit(r, action, "schedule", fmt.Sprintf("%d", id),
			fmt.Sprintf(`{"app":%q,"name":%q}`, app.Slug, spec.Name))
	}
	return nil
}

func derefOrZero(p *int) int {
	if p == nil {
		return 0
	}
	return *p
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
