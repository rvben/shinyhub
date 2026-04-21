package api

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"strings"

	"github.com/rvben/shinyhub/internal/auth"
	"github.com/rvben/shinyhub/internal/db"
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
		IPAddress:    s.clientIP(r),
	})
}

// clientIP is a Server method that returns the best-effort client IP.
// X-Forwarded-For is only trusted when the direct peer (RemoteAddr) is within
// a configured trusted proxy CIDR, preventing clients from spoofing the header.
func (s *Server) clientIP(r *http.Request) string {
	peerHost, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		peerHost = r.RemoteAddr
	}
	peerIP := net.ParseIP(peerHost)
	xff := r.Header.Get("X-Forwarded-For")
	if peerIP != nil && xff != "" {
		for _, n := range s.cfg.TrustedProxyNets {
			if n.Contains(peerIP) {
				if idx := strings.Index(xff, ","); idx >= 0 {
					return strings.TrimSpace(xff[:idx])
				}
				return strings.TrimSpace(xff)
			}
		}
	}
	return peerHost
}
