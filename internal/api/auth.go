package api

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"

	"github.com/rvben/shinyhub/internal/auth"
	"github.com/rvben/shinyhub/internal/db"
)

// dummyHash is a pre-computed bcrypt hash used to ensure constant-time
// response for unknown usernames, preventing timing-based enumeration.
var dummyHash, _ = auth.HashPassword("dummy-sentinel-do-not-use")

type loginRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

type loginResponse struct {
	Token string `json:"token"`
}

type sessionUserResponse struct {
	ID       int64  `json:"id"`
	Username string `json:"username"`
	Role     string `json:"role"`
}

type sessionResponse struct {
	User *sessionUserResponse `json:"user"`
}

func (s *Server) authenticateCredentials(req loginRequest) (*db.User, error) {
	user, err := s.store.GetUserByUsername(req.Username)
	if err != nil {
		if errors.Is(err, db.ErrNotFound) {
			auth.VerifyPassword(dummyHash, req.Password) // constant-time guard
			return nil, db.ErrNotFound
		}
		return nil, err
	}

	if err := auth.VerifyPassword(user.PasswordHash, req.Password); err != nil {
		return nil, db.ErrNotFound
	}

	return user, nil
}

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	var req loginRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}

	user, err := s.authenticateCredentials(req)
	if err != nil {
		if errors.Is(err, db.ErrNotFound) {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	if err := auth.VerifyPassword(user.PasswordHash, req.Password); err != nil {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	token, err := auth.IssueJWT(user.ID, user.Username, user.Role, s.cfg.Auth.Secret)
	if err != nil {
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	writeJSON(w, http.StatusOK, loginResponse{Token: token})
}

func (s *Server) handleSessionLogin(w http.ResponseWriter, r *http.Request) {
	var req loginRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}

	user, err := s.authenticateCredentials(req)
	if err != nil {
		if errors.Is(err, db.ErrNotFound) {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	token, err := auth.IssueJWT(user.ID, user.Username, user.Role, s.cfg.Auth.Secret)
	if err != nil {
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	auth.SetSessionCookie(w, r, token)
	writeJSON(w, http.StatusOK, sessionResponse{
		User: &sessionUserResponse{ID: user.ID, Username: user.Username, Role: user.Role},
	})
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	auth.ClearSessionCookie(w, r)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleMe(w http.ResponseWriter, r *http.Request) {
	u := auth.UserFromContext(r.Context())
	if u == nil {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	// Slide the session window: only refresh when the request arrived via the
	// session cookie (Bearer-token callers do not need a cookie response).
	if _, err := r.Cookie(auth.SessionCookieName); err == nil {
		freshToken, err := auth.IssueJWT(u.ID, u.Username, u.Role, s.cfg.Auth.Secret)
		if err != nil {
			http.Error(w, "internal server error", http.StatusInternalServerError)
			return
		}
		auth.SetSessionCookie(w, r, freshToken)
	}
	writeJSON(w, http.StatusOK, sessionResponse{
		User: &sessionUserResponse{ID: u.ID, Username: u.Username, Role: u.Role},
	})
}

type createTokenRequest struct {
	Name string `json:"name"`
}

type createTokenResponse struct {
	Token string `json:"token"`
}

func (s *Server) handleCreateToken(w http.ResponseWriter, r *http.Request) {
	var req createTokenRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	if req.Name == "" {
		http.Error(w, "name is required", http.StatusBadRequest)
		return
	}

	u := auth.UserFromContext(r.Context())
	if u == nil {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	rawKey, keyHash, err := generateAPIKey()
	if err != nil {
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	if err := s.store.CreateAPIKey(db.CreateAPIKeyParams{
		UserID:  u.ID,
		KeyHash: keyHash,
		Name:    req.Name,
	}); err != nil {
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	writeJSON(w, http.StatusCreated, createTokenResponse{Token: rawKey})
}

// generateAPIKey creates a cryptographically random 32-byte token and returns
// both the raw hex token (shown to the user once) and its SHA-256 hash (stored).
func generateAPIKey() (rawKey, keyHash string, err error) {
	buf := make([]byte, 32)
	if _, err = rand.Read(buf); err != nil {
		return "", "", err
	}
	rawKey = "shk_" + hex.EncodeToString(buf)
	keyHash = auth.HashAPIKey(rawKey)
	return rawKey, keyHash, nil
}
