package auth

import (
	"net"
	"net/http"
	"time"

	"github.com/rvben/shinyhub/internal/proxytrust"
)

// cookieSecure decides the Secure flag for cookies we set on this response.
// It mirrors proxytrust.Scheme: a direct TLS connection always wins, and
// X-Forwarded-Proto is honoured only when the direct peer is in trustedNets.
//
// An attacker connecting directly over plain HTTP could otherwise spoof
// `X-Forwarded-Proto: https` and trick us into setting Secure cookies on a
// non-HTTPS origin — which the browser then silently drops on every
// subsequent HTTP request, breaking session establishment.
func cookieSecure(r *http.Request, trustedNets []*net.IPNet) bool {
	return proxytrust.Scheme(r, trustedNets) == "https"
}

func sessionCookie(token string, secure bool) *http.Cookie {
	return &http.Cookie{
		Name:     SessionCookieName,
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   secure,
		MaxAge:   int(jwtExpiry.Seconds()),
		Expires:  time.Now().Add(jwtExpiry),
	}
}

// SetSessionCookie stores the signed JWT in an HttpOnly session cookie.
// trustedNets is the configured list of trusted-proxy CIDRs; pass
// cfg.TrustedProxyNets. See cookieSecure for why this matters.
func SetSessionCookie(w http.ResponseWriter, r *http.Request, token string, trustedNets []*net.IPNet) {
	http.SetCookie(w, sessionCookie(token, cookieSecure(r, trustedNets)))
}

// ClearSessionCookie removes the browser session cookie.
func ClearSessionCookie(w http.ResponseWriter, r *http.Request, trustedNets []*net.IPNet) {
	c := sessionCookie("", cookieSecure(r, trustedNets))
	c.MaxAge = -1
	c.Expires = time.Unix(0, 0)
	http.SetCookie(w, c)
}
