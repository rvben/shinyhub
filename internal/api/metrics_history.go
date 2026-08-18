package api

import (
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/rvben/shinyhub/internal/auth"
	"github.com/rvben/shinyhub/internal/history"
)

// metricsHistoryResponse is the columnar payload served at
// GET /api/apps/{slug}/metrics/history. Parallel arrays keep the JSON compact and
// map directly onto the dashboard's sparkline renderer.
type metricsHistoryResponse struct {
	WindowSeconds   int64          `json:"window_seconds"`
	IntervalSeconds int64          `json:"interval_seconds"`
	Series          history.Series `json:"series"`
}

type batchMetricsHistoryResponse struct {
	WindowSeconds   int64                     `json:"window_seconds"`
	IntervalSeconds int64                     `json:"interval_seconds"`
	GeneratedAt     time.Time                 `json:"generated_at"`
	History         map[string]history.Series `json:"history"`
}

const overviewHistoryWindow = 15 * time.Minute

// handleMetricsHistory returns the in-memory resource history for an app. It is
// gated by the same view check as the live metrics endpoint. When history
// collection is disabled (no store wired) it returns an empty series with zero
// window/interval so the UI can hide the Trends card.
func (s *Server) handleMetricsHistory(w http.ResponseWriter, r *http.Request) {
	slug := chi.URLParam(r, "slug")
	if _, _, ok := s.requireViewApp(w, r, slug); !ok {
		return
	}
	if s.history == nil {
		writeJSON(w, http.StatusOK, metricsHistoryResponse{Series: history.EmptySeries()})
		return
	}
	writeJSON(w, http.StatusOK, metricsHistoryResponse{
		WindowSeconds:   s.history.WindowSeconds(),
		IntervalSeconds: s.history.IntervalSeconds(),
		Series:          s.history.Series(slug, time.Now().Unix()),
	})
}

// handleBatchMetricsHistory returns the same bounded in-memory series as the
// per-app endpoint for every requested, viewable app. Overview uses one request
// to add compact 15-minute direction labels without an N+1 fleet poll.
func (s *Server) handleBatchMetricsHistory(w http.ResponseWriter, r *http.Request) {
	u := auth.UserFromContext(r.Context())
	if u == nil {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	apps, err := s.metricAppsForUser(u, r.URL.Query().Get("slugs"))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal server error")
		return
	}

	now := time.Now().UTC()
	window := overviewHistoryWindow
	resp := batchMetricsHistoryResponse{
		GeneratedAt: now,
		History:     make(map[string]history.Series, len(apps)),
	}
	if s.history != nil {
		if retained := time.Duration(s.history.WindowSeconds()) * time.Second; retained < window {
			window = retained
		}
		resp.WindowSeconds = int64(window.Seconds())
		resp.IntervalSeconds = s.history.IntervalSeconds()
	}
	for _, app := range apps {
		series := history.EmptySeries()
		if s.history != nil {
			series = s.history.SeriesWindow(app.Slug, now.Unix(), window)
		}
		resp.History[app.Slug] = series
	}
	writeJSON(w, http.StatusOK, resp)
}
