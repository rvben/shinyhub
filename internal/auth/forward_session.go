package auth

import (
	"context"
	"errors"
	"html/template"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/rvben/shinyhub/internal/originhost"
	"github.com/rvben/shinyhub/internal/proxytrust"
	"github.com/rvben/shinyhub/internal/rawquery"
)

const (
	ForwardAuthSignedOutCookie       = "shiny_forward_signed_out"
	SecureForwardAuthSignedOutCookie = "__Host-shiny_forward_signed_out"
	ForwardAuthSessionCookie         = "shiny_forward_session"
	SecureForwardAuthSessionCookie   = "__Host-shiny_forward_session"
	LogoutHandoffCookie              = "shiny_logout_handoff"
	SecureLogoutHandoffCookie        = "__Host-shiny_logout_handoff"
	ForwardAuthResumePath            = "/api/auth/forward-auth/resume"
	ForwardAuthLogoutPath            = "/api/auth/forward-auth/logout"
	AppLogoutPath                    = "/api/app/logout"
	forwardSessionPurpose            = "forward-auth-session"
)

type forwardAuthContextKey struct{}

func ForwardAuthFromContext(ctx context.Context) bool {
	v, _ := ctx.Value(forwardAuthContextKey{}).(bool)
	return v
}

func browserCookieName(r *http.Request, trusted []*net.IPNet, plain, secure string) string {
	if cookieSecure(r, trusted) {
		return secure
	}
	return plain
}

func ForwardAuthSignedOut(r *http.Request, trusted []*net.IPNet) bool {
	c, err := r.Cookie(browserCookieName(r, trusted, ForwardAuthSignedOutCookie, SecureForwardAuthSignedOutCookie))
	return err == nil && c.Value != ""
}
func ForwardAuthSessionCookieFromRequest(r *http.Request, trusted []*net.IPNet) (*http.Cookie, error) {
	return r.Cookie(browserCookieName(r, trusted, ForwardAuthSessionCookie, SecureForwardAuthSessionCookie))
}
func LogoutHandoffFromRequest(r *http.Request, trusted []*net.IPNet) (*http.Cookie, error) {
	return r.Cookie(browserCookieName(r, trusted, LogoutHandoffCookie, SecureLogoutHandoffCookie))
}
func setForwardCookie(w http.ResponseWriter, r *http.Request, trusted []*net.IPNet, plain, secure, value string, maxAge int) {
	c := &http.Cookie{Name: browserCookieName(r, trusted, plain, secure), Value: value, Path: "/", HttpOnly: true, Secure: cookieSecure(r, trusted), SameSite: http.SameSiteLaxMode, MaxAge: maxAge}
	if maxAge > 0 {
		c.Expires = time.Now().Add(time.Duration(maxAge) * time.Second)
	}
	http.SetCookie(w, c)
	w.Header().Set("Cache-Control", "no-store")
}
func clearForwardCookies(w http.ResponseWriter, r *http.Request, trusted []*net.IPNet, names ...string) {
	for _, name := range names {
		http.SetCookie(w, &http.Cookie{Name: name, Path: "/", HttpOnly: true, Secure: strings.HasPrefix(name, "__Host-") || cookieSecure(r, trusted), SameSite: http.SameSiteLaxMode, MaxAge: -1, Expires: time.Unix(0, 0)})
	}
}
func SetForwardAuthSignedOut(w http.ResponseWriter, r *http.Request, trusted []*net.IPNet) {
	setForwardCookie(w, r, trusted, ForwardAuthSignedOutCookie, SecureForwardAuthSignedOutCookie, "1", 0)
}
func ClearForwardAuthSignedOut(w http.ResponseWriter, r *http.Request, trusted []*net.IPNet) {
	clearForwardCookies(w, r, trusted, ForwardAuthSignedOutCookie, SecureForwardAuthSignedOutCookie)
}
func SetForwardAuthSessionCookie(w http.ResponseWriter, r *http.Request, token string, trusted []*net.IPNet) {
	setForwardCookie(w, r, trusted, ForwardAuthSessionCookie, SecureForwardAuthSessionCookie, token, 0)
}
func ClearForwardAuthSessionCookie(w http.ResponseWriter, r *http.Request, trusted []*net.IPNet) {
	clearForwardCookies(w, r, trusted, ForwardAuthSessionCookie, SecureForwardAuthSessionCookie)
}
func SetLogoutHandoffCookie(w http.ResponseWriter, r *http.Request, code string, trusted []*net.IPNet) {
	setForwardCookie(w, r, trusted, LogoutHandoffCookie, SecureLogoutHandoffCookie, code, 120)
}
func ClearLogoutHandoffCookie(w http.ResponseWriter, r *http.Request, trusted []*net.IPNet) {
	clearForwardCookies(w, r, trusted, LogoutHandoffCookie, SecureLogoutHandoffCookie)
}

// A tracking token identifies revocation, but can never authenticate a native session.
func IssueForwardAuthSession(u *ContextUser, secret string, authTime time.Time, maxAge time.Duration, jti string) (string, *TokenInfo, error) {
	if authTime.IsZero() {
		authTime = time.Now()
	}
	if maxAge <= 0 {
		maxAge = AbsoluteSessionMaxAge
	}
	if u.SupportSession != nil {
		return "", nil, ErrSupportSessionScope
	}
	return issueUserJWTAtWithID(u, secret, authTime, authTime.Add(maxAge).Truncate(time.Second), jti, forwardSessionPurpose)
}
func ValidateForwardAuthSession(token, secret string) (*Claims, error) {
	claims, err := validateJWT(token, secret, nil, forwardSessionPurpose)
	if err != nil {
		return nil, err
	}
	if claims.ExpiresAt == nil || claims.AuthTime == nil || claims.ID == "" || claims.UserID <= 0 || claims.SupportSessionID != "" {
		return nil, ErrSupportSessionInvalid
	}
	return claims, nil
}

// Reconnecting grants identity, so requests need a dashboard-origin Referer.
// Same-origin apps are trusted; only a separate app origin isolates their scripts.
func IsDashboardPost(r *http.Request, trusted []*net.IPNet) bool {
	host, scheme := proxytrust.Host(r, trusted), proxytrust.Scheme(r, trusted)
	authority, authorityErr := forwardOriginAuthority(host, scheme)
	matches := func(raw string) (*url.URL, bool) {
		u, err := url.Parse(raw)
		if err != nil || authorityErr != nil || !strings.EqualFold(u.Scheme, scheme) || u.User != nil {
			return u, false
		}
		origin, err := forwardOriginAuthority(u.Host, scheme)
		return u, err == nil && origin == authority
	}
	if origin := r.Header.Get("Origin"); origin != "" {
		if _, ok := matches(origin); !ok {
			return false
		}
	}
	ref, ok := matches(r.Header.Get("Referer"))
	return ok && ref.Path != "/app" && !strings.HasPrefix(ref.Path, "/app/")
}

func forwardBrowserFamily(w http.ResponseWriter, r *http.Request, u *ContextUser, cfg ForwardAuthConfig, trusted []*net.IPNet) (*TokenInfo, bool, error) {
	var claims *Claims
	if c, err := ForwardAuthSessionCookieFromRequest(r, trusted); err == nil {
		claims, _ = ValidateForwardAuthSession(c.Value, cfg.SessionSecret)
	}
	trackingFamily := claims != nil && claims.UserID == u.ID
	if claims == nil || claims.UserID != u.ID {
		claims = nil
		maxAge := cfg.SessionMaxAge
		if maxAge <= 0 {
			maxAge = AbsoluteSessionMaxAge
		}
		if c, err := r.Cookie(SessionCookieName); err == nil {
			candidate, _ := ValidateJWT(c.Value, cfg.SessionSecret, nil)
			if candidate != nil && candidate.SupportSessionID == "" && candidate.UserID == u.ID &&
				(candidate.AuthTime == nil || time.Now().Before(candidate.AuthTime.Time.Add(maxAge))) {
				claims = candidate
			}
		}
	}
	if claims != nil {
		if claims.SessionEpoch != u.TokenEpoch {
			return nil, false, nil
		}
		if cfg.Revoked != nil {
			revoked, err := cfg.Revoked(claims.ID)
			if err != nil {
				return nil, false, err
			}
			if revoked {
				return nil, false, nil
			}
		}
	}
	// Existing families need no renewal: preserve asset caching and their
	// original absolute expiry without emitting cookie or cache headers.
	if trackingFamily {
		return tokenFromClaims(claims), true, nil
	}
	// App tabs reconnect concurrently when a family expires. Only control-host
	// document navigations may establish a fresh family; app-host traffic and API
	// requests must use an existing one or explicitly reconnect. This prevents
	// upgrades from creating orphan families that logout could not revoke.
	if claims == nil && !((forwardPageLoad(r) && !forwardAppHost(r, cfg, trusted)) ||
		(r.Method == http.MethodPost && (r.URL.Path == "/api/auth/logout" || r.URL.Path == "/api/auth/handoff"))) {
		return nil, false, errForwardFamilyMissing
	}
	var original time.Time
	var jti string
	if claims != nil {
		jti = claims.ID
		if claims.AuthTime != nil {
			original = claims.AuthTime.Time
		}
	}
	token, ti, err := IssueForwardAuthSession(u, cfg.SessionSecret, original, cfg.SessionMaxAge, jti)
	if err != nil {
		return nil, false, err
	}
	SetForwardAuthSessionCookie(w, r, token, trusted)
	return ti, true, nil
}

var errForwardFamilyMissing = errors.New("forward browser family missing")

func forwardPageLoad(r *http.Request) bool {
	if r.Method != http.MethodGet || strings.EqualFold(r.Header.Get("Upgrade"), "websocket") {
		return false
	}
	if dest := r.Header.Get("Sec-Fetch-Dest"); dest != "" {
		return strings.EqualFold(dest, "document") || ((strings.EqualFold(dest, "iframe") || strings.EqualFold(dest, "frame")) && (r.Header.Get("Sec-Fetch-Mode") == "" || strings.EqualFold(r.Header.Get("Sec-Fetch-Mode"), "navigate")))
	}
	return strings.Contains(strings.ToLower(r.Header.Get("Accept")), "text/html")
}

func forwardAppHost(r *http.Request, cfg ForwardAuthConfig, trusted []*net.IPNet) bool {
	requestHost, err := originhost.Authority(proxytrust.Host(r, trusted))
	appHost, appErr := originhost.Authority(cfg.AppOriginHost)
	return cfg.AppOriginHost != "" && err == nil && appErr == nil && requestHost == appHost
}

// ForwardAuthCookieCheckParam confirms that app-launch cookies persisted before
// retrying a launch through the control host. It carries no authorization.
const ForwardAuthCookieCheckParam = "__shinyhub_cookie_check"

func forwardOriginAuthority(raw, scheme string) (string, error) {
	parsed, err := url.Parse("//" + raw)
	if err != nil {
		return "", err
	}
	host, err := originhost.Hostname(raw)
	if err != nil {
		return "", err
	}
	port := parsed.Port()
	if port == "" || (strings.EqualFold(scheme, "https") && port == "443") || (strings.EqualFold(scheme, "http") && port == "80") {
		return host, nil
	}
	return net.JoinHostPort(host, port), nil
}

func forwardFrameBlocked(r *http.Request) bool {
	if !forwardPageLoad(r) {
		return false
	}
	dest := strings.ToLower(r.Header.Get("Sec-Fetch-Dest"))
	if dest == "document" {
		return false
	}
	if strings.EqualFold(r.Header.Get("Sec-Fetch-Site"), "cross-site") {
		return true
	}
	return false
}

var forwardFrameTemplate = template.Must(template.New("frame-session").Parse(`<!doctype html><html lang="en"><meta charset="utf-8"><title>Open app to sign in</title><h1>Open this app in a new tab</h1><p>This embedded app cannot establish its browser session. Open it top-level with cookies enabled to sign in.</p><a href="{{.}}" target="_blank" rel="noopener noreferrer">Open app</a></html>`))

func forwardFrameUnavailable(w http.ResponseWriter, r *http.Request) {
	target := *r.URL
	target.RawQuery = rawquery.Delete(target.RawQuery, ForwardAuthCookieCheckParam)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; base-uri 'none'")
	w.WriteHeader(http.StatusUnauthorized)
	link := target.RequestURI()
	if !strings.HasPrefix(link, "/") || strings.HasPrefix(link, "//") {
		link = "/"
	}
	_ = forwardFrameTemplate.Execute(w, link)
}
