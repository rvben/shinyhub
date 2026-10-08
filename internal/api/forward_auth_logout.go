package api

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"html/template"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/rvben/shinyhub/internal/auth"
	"github.com/rvben/shinyhub/internal/db"
)

type browserLogoutCredentials struct {
	Tokens          []db.TokenRevocation
	Native, Forward *auth.Claims
}

func logoutHash(code string) string {
	sum := sha256.Sum256([]byte(code))
	return hex.EncodeToString(sum[:])
}
func newBrowserLogoutCode() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

func (s *Server) browserLogoutCredentials(r *http.Request, header bool) browserLogoutCredentials {
	var result browserLogoutCredentials
	add := func(claims *auth.Claims) {
		if claims == nil || claims.ID == "" || claims.UserID <= 0 || claims.SupportSessionID != "" {
			return
		}
		var expires, original = claims.ExpiresAt.Time, time.Time{}
		if claims.AuthTime != nil {
			original = claims.AuthTime.Time
		}
		expires = s.browserSessionRevocationExpiry(original, expires)
		for i, t := range result.Tokens {
			if t.JTI == claims.ID {
				if expires.After(t.ExpiresAt) {
					result.Tokens[i].ExpiresAt = expires
				}
				return
			}
		}
		result.Tokens = append(result.Tokens, db.TokenRevocation{JTI: claims.ID, UserID: claims.UserID, ExpiresAt: expires})
	}
	native := func(raw string) *auth.Claims {
		claims, _ := auth.ValidateJWT(raw, s.cfg.Auth.Secret, nil)
		if claims == nil || claims.ExpiresAt == nil || claims.SupportSessionID != "" {
			return nil
		}
		return claims
	}
	if c, err := r.Cookie(auth.SessionCookieName); err == nil {
		result.Native = native(c.Value)
		add(result.Native)
	}
	if c, err := auth.ForwardAuthSessionCookieFromRequest(r, s.cfg.TrustedProxyNets); err == nil {
		result.Forward, _ = auth.ValidateForwardAuthSession(c.Value, s.cfg.Auth.Secret)
		add(result.Forward)
	}
	if header {
		parts := strings.Fields(r.Header.Get("Authorization"))
		if len(parts) == 2 && strings.EqualFold(parts[0], "Bearer") {
			add(native(parts[1]))
		}
	}
	return result
}

func (s *Server) endBrowserSession(w http.ResponseWriter, r *http.Request, action string) (bool, bool) {
	credentials := s.browserLogoutCredentials(r, true)
	u := auth.UserFromContext(r.Context())
	// The first forwarded request may have minted its family on this response;
	// it is not yet in the request's cookie header, but must be ended too.
	if u != nil && auth.ForwardAuthFromContext(r.Context()) {
		if ti := auth.TokenInfoFromContext(r.Context()); ti != nil && ti.JTI != "" {
			found := false
			for _, t := range credentials.Tokens {
				if t.JTI == ti.JTI {
					found = true
					break
				}
			}
			if !found {
				credentials.Tokens = append(credentials.Tokens, db.TokenRevocation{JTI: ti.JTI, UserID: u.ID, ExpiresAt: s.browserSessionRevocationExpiry(ti.AuthTime, ti.ExpiresAt)})
			}
		}
	}
	ids := make([]int64, 0, len(credentials.Tokens)+1)
	for _, t := range credentials.Tokens {
		if !slices.Contains(ids, t.UserID) {
			ids = append(ids, t.UserID)
		}
	}
	if u != nil && !slices.Contains(ids, u.ID) {
		ids = append(ids, u.ID)
	}
	_, cookieErr := r.Cookie(auth.SessionCookieName)
	browser := cookieErr == nil || auth.ForwardAuthFromContext(r.Context()) && auth.TokenInfoFromContext(r.Context()) != nil || action == "logout_handoff"
	var code *db.BrowserLogoutCode
	var raw string
	if browser && s.cfg.Auth.ForwardAuth.Enabled && s.cfg.Server.AppOrigin != "" && len(ids) > 0 {
		var err error
		raw, err = newBrowserLogoutCode()
		if err != nil {
			writeError(w, http.StatusServiceUnavailable, "could not end session; try again")
			return false, false
		}
		code = &db.BrowserLogoutCode{Hash: logoutHash(raw), UserIDs: ids}
		if action == "logout_handoff" && s.cfg.Auth.ForwardAuth.LogoutURL == "" {
			code.NextPath = safeNextPath(r.FormValue("next"))
		}
	}
	count, err := s.store.RevokeBrowserTokens(credentials.Tokens, code)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "could not end session; try again")
		return false, false
	}
	if u == nil && count > 0 {
		for _, token := range credentials.Tokens {
			if actor, err := s.store.LookupContextUser(token.UserID); err == nil {
				u = actor
				break
			}
		}
	}
	if u != nil {
		jtIs := make([]string, 0, len(credentials.Tokens))
		for _, t := range credentials.Tokens {
			jtIs = append(jtIs, t.JTI)
		}
		s.logAuditEvent(r, db.AuditEventParams{UserID: &u.ID, Action: action, ResourceType: "user", ResourceID: u.Username, Detail: db.AuditDetail(map[string]any{"token_revoked": count > 0, "revoked_tokens": count, "token_ids": jtIs, "forward_auth": auth.ForwardAuthFromContext(r.Context())}), IPAddress: s.ClientIP(r)})
	}
	auth.ClearSessionCookie(w, r, s.cfg.TrustedProxyNets)
	if browser && s.cfg.Auth.ForwardAuth.Enabled {
		auth.ClearForwardAuthSessionCookie(w, r, s.cfg.TrustedProxyNets)
		auth.SetForwardAuthSignedOut(w, r, s.cfg.TrustedProxyNets)
		if code != nil {
			auth.SetLogoutHandoffCookie(w, r, raw, s.cfg.TrustedProxyNets)
		}
	}
	w.Header().Set("Cache-Control", "no-store")
	return true, code != nil
}

func (s *Server) handleForwardAuthResume(w http.ResponseWriter, r *http.Request) {
	if !s.cfg.Auth.ForwardAuth.Enabled {
		writeError(w, http.StatusNotFound, "forward authentication is not configured")
		return
	}
	if !auth.IsDashboardPost(r, s.cfg.TrustedProxyNets) {
		writeError(w, http.StatusForbidden, "reconnect must originate from the dashboard")
		return
	}
	u := auth.UserFromContext(r.Context())
	if u == nil || !auth.ForwardAuthFromContext(r.Context()) {
		writeError(w, http.StatusUnauthorized, "sign in through your authentication service first")
		return
	}
	token, _, err := auth.IssueForwardAuthSession(u, s.cfg.Auth.Secret, time.Time{}, s.cfg.Auth.BrowserSessionMaxAge(), "")
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "could not reconnect; try again")
		return
	}
	auth.SetForwardAuthSessionCookie(w, r, token, s.cfg.TrustedProxyNets)
	auth.ClearForwardAuthSignedOut(w, r, s.cfg.TrustedProxyNets)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleForwardAuthLogout(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	if !s.cfg.Auth.ForwardAuth.Enabled || !auth.ForwardAuthSignedOut(r, s.cfg.TrustedProxyNets) {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}
	handoff, err := auth.LogoutHandoffFromRequest(r, s.cfg.TrustedProxyNets)
	var nextPath string
	if complete := r.URL.Query().Get("complete"); complete != "" {
		if err != nil {
			writeError(w, http.StatusBadRequest, "logout completion expired or invalid; retry signing out")
			return
		}
		var consumeErr error
		nextPath, consumeErr = s.store.ConsumeBrowserLogoutCompletion(logoutHash(complete), logoutHash(handoff.Value))
		if consumeErr != nil {
			status := http.StatusServiceUnavailable
			if errors.Is(consumeErr, db.ErrNotFound) {
				status = http.StatusBadRequest
			}
			writeError(w, status, "could not complete logout; retry this page")
			return
		}
		auth.ClearLogoutHandoffCookie(w, r, s.cfg.TrustedProxyNets)
	} else if err == nil && handoff.Value != "" && s.cfg.Server.AppOrigin != "" {
		pending, readErr := s.store.BrowserLogoutCodePending(logoutHash(handoff.Value))
		if readErr != nil {
			writeError(w, http.StatusServiceUnavailable, "could not complete logout; retry this page")
			return
		}
		if pending {
			appOrigin, parseErr := url.Parse(s.cfg.Server.AppOrigin)
			if parseErr != nil {
				writeError(w, http.StatusServiceUnavailable, "invalid app origin")
				return
			}
			targetURL := appOrigin.JoinPath(auth.AppLogoutPath)
			targetURL.RawQuery = url.Values{"code": {handoff.Value}}.Encode()
			target := targetURL.String()
			http.Redirect(w, r, target, http.StatusSeeOther)
			return
		}
		auth.ClearLogoutHandoffCookie(w, r, s.cfg.TrustedProxyNets)
	}
	target := s.cfg.Auth.ForwardAuth.LogoutURL
	if target == "" {
		login := "/login"
		if next := safeNextPath(nextPath); next != "" {
			login += "?" + url.Values{"next": {next}}.Encode()
		}
		http.Redirect(w, r, login, http.StatusSeeOther)
		return
	}
	if s.cfg.Auth.ForwardAuth.LogoutMethod != "POST" {
		http.Redirect(w, r, target, http.StatusSeeOther)
		return
	}
	hash := sha256.Sum256([]byte(upstreamLogoutScript))
	w.Header().Set("Content-Security-Policy", "default-src 'none'; script-src 'sha256-"+base64.StdEncoding.EncodeToString(hash[:])+"'; base-uri 'none'; frame-ancestors 'none'")
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_ = upstreamLogoutTemplate.Execute(w, target)
}

const upstreamLogoutScript = `document.getElementById('logout').submit();`

var upstreamLogoutTemplate = template.Must(template.New("logout").Parse(`<!doctype html><html lang="en"><meta charset="utf-8"><title>Complete sign out</title><h1>Complete sign out</h1><p>Continue to your sign-in service to end its session.</p><form id="logout" method="post" action="{{.}}"><button type="submit">Continue signing out</button></form><a href="/login">Back to ShinyHub</a><script>` + upstreamLogoutScript + `</script></html>`))

// HandleAppLogout is admitted only on the configured app origin by the server mux.
func (s *Server) HandleAppLogout(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", "GET")
		writeError(w, http.StatusMethodNotAllowed, "logout handoff requires GET")
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	raw := r.URL.Query().Get("code")
	if raw == "" || !s.cfg.Auth.ForwardAuth.Enabled || s.cfg.Server.AppOrigin == "" {
		writeError(w, http.StatusBadRequest, "invalid logout handoff")
		return
	}
	credentials := s.browserLogoutCredentials(r, false)
	complete, err := newBrowserLogoutCode()
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "could not complete logout; retry from the dashboard")
		return
	}
	ids, err := s.store.FinishAppBrowserLogout(logoutHash(raw), logoutHash(complete), credentials.Tokens)
	if err != nil {
		status := http.StatusServiceUnavailable
		if errors.Is(err, db.ErrNotFound) {
			status = http.StatusUnauthorized
		}
		writeError(w, status, "logout handoff expired or failed; retry from the dashboard")
		return
	}
	nativeMatch := credentials.Native != nil && slices.Contains(ids, credentials.Native.UserID)
	forwardMatch := credentials.Forward != nil && slices.Contains(ids, credentials.Forward.UserID)
	u := auth.UserFromContext(r.Context())
	forwardID := int64(0)
	if auth.ForwardAuthFromContext(r.Context()) && u != nil {
		forwardID = u.ID
	}
	if nativeMatch {
		auth.ClearSessionCookie(w, r, s.cfg.TrustedProxyNets)
	}
	if forwardMatch {
		auth.ClearForwardAuthSessionCookie(w, r, s.cfg.TrustedProxyNets)
	}
	if slices.Contains(ids, forwardID) || forwardID == 0 && (nativeMatch || forwardMatch) {
		auth.SetForwardAuthSignedOut(w, r, s.cfg.TrustedProxyNets)
	}
	target := strings.TrimRight(s.cfg.Server.BaseURL, "/") + auth.ForwardAuthLogoutPath + "?complete=" + url.QueryEscape(complete)
	http.Redirect(w, r, target, http.StatusSeeOther)
}
