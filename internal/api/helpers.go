package api

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"

	"github.com/rvben/shinyhub/internal/auth"
	"github.com/rvben/shinyhub/internal/db"
	"github.com/rvben/shinyhub/internal/deployfail"
	"github.com/rvben/shinyhub/internal/proxytrust"
)

// reqLog returns a slog.Logger tagged with the request's correlation ID so
// handler-side and async errors can be joined to the api_access log line for
// the same request.
func reqLog(r *http.Request) *slog.Logger {
	return slog.With("request_id", RequestIDFromContext(r.Context()))
}

// writeJSON writes v as JSON with the given HTTP status code. Encode errors are
// logged (the header has already been sent, so they cannot reach the client).
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		slog.Warn("write_json_encode_failed", "err", err)
	}
}

// writeError writes a JSON error response: {"error": "msg"}.
func writeError(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	fmt.Fprintf(w, `{"error":%q}`+"\n", msg)
}

// writeErrorWithKind writes a JSON error response that also carries a stable
// machine-readable failure classification: {"error": msg, "failure_kind": kind}.
// Used by the deploy failure path so a CLI can distinguish a readiness timeout
// from a crash from a build failure without parsing the human message.
func writeErrorWithKind(w http.ResponseWriter, status int, msg string, kind deployfail.Kind) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": msg, "failure_kind": string(kind)})
}

// writeList writes a paginated list response with the standard envelope
// {items, total, limit, offset}. total is the size of the full result set;
// items is the page selected by limit/offset, sliced in-memory so every list
// endpoint shares one shape regardless of whether the store paginates. An empty
// page always marshals as [] (never null). extra carries endpoint-specific
// envelope keys (e.g. data ls quota_mb/used_bytes) and never clobbers the
// standard fields.
func writeList[T any](w http.ResponseWriter, items []T, limit, offset int, extra map[string]any) {
	total := len(items)
	if offset < 0 {
		offset = 0
	}
	if limit < 0 {
		limit = 0
	}
	start := offset
	if start > total {
		start = total
	}
	end := total
	if limit > 0 && start+limit < end {
		end = start + limit
	}
	writeListPage(w, items[start:end], total, limit, offset, extra)
}

// writeListPage writes the standard list envelope for a page the caller has
// ALREADY selected (e.g. a store method that paginated at the DB layer), with an
// explicit total for the full result set. Used where fetching the whole set
// in-memory would be unbounded (e.g. schedule run history). An empty page always
// marshals as [] (never null); extra never clobbers the standard fields.
func writeListPage[T any](w http.ResponseWriter, page []T, total, limit, offset int, extra map[string]any) {
	if len(page) == 0 {
		page = []T{}
	}
	env := map[string]any{
		"items":  page,
		"total":  total,
		"limit":  limit,
		"offset": offset,
	}
	for k, v := range extra {
		switch k {
		case "items", "total", "limit", "offset":
			// Standard fields are authoritative; ignore any collision.
		default:
			env[k] = v
		}
	}
	writeJSON(w, http.StatusOK, env)
}

// audit records a mutating action with the authenticated user, request IP, and
// optional JSON detail blob. detail may be empty.
func (s *Server) audit(r *http.Request, action, resourceType, resourceID, detail string) {
	var uid *int64
	if u := auth.UserFromContext(r.Context()); u != nil {
		v := u.ID
		uid = &v
	}
	s.store.LogAuditEvent(db.AuditEventParams{
		UserID:       uid,
		Action:       action,
		ResourceType: resourceType,
		ResourceID:   resourceID,
		Detail:       detail,
		IPAddress:    s.ClientIP(r),
	})
}

// ClientIP returns the best-effort client IP, honouring X-Forwarded-For only
// when the direct peer is in the configured trusted-proxy CIDRs. Exposed so
// other subsystems (e.g. the reverse proxy access log) share the same trust
// policy without duplicating it.
func (s *Server) ClientIP(r *http.Request) string {
	return proxytrust.ClientIP(r, s.cfg.TrustedProxyNets)
}

// effectiveHost returns the public host the user reached us on, honouring
// X-Forwarded-Host only when the direct peer is in the configured
// trusted-proxy CIDRs. See proxytrust.Host for the rationale.
func (s *Server) effectiveHost(r *http.Request) string {
	return proxytrust.Host(r, s.cfg.TrustedProxyNets)
}
