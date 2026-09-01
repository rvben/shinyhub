package auth

import (
	"net"
	"net/http"
	"strings"
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

func supportSessionCookie(token, appSlug string, expiresAt time.Time, secure bool) *http.Cookie {
	path := "/app/" + strings.Trim(appSlug, "/") + "/"
	maxAge := int(time.Until(expiresAt).Seconds())
	if maxAge < 0 {
		maxAge = 0
	}
	return &http.Cookie{
		Name:     SupportSessionCookieName,
		Value:    token,
		Path:     path,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   secure,
		MaxAge:   maxAge,
		Expires:  expiresAt,
	}
}

// SetSupportSessionCookie stores a purpose-built token on exactly one app
// path. It never overwrites or widens the administrator's dashboard session.
func SetSupportSessionCookie(w http.ResponseWriter, r *http.Request, token, appSlug string, expiresAt time.Time, trustedNets []*net.IPNet) {
	http.SetCookie(w, supportSessionCookie(token, appSlug, expiresAt, cookieSecure(r, trustedNets)))
}

// ClearSupportSessionCookie removes the app-scoped support cookie. The path
// must exactly match creation or browsers will retain the privileged cookie.
func ClearSupportSessionCookie(w http.ResponseWriter, r *http.Request, appSlug string, trustedNets []*net.IPNet) {
	c := supportSessionCookie("", appSlug, time.Unix(0, 0), cookieSecure(r, trustedNets))
	c.MaxAge = -1
	c.Expires = time.Unix(0, 0)
	http.SetCookie(w, c)
}

// SetSupportSessionGuardCookie prevents a browser with an active support
// session from falling back to its normal/forward-auth administrator identity
// when app JavaScript navigates outside the bound slug.
func SetSupportSessionGuardCookie(w http.ResponseWriter, r *http.Request, sessionID string, expiresAt time.Time, trustedNets []*net.IPNet) {
	http.SetCookie(w, &http.Cookie{
		Name: SupportSessionGuardCookieName, Value: sessionID, Path: "/", HttpOnly: true,
		SameSite: http.SameSiteLaxMode, Secure: cookieSecure(r, trustedNets),
		MaxAge: int(time.Until(expiresAt).Seconds()), Expires: expiresAt,
	})
}

func ClearSupportSessionGuardCookie(w http.ResponseWriter, r *http.Request, trustedNets []*net.IPNet) {
	http.SetCookie(w, &http.Cookie{
		Name: SupportSessionGuardCookieName, Path: "/", HttpOnly: true,
		SameSite: http.SameSiteLaxMode, Secure: cookieSecure(r, trustedNets),
		MaxAge: -1, Expires: time.Unix(0, 0),
	})
}
