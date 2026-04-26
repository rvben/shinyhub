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
		IPAddress:    s.ClientIP(r),
	})
}

// ClientIP returns the best-effort client IP. X-Forwarded-For is only trusted
// when the direct peer (RemoteAddr) is within a configured trusted proxy CIDR,
// preventing clients from spoofing the header. Exposed so other subsystems
// (e.g. the reverse proxy access log) can share the same trust policy without
// duplicating it.
func (s *Server) ClientIP(r *http.Request) string {
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

// effectiveHost returns the public host the user reached us on, preferring
// X-Forwarded-Host when the direct peer is a configured trusted proxy. Plain
// r.Host is wrong behind a reverse proxy: the inbound TCP connection terminates
// at the proxy, so r.Host is whatever the proxy addressed us at (often
// 127.0.0.1:<port> or a Unix socket alias) — not the public hostname the
// browser sees. Comparing such an r.Host against a browser-supplied Origin
// header would reject every same-origin request in production.
//
// X-Forwarded-Host is only trusted when the direct peer is in
// TrustedProxyNets, mirroring ClientIP's policy: an attacker who can reach us
// directly cannot fake the header to bypass the same-origin check.
func (s *Server) effectiveHost(r *http.Request) string {
	if s.peerIsTrustedProxy(r) {
		if v := r.Header.Get("X-Forwarded-Host"); v != "" {
			return strings.TrimSpace(strings.SplitN(v, ",", 2)[0])
		}
	}
	return r.Host
}

// peerIsTrustedProxy reports whether the direct TCP peer (RemoteAddr) is
// within a configured trusted proxy CIDR. Used as the gate for honouring
// X-Forwarded-* headers.
func (s *Server) peerIsTrustedProxy(r *http.Request) bool {
	peerHost, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		peerHost = r.RemoteAddr
	}
	peerIP := net.ParseIP(peerHost)
	if peerIP == nil {
		return false
	}
	for _, n := range s.cfg.TrustedProxyNets {
		if n.Contains(peerIP) {
			return true
		}
	}
	return false
}
