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

		if err := s.store.CreateUser(db.CreateUserParams{
			Username:     username,
			PasswordHash: "",
			Role:         "developer",
		}); err != nil {
			// Username collision — append "-oidc" suffix to make it unique.
			username = username + "-oidc"
			if err2 := s.store.CreateUser(db.CreateUserParams{
				Username:     username,
				PasswordHash: "",
				Role:         "developer",
			}); err2 != nil {
				fmt.Fprintf(os.Stderr, "create oidc user: %v\n", err2)
				writeError(w, http.StatusInternalServerError, "internal server error")
				return
			}
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
	http.Redirect(w, r, "/", http.StatusFound)
}

// deriveOIDCUsername returns a stable, URL-safe username derived from the
// OIDC user's available claims. Priority: email local-part → name with spaces
// replaced by hyphens → "oidc-" + first 16 chars of sub.
func deriveOIDCUsername(u *oauth.OIDCUser) string {
	if u.Email != "" {
		if at := strings.IndexByte(u.Email, '@'); at > 0 {
			return u.Email[:at]
		}
		return u.Email
	}
	if u.Name != "" {
		return strings.ReplaceAll(u.Name, " ", "-")
	}
	sub := u.Sub
	if len(sub) > 16 {
		sub = sub[:16]
	}
	return "oidc-" + sub
}
