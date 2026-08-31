package api

import (
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/rvben/shinyhub/internal/config"
	"github.com/rvben/shinyhub/internal/db"
)

type usageResponse struct {
	Enabled                bool                    `json:"enabled"`
	WindowDays             int                     `json:"window_days"`
	RawRetentionDays       int                     `json:"raw_retention_days"`
	AggregateRetentionDays int                     `json:"aggregate_retention_days"`
	IdentityMode           string                  `json:"identity_mode"`
	PolicySource           string                  `json:"policy_source"`
	Capabilities           usageCapabilities       `json:"capabilities"`
	DetailAvailableFrom    *time.Time              `json:"detail_available_from,omitempty"`
	GeneratedAt            time.Time               `json:"generated_at"`
	IdentityDetail         bool                    `json:"identity_detail"`
	Definition             string                  `json:"definition"`
	Summary                db.UsageSummary         `json:"summary"`
	Daily                  []db.UsageDay           `json:"daily"`
	Viewers                []db.UsageViewer        `json:"viewers"`
	RecentSessions         []db.UsageRecentSession `json:"recent_sessions"`
}

type usageCapabilities struct {
	UniqueViewers  bool `json:"unique_viewers"`
	ViewerDetail   bool `json:"viewer_detail"`
	RecentSessions bool `json:"recent_sessions"`
}

// handleAppUsage returns durable WebSocket-session analytics to app managers.
// Identifiable viewer and session rows are administrator-only; owners and app
// managers receive the same operationally useful aggregates without a staff
// activity ledger.
func (s *Server) handleAppUsage(w http.ResponseWriter, r *http.Request) {
	// Administrator responses may contain names and precise access times. Never
	// let browser or intermediary caches retain either identity detail or the
	// aggregates returned from the same endpoint.
	w.Header().Set("Cache-Control", "no-store")
	slug := chi.URLParam(r, "slug")
	app, user, ok := s.requireViewApp(w, r, slug)
	if !ok {
		return
	}
	if !s.effectiveCanManageApp(user, app) {
		writeError(w, http.StatusForbidden, "forbidden")
		return
	}

	days := 30
	if raw := r.URL.Query().Get("days"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || (parsed != 7 && parsed != 30 && parsed != 90 && parsed != 365) {
			writeError(w, http.StatusBadRequest, "days must be one of 7, 30, 90, or 365")
			return
		}
		days = parsed
	}
	if retained := s.cfg.Usage.AggregateRetentionDays; retained > 0 && days > retained {
		days = retained
	}

	appOverride := ""
	policySource := "hub"
	if app.UsageIdentityMode != nil {
		appOverride = *app.UsageIdentityMode
		policySource = "app"
	}
	identityMode, appCollectionEnabled := config.EffectiveUsageIdentityMode(s.cfg.Usage.IdentityMode, appOverride)
	collectionEnabled := s.cfg.Usage.Enabled && appCollectionEnabled
	identityDetail := identityMode == config.UsageIdentityIdentified &&
		user != nil && user.Role == "admin" && !user.IsServiceAccount()
	uniqueAvailable := identityMode != config.UsageIdentityUnattributed
	var detailFrom *time.Time
	if s.cfg.Usage.RawRetentionDays > 0 {
		v := time.Now().UTC().AddDate(0, 0, -s.cfg.Usage.RawRetentionDays)
		detailFrom = &v
	}
	resp := usageResponse{
		Enabled:                collectionEnabled,
		WindowDays:             days,
		RawRetentionDays:       s.cfg.Usage.RawRetentionDays,
		AggregateRetentionDays: s.cfg.Usage.AggregateRetentionDays,
		IdentityMode:           string(identityMode),
		PolicySource:           policySource,
		Capabilities: usageCapabilities{
			UniqueViewers:  uniqueAvailable,
			ViewerDetail:   identityDetail,
			RecentSessions: identityDetail,
		},
		DetailAvailableFrom: detailFrom,
		GeneratedAt:         time.Now().UTC(),
		IdentityDetail:      identityDetail,
		Definition:          "A session starts after a successful app WebSocket connection.",
		Daily:               []db.UsageDay{},
		Viewers:             []db.UsageViewer{},
		RecentSessions:      []db.UsageRecentSession{},
	}
	report, err := s.store.AppUsageReport(r.Context(), app.ID, time.Duration(days)*24*time.Hour, string(identityMode), identityDetail)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal server error")
		return
	}
	resp.Summary = report.Summary
	resp.Capabilities.UniqueViewers = report.Summary.UniqueViewers != nil
	resp.Daily = report.Daily
	resp.Viewers = report.Viewers
	resp.RecentSessions = report.Recent
	writeJSON(w, http.StatusOK, resp)
}
