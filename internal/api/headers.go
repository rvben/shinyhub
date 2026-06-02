package api

import (
	"net/http"
	"strings"
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

// SecurityHeaders sets defensive response headers on control-plane responses.
// Proxied app responses under /app/ are intentionally left untouched: they are
// separate, operator-supplied content that may legitimately be embedded in an
// iframe and run their own inline scripts/styles, so imposing the control-plane
// CSP/framing policy on them would break working apps.
func SecurityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/app/") {
			h := w.Header()
			h.Set("X-Content-Type-Options", "nosniff")
			h.Set("X-Frame-Options", "SAMEORIGIN")
			h.Set("Referrer-Policy", "same-origin")
			h.Set("Content-Security-Policy", controlPlaneCSP)
		}
		next.ServeHTTP(w, r)
	})
}
