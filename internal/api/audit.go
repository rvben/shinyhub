package api

import (
	"net/http"

	"github.com/rvben/shinyhub/internal/auth"
)

// canReadAudit reports whether u may read the audit log: admins always, and
// operators when the operator opts in via auth.operator_audit_access
// (operators manage every app, so seeing who changed what is a natural fit,
// but it stays off by default because audit rows include user management).
func (s *Server) canReadAudit(u *auth.ContextUser) bool {
	if u == nil {
		return false
	}
	return u.Role == "admin" || (u.Role == "operator" && s.cfg.Auth.OperatorAuditAccess)
}

// handleListAuditEvents returns the audit log. Admin only, unless
// auth.operator_audit_access extends it to operators.
//
// Response envelope: {"events": [...], "total": N, "has_more": bool}.
// The total + has_more fields let the UI enable/disable Next/Prev without a
// per-row guessing heuristic.
func (s *Server) handleListAuditEvents(w http.ResponseWriter, r *http.Request) {
	u := auth.UserFromContext(r.Context())
	if !s.canReadAudit(u) {
		writeError(w, http.StatusForbidden, "admin only")
		return
	}
	limit, offset := parsePagination(r)
	action := r.URL.Query().Get("action")
	events, err := s.store.ListAuditEvents(action, limit, offset)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal server error")
		return
	}
	total, err := s.store.CountAuditEvents(action)
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
