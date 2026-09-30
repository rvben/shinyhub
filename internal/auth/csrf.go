package auth

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"math"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// CSRFCookieName is the cookie that holds the CSRF token. Not HttpOnly so
// the browser JS can read it and echo the value in X-CSRF-Token.
const CSRFCookieName = "csrf_token"

// CSRFHeaderName is the header the frontend sends containing the CSRF token.
const CSRFHeaderName = "X-CSRF-Token"

var csrfSafeMethods = map[string]struct{}{
	http.MethodGet:     {},
	http.MethodHead:    {},
	http.MethodOptions: {},
}

// CSRFMiddleware implements the double-submit-cookie CSRF pattern.
//
//   - Safe methods (GET/HEAD/OPTIONS) pass through and, when a session cookie
//     is present (or the request is authenticated via forward-auth) without a
//     csrf_token cookie, the middleware mints one.
//   - Unsafe methods (POST/PUT/PATCH/DELETE) require the csrf_token cookie
//     value to match the X-CSRF-Token header.
//   - Requests with an Authorization header (Bearer/Token) bypass CSRF checks:
//     token auth is not vulnerable to CSRF.
//   - Forward-auth-authenticated requests (no session cookie, user injected by
//     ForwardAuthMiddleware) receive a csrf_token on safe methods and must
//     present the double-submit token on unsafe methods, giving them the same
//     CSRF protection as session users.
//
// trustedNets is the configured list of trusted-proxy CIDRs; it gates whether
// X-Forwarded-Proto is honoured when deciding the Secure flag on the minted
// CSRF cookie. Pass cfg.TrustedProxyNets.
func CSRFMiddleware(trustedNets []*net.IPNet) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get("Authorization") != "" {
				next.ServeHTTP(w, r)
				return
			}

			if _, safe := csrfSafeMethods[r.Method]; safe {
				ensureCSRFCookie(w, r, trustedNets)
				next.ServeHTTP(w, r)
				return
			}

			// Defense in depth against a proxied app riding the session: apps are
			// served same-origin under /app/<slug>/, so a compromised app's JS can
			// read the (JS-readable) csrf_token cookie and issue a same-origin
			// mutating request. Reject any mutation whose Referer is under /app/.
			// The dashboard's own fetches carry a dashboard-route Referer (never
			// /app/); token-authed API clients bypass CSRF above. A missing Referer
			// fails closed: otherwise app JavaScript can set referrerPolicy=no-referrer
			// and bypass this path check after reading the double-submit token. For
			// hard isolation of untrusted apps, deploy them on a separate origin
			// (server.app_origin).
			if r.Header.Get("Referer") == "" {
				http.Error(w, "csrf: missing referer", http.StatusForbidden)
				return
			}
			if refererUnderAppPath(r.Header.Get("Referer")) {
				http.Error(w, "csrf: mutation originated from a proxied app", http.StatusForbidden)
				return
			}

			cookie, err := r.Cookie(CSRFCookieName)
			if err != nil || cookie.Value == "" {
				http.Error(w, "csrf: missing token", http.StatusForbidden)
				return
			}
			header := r.Header.Get(CSRFHeaderName)
			if header == "" || subtle.ConstantTimeCompare([]byte(cookie.Value), []byte(header)) != 1 {
				http.Error(w, "csrf: token mismatch", http.StatusForbidden)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// refererUnderAppPath reports whether the Referer URL's path is under /app/,
// i.e. the request was initiated from a proxied app page. Empty Referer values
// are rejected by the middleware before this helper is called.
func refererUnderAppPath(referer string) bool {
	if referer == "" {
		return false
	}
	u, err := url.Parse(referer)
	if err != nil {
		return false
	}
	return u.Path == "/app" || strings.HasPrefix(u.Path, "/app/")
}

// ensureCSRFCookie sets a csrf_token cookie on the response when the request
// has a session cookie or is authenticated via forward-auth, and no csrf_token
// cookie is present yet. Forward-auth users have no session cookie, so checking
// for UserFromContext covers both authentication paths without bypassing the
// double-submit check for unsafe methods.
func ensureCSRFCookie(w http.ResponseWriter, r *http.Request, trustedNets []*net.IPNet) {
	// Mint a token when the request carries a ShinyHub session cookie or is
	// authenticated via forward-auth (a trusted-proxy header). Forward-auth users
	// have no session cookie, so without this branch they could never obtain a
	// csrf_token and every mutating request would be rejected. Minting here gives
	// them the same double-submit-cookie protection as session users, rather than
	// bypassing CSRF (which would push the whole defense onto the proxy cookie's
	// SameSite configuration).
	_, sessErr := r.Cookie(SessionCookieName)
	if sessErr != nil && UserFromContext(r.Context()) == nil {
		return
	}
	if c, err := r.Cookie(CSRFCookieName); err == nil && c.Value != "" {
		return
	}
	setCSRFCookieUntil(w, r, time.Now().Add(jwtExpiry), trustedNets)
}

// Renewal preserves the double-submit value so concurrent tabs and requests
// remain valid, while extending its cookie alongside the HttpOnly session.
func setCSRFCookieUntil(w http.ResponseWriter, r *http.Request, expiresAt time.Time, trustedNets []*net.IPNet) {
	value := ""
	if c, err := r.Cookie(CSRFCookieName); err == nil {
		value = c.Value
	}
	// Safe-method middleware may already have minted this cookie on the same
	// response. Reuse its value and replace its expiry rather than send two
	// different tokens for the same cookie name.
	if value == "" {
		response := &http.Response{Header: w.Header()}
		for _, c := range response.Cookies() {
			if c.Name == CSRFCookieName {
				value = c.Value
			}
		}
	}
	if value == "" {
		var b [32]byte
		if _, err := rand.Read(b[:]); err != nil {
			return
		}
		value = hex.EncodeToString(b[:])
	}
	maxAge := int(math.Ceil(time.Until(expiresAt).Seconds()))
	if maxAge <= 0 {
		maxAge = -1
	}
	var cookies []string
	for _, c := range w.Header().Values("Set-Cookie") {
		if !strings.HasPrefix(c, CSRFCookieName+"=") {
			cookies = append(cookies, c)
		}
	}
	w.Header().Del("Set-Cookie")
	for _, c := range cookies {
		w.Header().Add("Set-Cookie", c)
	}
	http.SetCookie(w, &http.Cookie{
		Name:     CSRFCookieName,
		Value:    value,
		Path:     "/",
		HttpOnly: false,
		SameSite: http.SameSiteLaxMode,
		Secure:   cookieSecure(r, trustedNets),
		MaxAge:   maxAge,
		Expires:  expiresAt,
	})
}
