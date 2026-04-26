package auth

import (
	"crypto/subtle"
	"net"
	"net/http"
	"time"
)

// OAuthStateCookieName is the cookie that binds an OAuth state nonce to the
// browser that initiated the login. The callback handler verifies that the
// `state` query param matches the cookie value before consuming the
// server-side nonce; this prevents a stolen state from being replayed in a
// different browser (login CSRF / authorization-code injection).
const OAuthStateCookieName = "shiny_oauth_state"

// oauthStateMaxAge bounds how long the binding cookie is valid. It mirrors
// the server-side state lifetime in the database (10 minutes); a longer
// cookie would just be ignored once the DB row is swept.
const oauthStateMaxAge = 10 * time.Minute

func oauthStateCookie(value string, secure bool) *http.Cookie {
	return &http.Cookie{
		Name:     OAuthStateCookieName,
		Value:    value,
		Path:     "/api/auth/",
		HttpOnly: true,
		// Lax is required: the IdP redirects the browser back to us via a
		// top-level GET, and Strict would drop the cookie on that hop.
		SameSite: http.SameSiteLaxMode,
		Secure:   secure,
		MaxAge:   int(oauthStateMaxAge.Seconds()),
		Expires:  time.Now().Add(oauthStateMaxAge),
	}
}

// SetOAuthStateCookie writes the state nonce to a short-lived, HttpOnly,
// SameSite=Lax cookie scoped to /api/auth/. Call this in the login handler
// immediately after generating the state, with the same value passed to the
// IdP authorization URL. trustedNets is the configured list of trusted-proxy
// CIDRs; pass cfg.TrustedProxyNets so the Secure flag is set correctly
// behind a TLS-terminating reverse proxy.
func SetOAuthStateCookie(w http.ResponseWriter, r *http.Request, state string, trustedNets []*net.IPNet) {
	http.SetCookie(w, oauthStateCookie(state, cookieSecure(r, trustedNets)))
}

// ClearOAuthStateCookie deletes the binding cookie. Call after a successful
// callback so a stale cookie can't be reused.
func ClearOAuthStateCookie(w http.ResponseWriter, r *http.Request, trustedNets []*net.IPNet) {
	c := oauthStateCookie("", cookieSecure(r, trustedNets))
	c.MaxAge = -1
	c.Expires = time.Unix(0, 0)
	http.SetCookie(w, c)
}

// VerifyOAuthStateCookie reports whether the request carries an oauth state
// cookie whose value matches state (constant-time compare). A missing cookie
// or a mismatched value both return false; callers should respond 400 in
// either case without consuming the server-side nonce, so the legitimate
// browser can still complete its flow.
func VerifyOAuthStateCookie(r *http.Request, state string) bool {
	if state == "" {
		return false
	}
	c, err := r.Cookie(OAuthStateCookieName)
	if err != nil || c.Value == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(c.Value), []byte(state)) == 1
}
