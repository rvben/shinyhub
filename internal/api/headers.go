package api

import (
	"net"
	"net/http"
	"strings"

	"github.com/rvben/shinyhub/internal/proxytrust"
)

// controlPlaneCSP is the Content-Security-Policy applied to control-plane
// responses (dashboard SPA, static assets, JSON API). It allows:
//   - 'self' for everything by default,
//   - inline scripts/styles ('unsafe-inline') because the SPA shell, the proxy
//     loading page, the access-denied page, and the branding injection all use
//     inline <script>/<style>,
//   - the Google Fonts hosts the dashboard loads its webfont from,
//   - data: images (CSS/inline icons).
//
// frame-ancestors 'self', base-uri 'self', and form-action 'self' are the
// defensive additions: they block cross-origin framing (clickjacking) and
// limit where the page can post or rebase to.
const controlPlaneCSP = "default-src 'self'; " +
	"img-src 'self' data:; " +
	"font-src 'self' https://fonts.gstatic.com; " +
	"style-src 'self' 'unsafe-inline' https://fonts.googleapis.com; " +
	"script-src 'self' 'unsafe-inline'; " +
	"connect-src 'self'; " +
	"frame-ancestors 'self'; " +
	"base-uri 'self'; " +
	"form-action 'self'"

// controlPlanePermissionsPolicy disables powerful browser features the
// dashboard never uses, shrinking the surface any injected content could abuse.
const controlPlanePermissionsPolicy = "camera=(), microphone=(), geolocation=(), payment=()"

// hstsValue pins HTTPS for two years including subdomains. Sent only on HTTPS
// responses (browsers ignore HSTS received over plain HTTP).
const hstsValue = "max-age=63072000; includeSubDomains"

// SecurityHeaders sets defensive response headers on control-plane responses.
// Proxied app responses under /app/ are intentionally left untouched: they are
// separate, operator-supplied content that may legitimately be embedded in an
// iframe and run their own inline scripts/styles, so imposing the control-plane
// CSP/framing policy on them would break working apps. trustedNets is the
// configured trusted-proxy CIDR list (cfg.TrustedProxyNets), used to decide the
// request scheme for HSTS the same way session cookies decide their Secure flag.
func SecurityHeaders(trustedNets []*net.IPNet, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/app/") {
			h := w.Header()
			h.Set("X-Content-Type-Options", "nosniff")
			h.Set("X-Frame-Options", "SAMEORIGIN")
			h.Set("Referrer-Policy", "same-origin")
			h.Set("Permissions-Policy", controlPlanePermissionsPolicy)
			h.Set("Content-Security-Policy", controlPlaneCSP)
			// HSTS only over HTTPS. X-Forwarded-Proto is honoured only from a
			// trusted proxy, so an attacker on a plain-HTTP connection cannot
			// induce an HSTS pin for the host.
			if proxytrust.Scheme(r, trustedNets) == "https" {
				h.Set("Strict-Transport-Security", hstsValue)
			}
		}
		next.ServeHTTP(w, r)
	})
}
