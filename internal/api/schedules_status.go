package api

import (
	"net/http"
	"time"

	"github.com/rvben/shinyhub/internal/db"
	"github.com/rvben/shinyhub/internal/schedulespec"
)

// scheduleStatusItem is one row of GET /api/fleet/schedules/status.
type scheduleStatusItem struct {
	Slug                 string  `json:"slug"`
	Schedule             string  `json:"schedule"`
	Enabled              bool    `json:"enabled"`
	LastRunID            *int64  `json:"last_run_id"`        // null if never run
	LastRunAt            *string `json:"last_run_at"`        // RFC3339, null if never run
	LastRunStatus        string  `json:"last_run_status"`    // "" if never run
	LastSuccessAt        *string `json:"last_success_at"`    // RFC3339, null if never succeeded
	LastSuccessAgeS      *int64  `json:"last_success_age_s"` // null if never succeeded
	Stale                *bool   `json:"stale"`
	Refreshing           bool    `json:"refreshing"`
	ActiveRunID          *int64  `json:"active_run_id"`
	FreshnessError       string  `json:"freshness_error"`
	ActivationStatus     string  `json:"activation_status"`
	ActivationPhase      string  `json:"activation_phase,omitempty"`
	ActivationAgeS       *int64  `json:"activation_age_s,omitempty"`
	ActivationDueAt      *string `json:"activation_due_at,omitempty"`
	ActivationGeneration *int64  `json:"activation_target_generation,omitempty"`
	ActivationError      string  `json:"activation_error,omitempty"`
	ActivationAttention  bool    `json:"activation_attention"`
	ServingFreshness     string  `json:"serving_freshness"`
}

func activationServingState(status string) (string, bool) {
	switch status {
	case "succeeded", "not_needed":
		return "current", false
	case "pending", "deferred_interval", "running":
		return "pending", false
	case "deferred_capacity", "repairing":
		return "pending", true
	case "failed", "cancelled", "blocked_unsupported":
		return "stale", true
	case "target_deleted":
		return "unavailable", true
	case "superseded":
		return "superseded", false
	default:
		return "unknown", false
	}
}

// scheduleStale maps a db.ScheduleFreshness to the policy struct and applies
// schedulespec.IsStale, resolving the per-schedule timezone against def. Shared
// by the status endpoint and the fleet-health banner.
func scheduleStale(fr db.ScheduleFreshness, def *time.Location, now time.Time) (bool, error) {
	loc, err := fr.EffectiveLocationChecked(def)
	if err != nil {
		return false, err
	}
	return schedulespec.EvaluateStale(scheduleFreshnessPolicy(fr), loc, now)
}

func scheduleFreshnessPolicy(fr db.ScheduleFreshness) schedulespec.Freshness {
	return schedulespec.Freshness{
		Enabled:        fr.Enabled,
		CronExpr:       fr.CronExpr,
		CreatedAt:      fr.CreatedAt,
		TimeoutSeconds: fr.TimeoutSeconds,
		LastRunStatus:  fr.LastRunStatus,
		LastRunAt:      fr.LastRunAt,
		LastSuccessAt:  fr.LastSuccessAt,
		ActiveRunAt:    fr.ActiveRunAt,
	}
}

// handleFleetScheduleStatus returns per-schedule freshness across the fleet:
// last run + status, last success + age, and a cron-aware stale flag.
// Admin-or-operator and side-effect free. ?slug=<slug> filters to one app.
//
// Rows are filtered by the caller's app scope, so a scoped deploy token cannot
// enumerate schedules for apps outside its allowlist regardless of its role.
func (s *Server) handleFleetScheduleStatus(w http.ResponseWriter, r *http.Request) {
	u, ok := requireOperator(w, r)
	if !ok {
		return
	}
	rows, err := s.store.ScheduleFreshness()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal server error")
		return
	}
	filter := r.URL.Query().Get("slug")
	// May be nil when the config is constructed directly (e.g. in tests);
	// EffectiveLocation tolerates a nil default and falls back to UTC.
	def := s.cfg.Scheduler.Location
	now := time.Now()
	out := make([]scheduleStatusItem, 0, len(rows))
	for _, fr := range rows {
		if filter != "" && fr.Slug != filter {
			continue
		}
		if !u.AppInScope(fr.Slug) {
			continue
		}
		stale, staleErr := scheduleStale(fr, def, now)
		item := scheduleStatusItem{
			Slug:                 fr.Slug,
			Schedule:             fr.Name,
			Enabled:              fr.Enabled,
			LastRunID:            fr.LastRunID,
			LastRunStatus:        fr.LastRunStatus,
			Refreshing:           schedulespec.IsRefreshing(scheduleFreshnessPolicy(fr), now),
			ActiveRunID:          fr.ActiveRunID,
			ActivationStatus:     fr.ActivationStatus,
			ActivationPhase:      fr.ActivationPhase,
			ActivationGeneration: fr.ActivationGeneration,
			ActivationError:      fr.ActivationError,
		}
		item.ServingFreshness, item.ActivationAttention = activationServingState(fr.ActivationStatus)
		if fr.ActivationCreatedAt != nil {
			age := int64(now.Sub(*fr.ActivationCreatedAt).Seconds())
			if age < 0 {
				age = 0
			}
			item.ActivationAgeS = &age
		}
		if fr.ActivationDueAt != nil {
			v := fr.ActivationDueAt.UTC().Format(time.RFC3339)
			item.ActivationDueAt = &v
		}
		if staleErr != nil {
			item.FreshnessError = "freshness could not be computed"
		} else {
			item.Stale = &stale
		}
		if fr.LastRunAt != nil {
			v := fr.LastRunAt.UTC().Format(time.RFC3339)
			item.LastRunAt = &v
		}
		if fr.LastSuccessAt != nil {
			v := fr.LastSuccessAt.UTC().Format(time.RFC3339)
			item.LastSuccessAt = &v
			age := int64(now.Sub(*fr.LastSuccessAt).Seconds())
			item.LastSuccessAgeS = &age
		}
		out = append(out, item)
	}
	limit, offset := parsePagination(r)
	writeList(w, out, limit, offset, nil)
}
