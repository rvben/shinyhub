package api

import (
	"net/http"
	"time"

	"github.com/rvben/shinyhub/internal/db"
	"github.com/rvben/shinyhub/internal/schedulespec"
)

// scheduleStatusItem is one row of GET /api/fleet/schedules/status.
type scheduleStatusItem struct {
	Slug            string  `json:"slug"`
	Schedule        string  `json:"schedule"`
	Enabled         bool    `json:"enabled"`
	LastRunAt       *string `json:"last_run_at"`        // RFC3339, null if never run
	LastRunStatus   string  `json:"last_run_status"`    // "" if never run
	LastSuccessAt   *string `json:"last_success_at"`    // RFC3339, null if never succeeded
	LastSuccessAgeS *int64  `json:"last_success_age_s"` // null if never succeeded
	Stale           bool    `json:"stale"`
}

// scheduleStale maps a db.ScheduleFreshness to the policy struct and applies
// schedulespec.IsStale, resolving the per-schedule timezone against def. Shared
// by the status endpoint and the fleet-health banner.
func scheduleStale(fr db.ScheduleFreshness, def *time.Location, now time.Time) bool {
	return schedulespec.IsStale(schedulespec.Freshness{
		Enabled:        fr.Enabled,
		CronExpr:       fr.CronExpr,
		CreatedAt:      fr.CreatedAt,
		TimeoutSeconds: fr.TimeoutSeconds,
		LastRunStatus:  fr.LastRunStatus,
		LastRunAt:      fr.LastRunAt,
		LastSuccessAt:  fr.LastSuccessAt,
	}, fr.EffectiveLocation(def), now)
}

// handleFleetScheduleStatus returns per-schedule freshness across the fleet:
// last run + status, last success + age, and a cron-aware stale flag. Admin
// only and side-effect free. ?slug=<slug> filters to one app.
func (s *Server) handleFleetScheduleStatus(w http.ResponseWriter, r *http.Request) {
	if _, ok := requireAdmin(w, r); !ok {
		return
	}
	rows, err := s.store.ScheduleFreshness()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal server error")
		return
	}
	filter := r.URL.Query().Get("slug")
	def := s.cfg.Scheduler.Location
	now := time.Now()
	out := make([]scheduleStatusItem, 0, len(rows))
	for _, fr := range rows {
		if filter != "" && fr.Slug != filter {
			continue
		}
		item := scheduleStatusItem{
			Slug:          fr.Slug,
			Schedule:      fr.Name,
			Enabled:       fr.Enabled,
			LastRunStatus: fr.LastRunStatus,
			Stale:         scheduleStale(fr, def, now),
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
	writeJSON(w, http.StatusOK, out)
}
