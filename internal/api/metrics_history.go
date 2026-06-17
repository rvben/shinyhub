package api

import (
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
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
