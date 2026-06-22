package api

import (
	"net"
	"net/http"
	"strings"

	"github.com/rvben/shinyhub/internal/proxytrust"
)

// buildControlPlaneCSP assembles the Content-Security-Policy applied to
// control-plane responses (dashboard SPA, static assets, JSON API). It allows
// 'self' for everything by default, the Google Fonts hosts the dashboard loads
// its webfont from, and data: images (CSS/inline icons).
//
// There is no 'unsafe-inline': the SPA shell and assets are external files, and
// the only inline blocks (the branding <script>/<style> injected by
// ui.RenderIndex when branding is active) are allowed by their exact SHA-256
// hash, passed in as scriptSources/styleSources. Both are empty when branding is
// inactive, in which case no inline is ever served. Hashing rather than nonces
// keeps the shell cacheable.
//
// frame-ancestors 'self', base-uri 'self', and form-action 'self' are the
// defensive additions: they block cross-origin framing (clickjacking) and
// limit where the page can post or rebase to.
func buildControlPlaneCSP(scriptSources, styleSources []string) string {
	scriptSrc := append([]string{"'self'"}, scriptSources...)
	styleSrc := append([]string{"'self'"}, styleSources...)
	styleSrc = append(styleSrc, "https://fonts.googleapis.com")
	return "default-src 'self'; " +
		"img-src 'self' data:; " +
		"font-src 'self' https://fonts.gstatic.com; " +
		"style-src " + strings.Join(styleSrc, " ") + "; " +
		"script-src " + strings.Join(scriptSrc, " ") + "; " +
		"connect-src 'self'; " +
		"frame-ancestors 'self'; " +
		"base-uri 'self'; " +
		"form-action 'self'"
}

// LandingPageCSP is the policy for an operator-configured custom landing page
// (branding.landing_page). That file is operator-supplied, trusted HTML that may
// use inline scripts/styles, so - unlike the strict, inline-free SPA policy - it
// permits 'unsafe-inline' (the pre-hash behavior, reused via the same builder).
// The landing handler sets it on that one response only; the SPA shell, assets,
// and API keep the strict policy.
func LandingPageCSP() string {
	return buildControlPlaneCSP([]string{"'unsafe-inline'"}, []string{"'unsafe-inline'"})
}

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
// scriptSources/styleSources are the CSP hash allowances for the active
// branding inline blocks (ui.CSPInlineSources); both empty when branding is off.
func SecurityHeaders(trustedNets []*net.IPNet, scriptSources, styleSources []string, next http.Handler) http.Handler {
	csp := buildControlPlaneCSP(scriptSources, styleSources)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/app/") {
			h := w.Header()
			h.Set("X-Content-Type-Options", "nosniff")
			h.Set("X-Frame-Options", "SAMEORIGIN")
			h.Set("Referrer-Policy", "same-origin")
			h.Set("Permissions-Policy", controlPlanePermissionsPolicy)
			h.Set("Content-Security-Policy", csp)
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
