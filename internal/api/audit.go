package api

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/rvben/shinyhub/internal/auth"
	"github.com/rvben/shinyhub/internal/db"
)

// canReadAudit reports whether u may read the audit log: admins always, and
// operators when the operator opts in via auth.operator_audit_access
// (operators manage every app, so seeing who changed what is a natural fit,
// but it stays off by default because audit rows include user management).
func (s *Server) canReadAudit(u *auth.ContextUser) bool {
	if u == nil {
		return false
	}
	// Audit rows include global user, project, and app details and cannot be
	// losslessly filtered by an app allowlist. Do not let a scoped automation
	// credential turn the global log into an out-of-scope discovery surface.
	if u.IsServiceAccount() && u.HasAppScopeRestriction() {
		return false
	}
	return u.Role == "admin" || (u.Role == "operator" && s.cfg.Auth.OperatorAuditAccess)
}

// auditDateLayout is the calendar-date form the date range accepts. Audit rows
// are stamped in UTC and the page renders them in UTC, so a date names a UTC
// day and needs no zone from the caller.
const auditDateLayout = "2006-01-02"

// parseAuditDateRange turns two optional YYYY-MM-DD bounds into an inclusive
// UTC instant range. "until" covers the whole named day: an operator asking for
// events until the 6th means through the end of the 6th, and a bound at
// midnight would silently drop everything that happened during it.
//
// An unparseable or inverted range is rejected rather than ignored. Dropping a
// filter the caller asked for returns more rows than requested, which reads as
// "those events are in range" and is the wrong answer to give someone reading
// an audit log.
func parseAuditDateRange(rawSince, rawUntil string) (since, until time.Time, err error) {
	rawSince, rawUntil = strings.TrimSpace(rawSince), strings.TrimSpace(rawUntil)
	if rawSince != "" {
		since, err = time.ParseInLocation(auditDateLayout, rawSince, time.UTC)
		if err != nil {
			return time.Time{}, time.Time{}, fmt.Errorf("invalid audit since date, expected YYYY-MM-DD")
		}
	}
	if rawUntil != "" {
		until, err = time.ParseInLocation(auditDateLayout, rawUntil, time.UTC)
		if err != nil {
			return time.Time{}, time.Time{}, fmt.Errorf("invalid audit until date, expected YYYY-MM-DD")
		}
		until = until.Add(24*time.Hour - time.Second)
	}
	if !since.IsZero() && !until.IsZero() && until.Before(since) {
		return time.Time{}, time.Time{}, fmt.Errorf("audit until date is before since date")
	}
	return since, until, nil
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
	filter := db.AuditEventFilter{
		Action: strings.TrimSpace(r.URL.Query().Get("action")),
		RunID:  strings.TrimSpace(r.URL.Query().Get("run")),
	}
	if len(filter.RunID) > 128 {
		writeError(w, http.StatusBadRequest, "invalid audit run filter")
		return
	}
	if rawEvent := strings.TrimSpace(r.URL.Query().Get("event")); rawEvent != "" {
		eventID, err := strconv.ParseInt(rawEvent, 10, 64)
		if err != nil || eventID <= 0 {
			writeError(w, http.StatusBadRequest, "invalid audit event filter")
			return
		}
		filter.EventID = eventID
	}
	since, until, err := parseAuditDateRange(r.URL.Query().Get("since"), r.URL.Query().Get("until"))
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	filter.Since, filter.Until = since, until
	events, err := s.store.ListAuditEventsFiltered(filter, limit, offset)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal server error")
		return
	}
	total, err := s.store.CountAuditEventsFiltered(filter)
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
