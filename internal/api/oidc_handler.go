package api

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/rvben/shinyhub/internal/auth"
	"github.com/rvben/shinyhub/internal/db"
	"github.com/rvben/shinyhub/internal/oauth"
)

// deriveOIDCNonce returns a per-authorization-request OIDC nonce, deterministically
// derived from the state nonce and the server's auth secret via HMAC-SHA256.
// Recomputing it at callback time (rather than storing it) needs no schema
// change: state is already single-use (consumed from oauth_states) and bound
// to the browser via the state cookie, so a value derived from it is exactly
// as unpredictable and exactly as scoped to this one login attempt as a
// separately-stored nonce would be. Defense-in-depth against ID-token
// replay/injection: an ID token minted for one authorization request carries a
// nonce claim that cannot be reproduced for a different state without knowing
// the server secret, so it cannot be substituted into another callback.
func deriveOIDCNonce(secret, state string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte("oidc-nonce:" + state))
	return hex.EncodeToString(mac.Sum(nil))
}

// handleGetProviders returns a JSON object indicating which login providers
// are currently enabled on this server instance.
func (s *Server) handleGetProviders(w http.ResponseWriter, r *http.Request) {
	type oidcInfo struct {
		Enabled      bool   `json:"enabled"`
		DisplayName  string `json:"display_name,omitempty"`
		AutoRedirect bool   `json:"auto_redirect,omitempty"`
	}
	type response struct {
		// Local reports whether the built-in username/password form is usable.
		// False for an SSO-only deployment (auth.local_login: false); the login
		// screen hides the password form when false.
		Local             bool     `json:"local"`
		ForwardAuth       bool     `json:"forward_auth"`
		ForwardAuthResume bool     `json:"forward_auth_resume"`
		ForwardAuthLogout bool     `json:"forward_auth_logout"`
		GitHub            bool     `json:"github"`
		Google            bool     `json:"google"`
		OIDC              oidcInfo `json:"oidc"`
	}

	resp := response{
		Local:             s.cfg.Auth.LocalLoginEnabled(),
		ForwardAuth:       s.cfg.Auth.ForwardAuth.Enabled,
		ForwardAuthResume: s.cfg.Auth.ForwardAuth.Enabled,
		ForwardAuthLogout: s.cfg.Auth.ForwardAuth.Enabled && (s.cfg.Auth.ForwardAuth.LogoutURL != "" || s.cfg.Server.AppOrigin != ""),
		GitHub:            s.github != nil,
		Google:            s.googleOAuth != nil,
		OIDC: oidcInfo{
			Enabled: s.oidcProvider != nil,
		},
	}
	if s.oidcProvider != nil {
		resp.OIDC.DisplayName = s.oidcProvider.DisplayName
		resp.OIDC.AutoRedirect = s.cfg.OAuth.OIDC.AutoRedirect && !resp.Local && !resp.GitHub && !resp.Google && !resp.ForwardAuth
	}

	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

// handleOIDCLogin redirects the browser to the OIDC identity provider's
// authorization endpoint.
func (s *Server) handleOIDCLogin(w http.ResponseWriter, r *http.Request) {
	if s.oidcProvider == nil {
		writeError(w, http.StatusNotFound, "OIDC SSO not configured")
		return
	}

	stateBytes := make([]byte, 16)
	if _, err := rand.Read(stateBytes); err != nil {
		writeInternal(w, r, "internal server error", err)
		return
	}
	state := hex.EncodeToString(stateBytes)
	nonce := deriveOIDCNonce(s.cfg.Auth.Secret, state)

	if err := s.store.CreateOAuthStateWithReturnPath(state, providerOIDC, safeNextPath(r.URL.Query().Get("next"))); err != nil {
		writeInternal(w, r, "internal server error", err)
		return
	}

	auth.SetOAuthStateCookie(w, r, state, s.cfg.TrustedProxyNets)
	http.Redirect(w, r, s.oidcProvider.AuthURL(state, nonce), http.StatusFound)
}

// handleOIDCCallback handles the OIDC authorization callback, verifies the
// ID token, and creates or finds the matching local user account before
// issuing a session JWT.
func (s *Server) handleOIDCCallback(w http.ResponseWriter, r *http.Request) {
	if s.oidcProvider == nil {
		writeError(w, http.StatusNotFound, "OIDC SSO not configured")
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
	returnPath, err := s.store.ConsumeOAuthStateWithReturnPath(state, providerOIDC)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid or expired state")
		return
	}
	auth.ClearOAuthStateCookie(w, r, s.cfg.TrustedProxyNets)

	tok, err := s.oidcProvider.Exchange(r.Context(), code)
	if err != nil {
		reqLog(r).Error("oidc_exchange_failed", "err", err)
		writeError(w, http.StatusBadGateway, "OAuth exchange failed")
		return
	}

	expectedNonce := deriveOIDCNonce(s.cfg.Auth.Secret, state)
	oidcUser, err := s.oidcProvider.VerifyIDToken(r.Context(), tok, expectedNonce)
	if err != nil {
		reqLog(r).Error("oidc_verify_id_token_failed", "err", err)
		writeError(w, http.StatusBadGateway, "failed to verify OIDC ID token")
		return
	}

	if oidcUser.GroupsOverage {
		reqLog(r).Warn("oidc_groups_overage")
		if s.cfg.OAuth.OIDC.GraphGroups {
			oidcUser.Groups, err = s.oidcProvider.EntraGroups(r.Context(), tok)
			if err != nil {
				reqLog(r).Warn("oidc_graph_groups_failed", "err", err)
				writeError(w, http.StatusBadGateway, "could not resolve OIDC groups")
				return
			}
			oidcUser.GroupsClaimPresent = true
			oidcUser.GroupsClaimMalformed = false
		} else if s.cfg.OAuth.OIDC.GroupsClaim == "" || s.cfg.OAuth.OIDC.GroupsClaim == "groups" {
			s.auditOIDCRefusal(r, "overage_unresolved", oidcUser.Sub)
			writeError(w, http.StatusForbidden, "OIDC group overage requires group resolution or app roles")
			return
		}
	}
	if s.cfg.OAuth.OIDC.GraphGroups {
		for _, group := range oidcUser.Groups {
			if _, err := uuid.Parse(group); err != nil {
				reqLog(r).Warn("oidc_groups_format_not_object_id")
				writeError(w, http.StatusBadGateway, "Graph group resolution requires Group ID claim format")
				return
			}
		}
	}
	if !oidcUser.GroupsClaimPresent && !oidcUser.GroupsClaimMalformed {
		switch s.cfg.OAuth.OIDC.MissingGroups {
		case "reject":
			s.auditOIDCRefusal(r, "claim_absent", oidcUser.Sub)
			writeError(w, http.StatusForbidden, "required OIDC authorization claim is absent")
			return
		case "empty":
			oidcUser.Groups = []string{}
			oidcUser.GroupsClaimPresent = true
		}
	}
	if oidcUser.GroupsClaimMalformed && (s.cfg.OAuth.OIDC.MissingGroups == "empty" || s.cfg.OAuth.OIDC.MissingGroups == "reject") {
		writeError(w, http.StatusBadGateway, "OIDC authorization claim is malformed")
		return
	}
	if oidcUser.GroupsClaimMalformed && s.cfg.OAuth.OIDC.RequireValidGroups {
		reqLog(r).Warn("oidc_groups_claim_malformed_refused", "sub", oidcUser.Sub)
		writeError(w, http.StatusBadGateway, "OIDC groups claim is malformed")
		return
	}

	if s.cfg.OAuth.OIDC.RequireGroups && len(oidcUser.Groups) == 0 {
		s.auditOIDCRefusal(r, "groups_required", oidcUser.Sub)
		writeError(w, http.StatusForbidden, "OIDC sign-in requires an assigned role or group")
		return
	}
	username, err := oidcUser.Username(s.cfg.OAuth.OIDC.UsernameClaim, s.cfg.OAuth.OIDC.UsernameRequired, s.cfg.OAuth.OIDC.UsernameStripDomain)
	if err != nil {
		s.auditOIDCRefusal(r, "username_claim_invalid", oidcUser.Sub)
		writeError(w, http.StatusForbidden, err.Error())
		return
	}
	claimUsername := username
	if username == "" {
		username = deriveOIDCUsername(oidcUser)
	}
	if err := s.store.BindOIDCProvider(s.oidcProvider.IssuerURL, s.oidcProvider.ClientID); err != nil {
		reqLog(r).Error("oidc_authority_refused")
		writeError(w, http.StatusServiceUnavailable, "OIDC authority requires administrator migration")
		return
	}
	if s.cfg.OAuth.OIDC.LinkExisting == "username" && claimUsername != "" {
		_, lookupErr := s.store.GetUserByOAuthAccount("oidc", oidcUser.Sub)
		if lookupErr != nil && !errors.Is(lookupErr, db.ErrNotFound) {
			writeInternal(w, r, "could not look up OIDC identity", lookupErr)
			return
		}
		if errors.Is(lookupErr, db.ErrNotFound) {
			linkedUser, linked, err := s.store.LinkOIDCUserAutomatically(claimUsername, oidcUser.Sub)
			if errors.Is(err, db.ErrOIDCLinkConflict) {
				s.auditOIDCRefusal(r, "link_conflict", oidcUser.Sub)
				writeError(w, http.StatusConflict, "existing account requires administrator linking")
				return
			}
			if err != nil && !errors.Is(err, db.ErrNotFound) {
				writeInternal(w, r, "could not link OIDC identity", err)
				return
			}
			if linked {
				s.logAuditEvent(r, db.AuditEventParams{Action: "link_oidc", ResourceType: "user", UserID: &linkedUser.ID, ResourceID: linkedUser.Username, IPAddress: s.ClientIP(r), Detail: db.AuditDetail(map[string]any{"method": "username", "issuer": s.oidcProvider.IssuerURL, "sub": oidcUser.Sub})})
			}
		}
	}
	candidates := []string{username, username + "-" + oidcUser.Sub[:min(8, len(oidcUser.Sub))], username + "-oidc"}
	if claimUsername != "" {
		candidates = []string{username}
	}
	user, created, err := s.store.ProvisionOAuthUser(db.ProvisionOAuthUserParams{
		Provider:                "oidc",
		ProviderID:              oidcUser.Sub,
		UsernameCandidates:      candidates,
		CaseInsensitiveUsername: claimUsername != "",
		Role:                    s.jitOAuthRole(),
	})
	if err != nil {
		if errors.Is(err, db.ErrReservedUsername) {
			reqLog(r).Warn("oidc_reserved_identity_refused")
			writeError(w, http.StatusForbidden, "this identity cannot sign in")
			return
		}
		if errors.Is(err, db.ErrUsernameExists) {
			s.auditOIDCRefusal(r, "username_in_use", oidcUser.Sub)
			writeError(w, http.StatusConflict, "OIDC username is already in use; administrator linking is required")
			return
		}
		reqLog(r).Error("oidc_provision_user_failed", "err", err)
		writeInternal(w, r, "internal server error", err)
		return
	}
	if created {
		s.logAuditEvent(r, db.AuditEventParams{
			UserID: &user.ID, Action: "create_user", ResourceType: "user",
			ResourceID: user.Username, IPAddress: s.ClientIP(r),
			Detail: db.AuditDetail(map[string]any{"provider": providerOIDC, "role": user.Role}),
		})
	}
	// Refresh the IdP-governed display name from the OIDC name claim. No-op for
	// accounts with a local password (self-managed) or when the claim is absent.
	if err := s.store.SetDisplayNameFromIdP(user.ID, oidcUser.Name); err != nil {
		reqLog(r).Warn("oidc_display_name_failed", "err", err)
	}
	// Persist the IdP email so native session requests forward X-Shinyhub-Email.
	// No-op for local-password accounts or when the email claim is absent.
	if err := s.store.SetEmailFromIdP(user.ID, oidcUser.Email); err != nil {
		reqLog(r).Warn("oidc_email_failed", "err", err)
	}

	// Authoritative role reconciliation from IdP groups. Only when the provider
	// actually sent the groups claim - an absent claim must not demote the user.
	if oidcUser.GroupsClaimMalformed {
		reqLog(r).Warn("oidc_groups_claim_unparseable_skipped", "username", user.Username)
	}
	if oidcUser.GroupsClaimPresent {
		if err := s.store.ReconcileUserFromGroups(
			user.ID, oidcUser.Groups,
			AuthMappings(s.cfg.Auth.GroupRoleMappings), s.jitOAuthRole(),
		); err != nil {
			reqLog(r).Error("oidc_reconcile_groups_failed", "username", user.Username, "err", err)
			writeInternal(w, r, "internal server error", err)
			return
		}
		// Re-read so the issued JWT carries the reconciled role. A read failure
		// here must not issue a session with a stale role claim.
		fresh, err := s.store.GetUserByID(user.ID)
		if err != nil {
			reqLog(r).Error("oidc_reread_user_failed", "username", user.Username, "err", err)
			writeInternal(w, r, "internal server error", err)
			return
		}
		if s.cfg.OAuth.OIDC.MissingGroups != "" && s.cfg.OAuth.OIDC.MissingGroups != "preserve" && fresh.ManualRole == "" {
			expected, matched := auth.ResolveGlobalRole(oidcUser.Groups, AuthMappings(s.cfg.Auth.GroupRoleMappings), s.jitOAuthRole())
			if !matched {
				expected = s.jitOAuthRole()
			}
			if fresh.Role != expected {
				reqLog(r).Warn("oidc_role_reconciliation_refused")
				s.logAuditEvent(r, db.AuditEventParams{UserID: &fresh.ID, Action: "login_refused", ResourceType: "user", ResourceID: fresh.Username, IPAddress: s.ClientIP(r), Detail: db.AuditDetail(map[string]any{"reason": "last_admin_reconciliation_guard"})})
				writeError(w, http.StatusForbidden, "administrator recovery is required before OIDC access can be reconciled")
				return
			}
		}
		user = fresh
	}

	authTime := time.Now().Truncate(time.Second)
	var sessionID string
	if s.cfg.OAuth.OIDC.EndSession {
		sessionID, err = s.saveOIDCSession(user.ID, oidcUser, tok, authTime)
		if err != nil {
			writeInternal(w, r, "could not save OIDC logout session", err)
			return
		}
	}
	_, err = s.setBrowserSession(w, r, user.ContextUser(), authTime, sessionID)
	if err != nil {
		writeInternal(w, r, "internal server error", err)
		return
	}

	s.logAuditEvent(r, db.AuditEventParams{
		UserID: &user.ID, Action: "login", ResourceType: "user",
		ResourceID: user.Username, IPAddress: s.ClientIP(r),
		Detail: db.AuditDetail(map[string]any{"grant": grantSessionCookie, "provider": providerOIDC}),
	})
	http.Redirect(w, r, oauthReturnPath(returnPath), http.StatusFound)
}

// deriveOIDCUsername returns a stable, URL-safe username derived from the
// OIDC user's available claims. Priority: email local-part → lowercased name
// with spaces replaced by hyphens → "oidc-" + first 16 chars of sub.
// Only alphanumeric characters and hyphens are kept; length is capped at 64.
func deriveOIDCUsername(u *oauth.OIDCUser) string {
	var base string
	if u.Email != "" {
		if at := strings.IndexByte(u.Email, '@'); at > 0 {
			base = u.Email[:at]
		} else {
			base = u.Email
		}
	} else if u.Name != "" {
		base = strings.ToLower(strings.ReplaceAll(u.Name, " ", "-"))
	} else if len(u.Sub) > 16 {
		return "oidc-" + u.Sub[:16]
	} else {
		return "oidc-" + u.Sub
	}
	// Keep only alphanumeric and hyphens, cap at 64 chars.
	var b strings.Builder
	for _, r := range base {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' {
			b.WriteRune(r)
		} else if r >= 'A' && r <= 'Z' {
			b.WriteRune(r + 32) // lowercase
		}
		if b.Len() >= 64 {
			break
		}
	}
	if b.Len() == 0 {
		return "oidc-user"
	}
	return b.String()
}
