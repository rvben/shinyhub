package api

import (
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/rvben/shinyhub/internal/tracing"
)

// tracesResponse is the JSON shape returned by GET /api/apps/{slug}/traces.
type tracesResponse struct {
	Enabled           bool            `json:"enabled"`
	TraceLinkTemplate string          `json:"trace_link_template,omitempty"`
	Spans             []tracing.Span  `json:"spans"`
}

// handleTraces returns the ring buffer of recent slow/error proxy spans for
// one app, plus the operator's trace_link_template so the UI can deep-link
// each trace_id into the backend that holds the full trace tree.
//
// Auth matches /metrics: any caller authorized to view the app (owner, member,
// admin/operator, or anyone if visibility is public) may read traces. We
// deliberately do not require manage rights here — observability data is read-
// only and surfaces the same paths that already appear in /logs and the access
// log, both of which use the view-app auth boundary.
func (s *Server) handleTraces(w http.ResponseWriter, r *http.Request) {
	slug := chi.URLParam(r, "slug")
	if _, _, ok := s.requireViewApp(w, r, slug); !ok {
		return
	}

	resp := tracesResponse{
		Enabled: s.cfg.Tracing.Enabled,
		Spans:   []tracing.Span{},
	}
	if strings.TrimSpace(s.cfg.Tracing.TraceLinkTemplate) != "" {
		resp.TraceLinkTemplate = s.cfg.Tracing.TraceLinkTemplate
	}
	if s.traceBuffer != nil {
		if spans := s.traceBuffer.Snapshot(slug); len(spans) > 0 {
			resp.Spans = spans
		}
	}
	writeJSON(w, http.StatusOK, resp)
}
