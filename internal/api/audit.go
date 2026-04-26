package api

import (
	"net/http"

	"github.com/rvben/shinyhub/internal/auth"
)

// handleListAuditEvents returns the audit log. Admin only.
//
// Response envelope: {"events": [...], "total": N, "has_more": bool}.
// The total + has_more fields let the UI enable/disable Next/Prev without a
// per-row guessing heuristic.
func (s *Server) handleListAuditEvents(w http.ResponseWriter, r *http.Request) {
	u := auth.UserFromContext(r.Context())
	if u == nil || u.Role != "admin" {
		writeError(w, http.StatusForbidden, "admin only")
		return
	}
	limit, offset := parsePagination(r)
	events, err := s.store.ListAuditEvents(limit, offset)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal server error")
		return
	}
	total, err := s.store.CountAuditEvents()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal server error")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"events":   events,
		"total":    total,
		"has_more": int64(offset+len(events)) < total,
	})
}
