package auth

import (
	"net/http"
	"strings"
	"time"
)

func cookieSecure(r *http.Request) bool {
	if r != nil {
		if r.TLS != nil {
			return true
		}
		if strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https") {
			return true
		}
	}
	return false
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
func SetSessionCookie(w http.ResponseWriter, r *http.Request, token string) {
	http.SetCookie(w, sessionCookie(token, cookieSecure(r)))
}

// ClearSessionCookie removes the browser session cookie.
func ClearSessionCookie(w http.ResponseWriter, r *http.Request) {
	c := sessionCookie("", cookieSecure(r))
	c.MaxAge = -1
	c.Expires = time.Unix(0, 0)
	http.SetCookie(w, c)
}
