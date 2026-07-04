// Package proxytrust centralises the trust gate that controls whether
// X-Forwarded-* request headers are honoured.
//
// Every X-Forwarded-* header is attacker-controlled by default: any client
// that can open a TCP connection to us can put whatever they want in there.
// They become trustworthy only when the direct TCP peer (r.RemoteAddr) is
// known to be a reverse proxy that strips client-supplied copies before
// forwarding the request. We express that "known" set as a list of CIDRs
// (cfg.TrustedProxyNets) and gate every X-Forwarded-* lookup on it.
//
// All callers — the access middleware, the auth cookie helpers, the API
// server's ClientIP — go through this package so the trust policy is defined
// in exactly one place.
package proxytrust

import (
	"net"
	"net/http"
	"strings"
)

// PeerIsTrusted reports whether the request's direct TCP peer (RemoteAddr)
// is inside any of the supplied CIDRs. An empty or nil nets slice means no
// peer is trusted — the production-safe default.
func PeerIsTrusted(r *http.Request, nets []*net.IPNet) bool {
	if r == nil || len(nets) == 0 {
		return false
	}
	peerHost, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		peerHost = r.RemoteAddr
	}
	peerIP := net.ParseIP(peerHost)
	if peerIP == nil {
		return false
	}
	for _, n := range nets {
		if n.Contains(peerIP) {
			return true
		}
	}
	return false
}

// Host returns the public host the user reached us on. Behind a reverse
// proxy r.Host is the upstream socket address (commonly 127.0.0.1:<port> or
// a Unix socket alias) — never the public hostname the browser used.
// X-Forwarded-Host is honoured only when the direct peer is in nets.
func Host(r *http.Request, nets []*net.IPNet) string {
	if r == nil {
		return ""
	}
	if PeerIsTrusted(r, nets) {
		if v := r.Header.Get("X-Forwarded-Host"); v != "" {
			return lastForwardedValue(v)
		}
	}
	return r.Host
}

// Scheme returns the public scheme ("http" or "https"). r.TLS is the
// authoritative signal for direct connections; X-Forwarded-Proto is honoured
// only when the direct peer is in nets, otherwise an attacker connecting
// directly over plain HTTP could spoof "https" and influence Secure-cookie
// decisions or rendered URLs.
func Scheme(r *http.Request, nets []*net.IPNet) string {
	if r == nil {
		return "http"
	}
	if r.TLS != nil {
		return "https"
	}
	if PeerIsTrusted(r, nets) {
		if v := r.Header.Get("X-Forwarded-Proto"); v != "" {
			return lastForwardedValue(v)
		}
	}
	return "http"
}

// ClientIP returns the best-effort client IP. X-Forwarded-For is honoured
// only when the direct peer is in nets, preventing clients from spoofing
// the header.
func ClientIP(r *http.Request, nets []*net.IPNet) string {
	if r == nil {
		return ""
	}
	peerHost, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		peerHost = r.RemoteAddr
	}
	if !PeerIsTrusted(r, nets) {
		return peerHost
	}
	xff := r.Header.Get("X-Forwarded-For")
	if xff == "" {
		return peerHost
	}
	// Reverse proxies APPEND to X-Forwarded-For, so entries added by trusted
	// proxies are on the RIGHT and any client-supplied (spoofable) value is on the
	// LEFT. Walk right-to-left, skipping addresses that are themselves trusted
	// proxies; the first untrusted address is the real client as observed by the
	// innermost trusted proxy. Taking the leftmost entry (the previous behaviour)
	// let a client forge its own IP by prepending X-Forwarded-For.
	parts := strings.Split(xff, ",")
	for i := len(parts) - 1; i >= 0; i-- {
		ip := strings.TrimSpace(parts[i])
		if ip == "" {
			continue
		}
		parsed := net.ParseIP(ip)
		if parsed == nil {
			continue
		}
		if ipInNets(parsed, nets) {
			continue // a trusted proxy hop; keep walking left
		}
		return ip
	}
	// Every entry was a trusted proxy (or empty/unparseable): fall back to the peer.
	return peerHost
}

// lastForwardedValue returns the rightmost comma-separated entry of a forwarded
// header. Proxies append, so the rightmost value is the one set by the closest
// (most trusted) hop; a client-supplied value would sit to its left.
func lastForwardedValue(v string) string {
	parts := strings.Split(v, ",")
	return strings.TrimSpace(parts[len(parts)-1])
}

// ipInNets reports whether ip is inside any of the supplied CIDRs.
func ipInNets(ip net.IP, nets []*net.IPNet) bool {
	for _, n := range nets {
		if n.Contains(ip) {
			return true
		}
	}
	return false
}
