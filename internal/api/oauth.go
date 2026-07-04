package api

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/rvben/shinyhub/internal/auth"
	"github.com/rvben/shinyhub/internal/db"
	"github.com/rvben/shinyhub/internal/oauth"
)

// SetGitHubProvider replaces the GitHub OAuth provider after the server is
// constructed. Must be called before the server begins handling requests.
// Production wiring happens in New via cfg.OAuth.GitHub; this setter exists so
// tests can point a GitHub provider at a fake server (see SetTestEndpoints on
// oauth.GitHub).
func (s *Server) SetGitHubProvider(g *oauth.GitHub) { s.github = g }

// SetGoogleProvider replaces the Google OAuth provider after the server is
// constructed. Must be called before the server begins handling requests.
// Production wiring happens in New via cfg.OAuth.Google; this setter exists so
// tests can point a Google provider at a fake server (see SetTestEndpoints on
// oauth.Google).
func (s *Server) SetGoogleProvider(g *oauth.Google) { s.googleOAuth = g }

// handleGitHubLogin redirects the browser to GitHub's OAuth2 authorization page.
func (s *Server) handleGitHubLogin(w http.ResponseWriter, r *http.Request) {
	if s.github == nil {
		writeError(w, http.StatusNotImplemented, "GitHub OAuth not configured")
		return
	}

	stateBytes := make([]byte, 16)
	if _, err := rand.Read(stateBytes); err != nil {
		writeError(w, http.StatusInternalServerError, "internal server error")
		return
	}
	state := hex.EncodeToString(stateBytes)

	if err := s.store.CreateOAuthState(state); err != nil {
		writeError(w, http.StatusInternalServerError, "internal server error")
		return
	}

	auth.SetOAuthStateCookie(w, r, state, s.cfg.TrustedProxyNets)
	http.Redirect(w, r, s.github.AuthURL(state), http.StatusFound)
}

// handleGitHubCallback handles the GitHub OAuth2 callback, creates or finds
// the local user account, and issues a JWT.
func (s *Server) handleGitHubCallback(w http.ResponseWriter, r *http.Request) {
	if s.github == nil {
		writeError(w, http.StatusNotImplemented, "GitHub OAuth not configured")
		return
	}

	state := r.URL.Query().Get("state")
	code := r.URL.Query().Get("code")
	if state == "" || code == "" {
		writeError(w, http.StatusBadRequest, "missing state or code")
		return
	}

	// Bind the state to the browser that started the login. Verify before
	// consuming the server-side nonce so a legitimate user with the cookie
	// can still finish their flow if an attacker's replay arrives first.
	if !auth.VerifyOAuthStateCookie(r, state) {
		writeError(w, http.StatusBadRequest, "invalid or expired state")
		return
	}
	if err := s.store.ConsumeOAuthState(state); err != nil {
		writeError(w, http.StatusBadRequest, "invalid or expired state")
		return
	}
	auth.ClearOAuthStateCookie(w, r, s.cfg.TrustedProxyNets)

	tok, err := s.github.Exchange(r.Context(), code)
	if err != nil {
		reqLog(r).Error("oauth_exchange_failed", "provider", "github", "err", err)
		writeError(w, http.StatusBadGateway, "OAuth exchange failed")
		return
	}

	ghUser, err := s.github.FetchUser(r.Context(), tok)
	if err != nil {
		reqLog(r).Error("oauth_fetch_user_failed", "provider", "github", "err", err)
		writeError(w, http.StatusBadGateway, "failed to fetch GitHub user")
		return
	}

	providerID := strconv.FormatInt(ghUser.ID, 10)

	user, created, err := s.store.ProvisionOAuthUser(db.ProvisionOAuthUserParams{
		Provider:   "github",
		ProviderID: providerID,
		UsernameCandidates: []string{
			ghUser.Login,
			fmt.Sprintf("%s-gh%s", ghUser.Login, providerID),
		},
		Role: s.jitOAuthRole(),
	})
	if err != nil {
		reqLog(r).Error("oauth_provision_user_failed", "provider", "github", "err", err)
		writeError(w, http.StatusInternalServerError, "internal server error")
		return
	}
	if created {
		s.store.LogAuditEvent(db.AuditEventParams{
			UserID: &user.ID, Action: "create_user", ResourceType: "user",
			ResourceID: user.Username, IPAddress: s.ClientIP(r),
		})
	}
	// Refresh the IdP-governed display name from GitHub. No-op for accounts with
	// a local password (self-managed) or when GitHub sends no name. Non-fatal.
	if err := s.store.SetDisplayNameFromIdP(user.ID, ghUser.Name); err != nil {
		reqLog(r).Warn("oauth_display_name_failed", "provider", "github", "err", err)
	}
	// Persist the IdP email so native session requests forward X-Shinyhub-Email.
	// FetchUser resolves the verified primary for private-email accounts, so this
	// is a no-op only for local-password accounts or when GitHub exposes no
	// verified email at all.
	if err := s.store.SetEmailFromIdP(user.ID, ghUser.Email); err != nil {
		reqLog(r).Warn("oauth_email_failed", "provider", "github", "err", err)
	}

	jwtToken, err := auth.IssueJWT(user.ID, user.Username, user.Role, s.cfg.Auth.Secret)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal server error")
		return
	}

	auth.SetSessionCookie(w, r, jwtToken, s.cfg.TrustedProxyNets)
	s.store.LogAuditEvent(db.AuditEventParams{
		UserID: &user.ID, Action: "login", ResourceType: "user",
		ResourceID: user.Username, IPAddress: s.ClientIP(r),
	})
	http.Redirect(w, r, "/", http.StatusFound)
}

// handleGoogleLogin redirects the browser to Google's OAuth2 authorization page.
func (s *Server) handleGoogleLogin(w http.ResponseWriter, r *http.Request) {
	if s.googleOAuth == nil {
		writeError(w, http.StatusNotImplemented, "Google OAuth not configured")
		return
	}

	stateBytes := make([]byte, 16)
	if _, err := rand.Read(stateBytes); err != nil {
		writeError(w, http.StatusInternalServerError, "internal server error")
		return
	}
	state := hex.EncodeToString(stateBytes)

	if err := s.store.CreateOAuthState(state); err != nil {
		writeError(w, http.StatusInternalServerError, "internal server error")
		return
	}

	auth.SetOAuthStateCookie(w, r, state, s.cfg.TrustedProxyNets)
	http.Redirect(w, r, s.googleOAuth.AuthURL(state), http.StatusFound)
}

// handleGoogleCallback handles the Google OAuth2 callback, creates or finds
// the local user account, and issues a JWT.
func (s *Server) handleGoogleCallback(w http.ResponseWriter, r *http.Request) {
	if s.googleOAuth == nil {
		writeError(w, http.StatusNotImplemented, "Google OAuth not configured")
		return
	}

	state := r.URL.Query().Get("state")
	code := r.URL.Query().Get("code")
	if state == "" || code == "" {
		writeError(w, http.StatusBadRequest, "missing state or code")
		return
	}

	if !auth.VerifyOAuthStateCookie(r, state) {
		writeError(w, http.StatusBadRequest, "invalid or expired state")
		return
	}
	if err := s.store.ConsumeOAuthState(state); err != nil {
		writeError(w, http.StatusBadRequest, "invalid or expired state")
		return
	}
	auth.ClearOAuthStateCookie(w, r, s.cfg.TrustedProxyNets)

	tok, err := s.googleOAuth.Exchange(r.Context(), code)
	if err != nil {
		reqLog(r).Error("oauth_exchange_failed", "provider", "google", "err", err)
		writeError(w, http.StatusBadGateway, "OAuth exchange failed")
		return
	}

	gUser, err := s.googleOAuth.FetchUser(r.Context(), tok)
	if err != nil {
		reqLog(r).Error("oauth_fetch_user_failed", "provider", "google", "err", err)
		writeError(w, http.StatusBadGateway, "failed to fetch Google user")
		return
	}

	// Derive username from the email local part (before @).
	at := strings.IndexByte(gUser.Email, '@')
	username := gUser.Email
	if at > 0 {
		username = gUser.Email[:at]
	}
	if username == "" {
		username = "google-user"
	}

	user, created, err := s.store.ProvisionOAuthUser(db.ProvisionOAuthUserParams{
		Provider:   "google",
		ProviderID: gUser.ID,
		UsernameCandidates: []string{
			username,
			username + "-g" + gUser.ID,
		},
		Role: s.jitOAuthRole(),
	})
	if err != nil {
		reqLog(r).Error("oauth_provision_user_failed", "provider", "google", "err", err)
		writeError(w, http.StatusInternalServerError, "internal server error")
		return
	}
	if created {
		s.store.LogAuditEvent(db.AuditEventParams{
			UserID: &user.ID, Action: "create_user", ResourceType: "user",
			ResourceID: user.Username, IPAddress: s.ClientIP(r),
		})
	}
	// Refresh the IdP-governed display name from Google. No-op for accounts with
	// a local password (self-managed) or when Google sends no name. Non-fatal.
	if err := s.store.SetDisplayNameFromIdP(user.ID, gUser.Name); err != nil {
		reqLog(r).Warn("oauth_display_name_failed", "provider", "google", "err", err)
	}
	// Persist the IdP email so native session requests forward X-Shinyhub-Email.
	// No-op for local-password accounts. Google always sends a verified email.
	if err := s.store.SetEmailFromIdP(user.ID, gUser.Email); err != nil {
		reqLog(r).Warn("oauth_email_failed", "provider", "google", "err", err)
	}

	jwtToken, err := auth.IssueJWT(user.ID, user.Username, user.Role, s.cfg.Auth.Secret)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal server error")
		return
	}

	auth.SetSessionCookie(w, r, jwtToken, s.cfg.TrustedProxyNets)
	s.store.LogAuditEvent(db.AuditEventParams{
		UserID: &user.ID, Action: "login", ResourceType: "user",
		ResourceID: user.Username, IPAddress: s.ClientIP(r),
	})
	http.Redirect(w, r, "/", http.StatusFound)
}
