package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"

	"github.com/rvben/shinyhub/internal/auth"
	"github.com/rvben/shinyhub/internal/db"
	"github.com/rvben/shinyhub/internal/proxytrust"
)

// writeJSON writes v as JSON with the given HTTP status code. Encode errors
// are logged to stderr; they cannot be reported to the client because the
// header has already been sent.
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		fmt.Fprintf(os.Stderr, "writeJSON encode: %v\n", err)
	}
}

// writeError writes a JSON error response: {"error": "msg"}.
func writeError(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	fmt.Fprintf(w, `{"error":%q}`+"\n", msg)
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
