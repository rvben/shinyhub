package api

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/rvben/shinyhub/internal/auth"
	"github.com/rvben/shinyhub/internal/db"
)

// keyedRateLimiter is a simple sliding-window in-memory rate limiter keyed by
// an arbitrary identifier (IP, user ID, etc.).
type keyedRateLimiter struct {
	mu      sync.Mutex
	windows map[string][]time.Time
	limit   int
	window  time.Duration
}

func newKeyedRateLimiter(limit int, window time.Duration) *keyedRateLimiter {
	return &keyedRateLimiter{
		windows: make(map[string][]time.Time),
		limit:   limit,
		window:  window,
	}
}

// allow returns true if the request from key is within the rate limit.
func (rl *keyedRateLimiter) allow(key string) bool {
	rl.mu.Lock()
	defer rl.mu.Unlock()

	now := time.Now()
	cutoff := now.Add(-rl.window)

	reqs := rl.windows[key]
	var recent []time.Time
	for _, t := range reqs {
		if t.After(cutoff) {
			recent = append(recent, t)
		}
	}

	if len(recent) >= rl.limit {
		rl.windows[key] = recent
		return false
	}

	rl.windows[key] = append(recent, now)
	return true
}

// loginRateLimiter retains its name for readability at existing call sites.
type loginRateLimiter = keyedRateLimiter

func newLoginRateLimiter(limit int, window time.Duration) *loginRateLimiter {
	return newKeyedRateLimiter(limit, window)
}

// dummyHash is a pre-computed bcrypt hash used to ensure constant-time
// response for unknown usernames, preventing timing-based enumeration.
var dummyHash, _ = auth.HashPassword("dummy-sentinel-do-not-use")

type loginRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

type loginResponse struct {
	Token string               `json:"token"`
	User  *sessionUserResponse `json:"user"`
}

type sessionUserResponse struct {
	ID       int64  `json:"id"`
	Username string `json:"username"`
	Role     string `json:"role"`
}

type sessionResponse struct {
	User          *sessionUserResponse `json:"user"`
	CanCreateApps bool                 `json:"can_create_apps"`
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
	if !s.loginLimiter.allow(s.clientIP(r)) {
		writeError(w, http.StatusTooManyRequests, "too many login attempts, try again later")
		return
	}
	var req loginRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "bad request")
		return
	}

	user, err := s.authenticateCredentials(req)
	if err != nil {
		if errors.Is(err, db.ErrNotFound) {
			s.store.LogAuditEvent(db.AuditEventParams{
				Action:       "login_failed",
				ResourceType: "user",
				ResourceID:   req.Username,
				IPAddress:    s.clientIP(r),
			})
			writeError(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		writeError(w, http.StatusInternalServerError, "internal server error")
		return
	}

	token, err := auth.IssueJWT(user.ID, user.Username, user.Role, s.cfg.Auth.Secret)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal server error")
		return
	}

	s.store.LogAuditEvent(db.AuditEventParams{
		UserID:       &user.ID,
		Action:       "login",
		ResourceType: "user",
		ResourceID:   user.Username,
		IPAddress:    s.clientIP(r),
	})
	writeJSON(w, http.StatusOK, loginResponse{
		Token: token,
		User:  &sessionUserResponse{ID: user.ID, Username: user.Username, Role: user.Role},
	})
}

func (s *Server) handleSessionLogin(w http.ResponseWriter, r *http.Request) {
	if !s.loginLimiter.allow(s.clientIP(r)) {
		writeError(w, http.StatusTooManyRequests, "too many login attempts, try again later")
		return
	}
	var req loginRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "bad request")
		return
	}

	user, err := s.authenticateCredentials(req)
	if err != nil {
		if errors.Is(err, db.ErrNotFound) {
			s.store.LogAuditEvent(db.AuditEventParams{
				Action:       "login_failed",
				ResourceType: "user",
				ResourceID:   req.Username,
				IPAddress:    s.clientIP(r),
			})
			writeError(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		writeError(w, http.StatusInternalServerError, "internal server error")
		return
	}

	token, err := auth.IssueJWT(user.ID, user.Username, user.Role, s.cfg.Auth.Secret)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal server error")
		return
	}

	s.store.LogAuditEvent(db.AuditEventParams{
		UserID:       &user.ID,
		Action:       "login",
		ResourceType: "user",
		ResourceID:   user.Username,
		IPAddress:    s.clientIP(r),
	})
	auth.SetSessionCookie(w, r, token)
	ctxUser := &auth.ContextUser{ID: user.ID, Username: user.Username, Role: user.Role}
	writeJSON(w, http.StatusOK, sessionResponse{
		User:          &sessionUserResponse{ID: user.ID, Username: user.Username, Role: user.Role},
		CanCreateApps: canCreateApps(ctxUser),
	})
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	u := auth.UserFromContext(r.Context())
	if u != nil {
		// Revoke the caller's own JWT so it cannot be reused for the remainder
		// of its signed lifetime. Only JWT-authenticated requests populate
		// TokenInfo; API-key callers have no jti to revoke.
		if t := auth.TokenInfoFromContext(r.Context()); t != nil && t.JTI != "" {
			if err := s.store.RevokeToken(t.JTI, u.ID, t.ExpiresAt); err != nil {
				slog.Warn("revoke token on logout", "user", u.Username, "err", err)
			}
		}
		s.store.LogAuditEvent(db.AuditEventParams{
			UserID:       &u.ID,
			Action:       "logout",
			ResourceType: "user",
			ResourceID:   u.Username,
			IPAddress:    s.clientIP(r),
		})
	}
	auth.ClearSessionCookie(w, r)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleMe(w http.ResponseWriter, r *http.Request) {
	u := auth.UserFromContext(r.Context())
	if u == nil {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	// Slide the session window: only refresh when the request arrived via the
	// session cookie (Bearer-token callers do not need a cookie response).
	if _, err := r.Cookie(auth.SessionCookieName); err == nil {
		freshToken, err := auth.IssueJWT(u.ID, u.Username, u.Role, s.cfg.Auth.Secret)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "internal server error")
			return
		}
		auth.SetSessionCookie(w, r, freshToken)
	}
	writeJSON(w, http.StatusOK, sessionResponse{
		User:          &sessionUserResponse{ID: u.ID, Username: u.Username, Role: u.Role},
		CanCreateApps: canCreateApps(u),
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
		writeError(w, http.StatusBadRequest, "bad request")
		return
	}
	if req.Name == "" {
		writeError(w, http.StatusBadRequest, "name is required")
		return
	}

	u := auth.UserFromContext(r.Context())
	if u == nil {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}

	exists, err := s.store.APIKeyNameExists(u.ID, req.Name)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal server error")
		return
	}
	if exists {
		writeError(w, http.StatusConflict, "token name already in use")
		return
	}

	rawKey, keyHash, err := generateAPIKey()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal server error")
		return
	}

	if err := s.store.CreateAPIKey(db.CreateAPIKeyParams{
		UserID:  u.ID,
		KeyHash: keyHash,
		Name:    req.Name,
	}); err != nil {
		writeError(w, http.StatusInternalServerError, "internal server error")
		return
	}

	s.store.LogAuditEvent(db.AuditEventParams{
		UserID:       &u.ID,
		Action:       "create_token",
		ResourceType: "token",
		ResourceID:   req.Name,
		IPAddress:    s.clientIP(r),
	})
	writeJSON(w, http.StatusCreated, createTokenResponse{Token: rawKey})
}

func (s *Server) handleListTokens(w http.ResponseWriter, r *http.Request) {
	u := auth.UserFromContext(r.Context())
	if u == nil {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}

	keys, err := s.store.ListAPIKeys(u.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal server error")
		return
	}
	writeJSON(w, http.StatusOK, keys)
}

func (s *Server) handleDeleteToken(w http.ResponseWriter, r *http.Request) {
	u := auth.UserFromContext(r.Context())
	if u == nil {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}

	idStr := chi.URLParam(r, "id")
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid token id")
		return
	}

	// Admins bypass ownership check (ownerID=0); others can only delete their own.
	ownerID := u.ID
	if u.Role == "admin" {
		ownerID = 0
	}

	if err := s.store.DeleteAPIKey(id, ownerID); err != nil {
		if errors.Is(err, db.ErrNotFound) {
			writeError(w, http.StatusNotFound, "token not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "internal server error")
		return
	}

	s.store.LogAuditEvent(db.AuditEventParams{
		UserID:       &u.ID,
		Action:       "delete_token",
		ResourceType: "token",
		ResourceID:   strconv.FormatInt(id, 10),
		IPAddress:    s.clientIP(r),
	})
	w.WriteHeader(http.StatusNoContent)
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
