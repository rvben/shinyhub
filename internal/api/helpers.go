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
