package api

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"

	"github.com/rvben/shinyhub/internal/auth"
	"github.com/rvben/shinyhub/internal/db"
	"github.com/rvben/shinyhub/internal/oauth"
)

// handleGetProviders returns a JSON object indicating which login providers
// are currently enabled on this server instance.
func (s *Server) handleGetProviders(w http.ResponseWriter, r *http.Request) {
	type oidcInfo struct {
		Enabled     bool   `json:"enabled"`
		DisplayName string `json:"display_name,omitempty"`
	}
	type response struct {
		GitHub bool     `json:"github"`
		Google bool     `json:"google"`
		OIDC   oidcInfo `json:"oidc"`
	}

	resp := response{
		GitHub: s.github != nil,
		Google: s.googleOAuth != nil,
		OIDC: oidcInfo{
			Enabled: s.oidcProvider != nil,
		},
	}
	if s.oidcProvider != nil {
		resp.OIDC.DisplayName = s.oidcProvider.DisplayName
	}

	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

// handleOIDCLogin redirects the browser to the OIDC identity provider's
// authorization endpoint.
func (s *Server) handleOIDCLogin(w http.ResponseWriter, r *http.Request) {
	if s.oidcProvider == nil {
		writeError(w, http.StatusNotImplemented, "OIDC SSO not configured")
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

	http.Redirect(w, r, s.oidcProvider.AuthURL(state), http.StatusFound)
}

// handleOIDCCallback handles the OIDC authorization callback, verifies the
// ID token, and creates or finds the matching local user account before
// issuing a session JWT.
func (s *Server) handleOIDCCallback(w http.ResponseWriter, r *http.Request) {
	if s.oidcProvider == nil {
		writeError(w, http.StatusNotImplemented, "OIDC SSO not configured")
		return
	}

	state := r.URL.Query().Get("state")
	code := r.URL.Query().Get("code")
	if state == "" || code == "" {
		writeError(w, http.StatusBadRequest, "missing state or code")
		return
	}

	if err := s.store.ConsumeOAuthState(state); err != nil {
		writeError(w, http.StatusBadRequest, "invalid or expired state")
		return
	}

	tok, err := s.oidcProvider.Exchange(r.Context(), code)
	if err != nil {
		fmt.Fprintf(os.Stderr, "oidc exchange: %v\n", err)
		writeError(w, http.StatusBadGateway, "OAuth exchange failed")
		return
	}

	oidcUser, err := s.oidcProvider.VerifyIDToken(r.Context(), tok)
	if err != nil {
		fmt.Fprintf(os.Stderr, "oidc verify id_token: %v\n", err)
		writeError(w, http.StatusBadGateway, "failed to verify OIDC ID token")
		return
	}

	user, err := s.store.GetUserByOAuthAccount("oidc", oidcUser.Sub)
	if errors.Is(err, db.ErrNotFound) {
		username := deriveOIDCUsername(oidcUser)
		var createdUser bool
		for _, candidate := range []string{username, username + "-" + oidcUser.Sub[:min(8, len(oidcUser.Sub))], username + "-oidc"} {
			if err2 := s.store.CreateUser(db.CreateUserParams{
				Username:     candidate,
				PasswordHash: "",
				Role:         "developer",
			}); err2 != nil {
				fmt.Fprintf(os.Stderr, "oidc: create user %q: %v\n", candidate, err2)
				continue
			}
			username = candidate
			createdUser = true
			break
		}
		if !createdUser {
			writeError(w, http.StatusInternalServerError, "internal server error")
			return
		}
		user, err = s.store.GetUserByUsername(username)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "internal server error")
			return
		}
		if err := s.store.CreateOAuthAccount(db.CreateOAuthAccountParams{
			UserID:     user.ID,
			Provider:   "oidc",
			ProviderID: oidcUser.Sub,
		}); err != nil {
			fmt.Fprintf(os.Stderr, "create oidc oauth account: %v\n", err)
		}
		s.store.LogAuditEvent(db.AuditEventParams{
			UserID: &user.ID, Action: "create_user", ResourceType: "user",
			ResourceID: user.Username, IPAddress: clientIP(r),
		})
	} else if err != nil {
		writeError(w, http.StatusInternalServerError, "internal server error")
		return
	}

	jwtToken, err := auth.IssueJWT(user.ID, user.Username, user.Role, s.cfg.Auth.Secret)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal server error")
		return
	}

	auth.SetSessionCookie(w, r, jwtToken)
	s.store.LogAuditEvent(db.AuditEventParams{
		UserID: &user.ID, Action: "login", ResourceType: "user",
		ResourceID: user.Username, IPAddress: clientIP(r),
	})
	http.Redirect(w, r, "/", http.StatusFound)
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
