package api

import (
	"net/http"

	"github.com/rvben/shinyhub/internal/auth"
)

// handleListAuditEvents returns the audit log. Admin only.
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
	writeJSON(w, http.StatusOK, events)
}
