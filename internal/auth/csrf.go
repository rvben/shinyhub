package auth

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"net"
	"net/http"
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
//     is present without a csrf_token cookie, the middleware mints one.
//   - Unsafe methods (POST/PUT/PATCH/DELETE) require the csrf_token cookie
//     value to match the X-CSRF-Token header.
//   - Requests with an Authorization header (Bearer/Token) bypass CSRF checks:
//     token auth is not vulnerable to CSRF.
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

// ensureCSRFCookie sets a csrf_token cookie on the response when the request
// has a session cookie but no csrf_token cookie yet.
func ensureCSRFCookie(w http.ResponseWriter, r *http.Request, trustedNets []*net.IPNet) {
	if _, err := r.Cookie(SessionCookieName); err != nil {
		return
	}
	if c, err := r.Cookie(CSRFCookieName); err == nil && c.Value != "" {
		return
	}
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name:     CSRFCookieName,
		Value:    hex.EncodeToString(b[:]),
		Path:     "/",
		HttpOnly: false,
		SameSite: http.SameSiteLaxMode,
		Secure:   cookieSecure(r, trustedNets),
		MaxAge:   int(jwtExpiry.Seconds()),
	})
}
