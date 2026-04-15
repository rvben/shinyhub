package api

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"strings"

	"github.com/rvben/shinyhub/internal/auth"
	"github.com/rvben/shinyhub/internal/db"
)

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

	if err := s.store.ConsumeOAuthState(state); err != nil {
		writeError(w, http.StatusBadRequest, "invalid or expired state")
		return
	}

	tok, err := s.github.Exchange(r.Context(), code)
	if err != nil {
		fmt.Fprintf(os.Stderr, "github exchange: %v\n", err)
		writeError(w, http.StatusBadGateway, "OAuth exchange failed")
		return
	}

	ghUser, err := s.github.FetchUser(r.Context(), tok)
	if err != nil {
		fmt.Fprintf(os.Stderr, "github fetch user: %v\n", err)
		writeError(w, http.StatusBadGateway, "failed to fetch GitHub user")
		return
	}

	providerID := strconv.FormatInt(ghUser.ID, 10)

	user, err := s.store.GetUserByOAuthAccount("github", providerID)
	if errors.Is(err, db.ErrNotFound) {
		username := ghUser.Login
		if err := s.store.CreateUser(db.CreateUserParams{
			Username:     username,
			PasswordHash: "",
			Role:         "developer",
		}); err != nil {
			// Username collision — append GitHub ID to make it unique.
			username = fmt.Sprintf("%s-gh%s", ghUser.Login, providerID)
			if err2 := s.store.CreateUser(db.CreateUserParams{
				Username:     username,
				PasswordHash: "",
				Role:         "developer",
			}); err2 != nil {
				fmt.Fprintf(os.Stderr, "create oauth user: %v\n", err2)
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
			Provider:   "github",
			ProviderID: providerID,
		}); err != nil {
			fmt.Fprintf(os.Stderr, "create oauth account: %v\n", err)
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

	if err := s.store.ConsumeOAuthState(state); err != nil {
		writeError(w, http.StatusBadRequest, "invalid or expired state")
		return
	}

	tok, err := s.googleOAuth.Exchange(r.Context(), code)
	if err != nil {
		fmt.Fprintf(os.Stderr, "google exchange: %v\n", err)
		writeError(w, http.StatusBadGateway, "OAuth exchange failed")
		return
	}

	gUser, err := s.googleOAuth.FetchUser(r.Context(), tok)
	if err != nil {
		fmt.Fprintf(os.Stderr, "google fetch user: %v\n", err)
		writeError(w, http.StatusBadGateway, "failed to fetch Google user")
		return
	}

	user, err := s.store.GetUserByOAuthAccount("google", gUser.ID)
	if errors.Is(err, db.ErrNotFound) {
		// Derive username from the email local part (before @).
		at := strings.IndexByte(gUser.Email, '@')
		username := gUser.Email
		if at > 0 {
			username = gUser.Email[:at]
		}
		if username == "" {
			username = "google-user"
		}

		if err := s.store.CreateUser(db.CreateUserParams{
			Username:     username,
			PasswordHash: "",
			Role:         "developer",
		}); err != nil {
			// Username collision — append Google ID suffix to make it unique.
			username = username + "-g" + gUser.ID
			if err2 := s.store.CreateUser(db.CreateUserParams{
				Username:     username,
				PasswordHash: "",
				Role:         "developer",
			}); err2 != nil {
				fmt.Fprintf(os.Stderr, "create google oauth user: %v\n", err2)
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
			Provider:   "google",
			ProviderID: gUser.ID,
		}); err != nil {
			fmt.Fprintf(os.Stderr, "create google oauth account: %v\n", err)
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
