package api

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/rvben/shinyhub/internal/auth"
	"github.com/rvben/shinyhub/internal/db"
	"github.com/rvben/shinyhub/internal/proxy"
)

// keyedRateLimiter is a simple sliding-window in-memory rate limiter keyed by
// an arbitrary identifier (IP, user ID, etc.).
//
// State is per-process and in-memory: limits are NOT shared across a
// multi-instance / load-balanced deployment, and they reset on restart. A
// caller running N replicas behind a balancer can therefore make up to N x
// limit requests per window in aggregate. This is acceptable for the abuse-
// dampening goal here (slow brute force / runaway clients); it is not a
// distributed quota. Run a single instance, or front it with a shared limiter
// (e.g. at the proxy), if a hard global cap is required.
type keyedRateLimiter struct {
	mu        sync.Mutex
	windows   map[string][]time.Time
	limit     int
	window    time.Duration
	lastSweep time.Time
}

func newKeyedRateLimiter(limit int, window time.Duration) *keyedRateLimiter {
	return &keyedRateLimiter{
		windows:   make(map[string][]time.Time),
		limit:     limit,
		window:    window,
		lastSweep: time.Now(),
	}
}

// sweep drops keys whose timestamps have all aged out. allow() only prunes
// the key it touches, so without this a long-lived process accumulates one
// map entry per distinct source IP / user ID forever. Caller must hold rl.mu.
func (rl *keyedRateLimiter) sweep(cutoff time.Time) {
	for k, ts := range rl.windows {
		live := false
		for _, t := range ts {
			if t.After(cutoff) {
				live = true
				break
			}
		}
		if !live {
			delete(rl.windows, k)
		}
	}
}

// allow returns true if the request from key is within the rate limit.
func (rl *keyedRateLimiter) allow(key string) bool {
	rl.mu.Lock()
	defer rl.mu.Unlock()

	now := time.Now()
	cutoff := now.Add(-rl.window)

	// Periodic global cleanup so idle keys cannot grow the map without
	// bound. Bounded to once per window: O(n) over keys, but n is already
	// pruned to the active set each sweep.
	if now.Sub(rl.lastSweep) >= rl.window {
		rl.sweep(cutoff)
		rl.lastSweep = now
	}

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

// rateLimiter reports whether a request identified by key may proceed. Both the
// in-memory keyedRateLimiter and the database-backed dbRateLimiter implement it,
// so the login handler is oblivious to which is wired.
type rateLimiter interface {
	allow(key string) bool
}

// dbRateLimiter is a shared, database-backed limiter. Its counts live in the
// rate_limit_attempts table, so every instance against the same database
// enforces one combined limit per key. This closes the multi-instance bypass of
// the in-memory limiter, where N replicas behind a balancer would otherwise
// allow N x limit attempts. Used for login on a Postgres (HA-capable) backend.
type dbRateLimiter struct {
	store  *db.Store
	bucket string
	limit  int
	window time.Duration
}

func (l *dbRateLimiter) allow(key string) bool {
	ok, err := l.store.RateLimitAllow(l.bucket, key, l.limit, l.window)
	if err != nil {
		// Fail open: a transient database error must not lock every user out of
		// login. The login handler still queries the database to verify the
		// password, so an attacker gains no advantage during the outage.
		slog.Warn("login rate limiter degraded; allowing request", "err", err)
		return true
	}
	return ok
}

// newLoginLimiter selects the limiter by backend: a shared database-backed
// limiter on Postgres (a load-balanced deployment must share the count across
// instances), or the in-memory limiter on SQLite (single instance, no DB round
// trip per attempt).
func newLoginLimiter(store *db.Store, limit int, window time.Duration) rateLimiter {
	if store != nil && store.IsPostgres() {
		return &dbRateLimiter{store: store, bucket: "login", limit: limit, window: window}
	}
	return newLoginRateLimiter(limit, window)
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
	ID          int64  `json:"id"`
	Username    string `json:"username"`
	Role        string `json:"role"`
	DisplayName string `json:"display_name"`
	// CanSetPassword is true for local accounts (a real bcrypt password is on
	// file) and false for SSO/forward-auth accounts. The profile UI uses it to
	// show the change-password fields only when they would actually work.
	CanSetPassword bool `json:"can_set_password"`
}

// newSessionUser builds the public session view from a full DB user record so
// the display name and password-management capability travel with every
// authenticated response.
func newSessionUser(u *db.User) *sessionUserResponse {
	return &sessionUserResponse{
		ID:             u.ID,
		Username:       u.Username,
		Role:           u.Role,
		DisplayName:    u.DisplayName,
		CanSetPassword: db.HasLocalPassword(u.PasswordHash),
	}
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
	if !s.loginLimiter.allow(s.ClientIP(r)) {
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
				IPAddress:    s.ClientIP(r),
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
		IPAddress:    s.ClientIP(r),
	})
	writeJSON(w, http.StatusOK, loginResponse{
		Token: token,
		User:  newSessionUser(user),
	})
}

func (s *Server) handleSessionLogin(w http.ResponseWriter, r *http.Request) {
	if !s.loginLimiter.allow(s.ClientIP(r)) {
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
				IPAddress:    s.ClientIP(r),
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
		IPAddress:    s.ClientIP(r),
	})
	auth.SetSessionCookie(w, r, token, s.cfg.TrustedProxyNets)
	ctxUser := &auth.ContextUser{ID: user.ID, Username: user.Username, Role: user.Role}
	writeJSON(w, http.StatusOK, sessionResponse{
		User:          newSessionUser(user),
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
			IPAddress:    s.ClientIP(r),
		})
	}
	auth.ClearSessionCookie(w, r, s.cfg.TrustedProxyNets)
	// Also expire the per-app proxy routing cookies (sticky replica + elastic
	// client-id) so a shared/kiosk browser does not route the next user to this
	// user's pinned replica or dedicated worker.
	proxy.ClearRoutingCookies(w, r)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleMe(w http.ResponseWriter, r *http.Request) {
	u := auth.UserFromContext(r.Context())
	if u == nil {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	// Slide the session window: only refresh when the request authenticated
	// via the session cookie. Authorization-header callers (Bearer JWT or
	// Token API key) take that branch first in AuthenticateRequest, so when
	// no header is present the user must have come from the cookie — which is
	// the only case where we should re-issue one.
	if r.Header.Get("Authorization") == "" {
		if _, err := r.Cookie(auth.SessionCookieName); err == nil {
			// Slide the 1h window, but preserve the original login time so the
			// session cannot be kept alive indefinitely. Past the absolute cap we
			// stop renewing; the current token then expires within its TTL and the
			// user re-logs in, which re-runs SSO group reconciliation (so a role
			// revoked at the IdP takes effect within the cap rather than never).
			var authTime time.Time
			if ti := auth.TokenInfoFromContext(r.Context()); ti != nil {
				authTime = ti.AuthTime
			}
			if auth.CanSlideSession(authTime) {
				if authTime.IsZero() {
					authTime = time.Now()
				}
				freshToken, err := auth.IssueJWTAt(u.ID, u.Username, u.Role, s.cfg.Auth.Secret, authTime)
				if err != nil {
					writeError(w, http.StatusInternalServerError, "internal server error")
					return
				}
				auth.SetSessionCookie(w, r, freshToken, s.cfg.TrustedProxyNets)
			}
		}
	}
	// Prefer the full DB record so display name + password capability ride
	// along. Fall back to the JWT claims if the read fails (e.g. the row was
	// removed mid-session) so /api/auth/me stays available for logout/redirect.
	su := &sessionUserResponse{ID: u.ID, Username: u.Username, Role: u.Role}
	if dbUser, err := s.store.GetUserByID(u.ID); err == nil {
		su = newSessionUser(dbUser)
	}
	writeJSON(w, http.StatusOK, sessionResponse{
		User:          su,
		CanCreateApps: canCreateApps(u),
	})
}

type patchMeRequest struct {
	// DisplayName is a pointer so the handler can tell "set to empty" (clear the
	// name, re-opening it to SSO backfill) apart from "field omitted".
	DisplayName     *string `json:"display_name"`
	CurrentPassword string  `json:"current_password"`
	NewPassword     string  `json:"new_password"`
}

// handlePatchMe lets the authenticated user edit their own profile: the
// friendly display name and, for local accounts, their password (verifying the
// current one first). SSO and system accounts cannot set a password here. It
// returns the refreshed session view so the dashboard can update the sidebar
// without a reload.
func (s *Server) handlePatchMe(w http.ResponseWriter, r *http.Request) {
	u := auth.UserFromContext(r.Context())
	if u == nil {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	// System accounts (e.g. __deploy__) are owned by env/config, not the UI.
	if db.IsSystemUser(u.Username) {
		writeError(w, http.StatusForbidden, "system users cannot edit their profile")
		return
	}

	var req patchMeRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "bad request")
		return
	}

	current, err := s.store.GetUserByID(u.ID)
	if err != nil {
		if errors.Is(err, db.ErrNotFound) {
			writeError(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		writeError(w, http.StatusInternalServerError, "internal server error")
		return
	}

	changedDisplayName := false
	if req.DisplayName != nil {
		// SSO accounts get their name from the identity provider (refreshed on
		// every login); only local accounts edit it here, mirroring the password
		// rule so a self-managed account and an IdP-managed one are unambiguous.
		if !db.HasLocalPassword(current.PasswordHash) {
			writeError(w, http.StatusForbidden, "display name is managed by your identity provider")
			return
		}
		name := strings.TrimSpace(*req.DisplayName)
		if len([]rune(name)) > 80 {
			writeError(w, http.StatusBadRequest, "display name must be at most 80 characters")
			return
		}
		if name != current.DisplayName {
			if err := s.store.UpdateUserDisplayName(u.ID, name); err != nil {
				writeError(w, http.StatusInternalServerError, "internal server error")
				return
			}
			changedDisplayName = true
		}
	}

	if req.NewPassword != "" {
		if !db.HasLocalPassword(current.PasswordHash) {
			writeError(w, http.StatusForbidden, "password is managed by your identity provider")
			return
		}
		if len(req.NewPassword) < 8 {
			writeError(w, http.StatusBadRequest, "password must be at least 8 characters")
			return
		}
		if err := auth.VerifyPassword(current.PasswordHash, req.CurrentPassword); err != nil {
			writeError(w, http.StatusUnauthorized, "current password is incorrect")
			return
		}
		hash, err := auth.HashPassword(req.NewPassword)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "internal server error")
			return
		}
		if err := s.store.UpdateUserPassword(u.ID, hash); err != nil {
			writeError(w, http.StatusInternalServerError, "internal server error")
			return
		}
		s.store.LogAuditEvent(db.AuditEventParams{
			UserID:       &u.ID,
			Action:       "change_own_password",
			ResourceType: "user",
			ResourceID:   u.Username,
			IPAddress:    s.ClientIP(r),
		})
	}

	if changedDisplayName {
		s.store.LogAuditEvent(db.AuditEventParams{
			UserID:       &u.ID,
			Action:       "update_profile",
			ResourceType: "user",
			ResourceID:   u.Username,
			IPAddress:    s.ClientIP(r),
		})
	}

	fresh, err := s.store.GetUserByID(u.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal server error")
		return
	}
	writeJSON(w, http.StatusOK, sessionResponse{
		User:          newSessionUser(fresh),
		CanCreateApps: canCreateApps(u),
	})
}

type createTokenRequest struct {
	Name string `json:"name"`
}

type createTokenResponse struct {
	ID        int64     `json:"id"`
	Name      string    `json:"name"`
	Token     string    `json:"token"`
	CreatedAt time.Time `json:"created_at"`
}

func (s *Server) handleCreateToken(w http.ResponseWriter, r *http.Request) {
	u := auth.UserFromContext(r.Context())
	if u == nil {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}

	if db.IsSystemUser(u.Username) {
		writeError(w, http.StatusForbidden, "system users cannot create persistent tokens")
		return
	}

	var req createTokenRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "bad request")
		return
	}
	if req.Name == "" {
		writeError(w, http.StatusBadRequest, "name is required")
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

	keyID, createdAt, err := s.store.CreateAPIKey(db.CreateAPIKeyParams{
		UserID:  u.ID,
		KeyHash: keyHash,
		Name:    req.Name,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal server error")
		return
	}

	s.store.LogAuditEvent(db.AuditEventParams{
		UserID:       &u.ID,
		Action:       "create_token",
		ResourceType: "token",
		ResourceID:   req.Name,
		IPAddress:    s.ClientIP(r),
	})
	writeJSON(w, http.StatusCreated, createTokenResponse{
		ID:        keyID,
		Name:      req.Name,
		Token:     rawKey,
		CreatedAt: createdAt,
	})
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
		IPAddress:    s.ClientIP(r),
	})
	w.WriteHeader(http.StatusNoContent)
}

// handleSessionHandoff terminates the current browser session server-side and
// redirects to the login form. Used by the access-denied 403 page so a user
// signed in to the wrong account can switch users in one click.
//
// This endpoint is registered OUTSIDE the bearer+CSRF middleware group: the
// 403 page is rendered by an unauthenticated context (well, an *insufficiently*
// authenticated one), and crucially the page may be opened in a fresh tab
// where the SPA hasn't bootstrapped — so it has no CSRF token to send. The
// previous design (a GET-driven /?logout=1 link gated by an onclick
// sessionStorage marker) only worked when the click happened in the same tab
// the marker was planted in; Cmd+Click / Ctrl+Click on the link opened a new
// tab where the marker was missing, the SPA refused to log out, and the user
// bounced straight back to the same 403 page.
//
// Defence against cross-site forgery: we require Origin or Referer to match
// our own host. A malicious site can POST to us, but the browser will either
// attach a third-party Origin header (which we reject) or — if neither header
// is sent — we reject the request outright. This is the same pattern Django,
// Rails et al. use for their double-submit-cookie escape hatch.
func (s *Server) handleSessionHandoff(w http.ResponseWriter, r *http.Request) {
	if !s.sameOriginPost(r) {
		writeError(w, http.StatusForbidden, "cross-origin handoff rejected")
		return
	}

	// Best-effort: revoke the JWT so it can't be reused for the rest of its
	// signed lifetime. A bad/expired/missing cookie is fine — we still clear
	// it and redirect; the goal is "next request starts unauthenticated", not
	// "we successfully revoked something specific".
	if c, err := r.Cookie(auth.SessionCookieName); err == nil && c.Value != "" {
		if claims, err := auth.ValidateJWT(c.Value, s.cfg.Auth.Secret, s.revocationChecker()); err == nil {
			expiry := time.Time{}
			if claims.ExpiresAt != nil {
				expiry = claims.ExpiresAt.Time
			}
			if err := s.store.RevokeToken(claims.ID, claims.UserID, expiry); err != nil {
				slog.Warn("revoke token on handoff", "user", claims.Subject, "err", err)
			}
			s.store.LogAuditEvent(db.AuditEventParams{
				UserID:       &claims.UserID,
				Action:       "logout_handoff",
				ResourceType: "user",
				ResourceID:   claims.Subject,
				IPAddress:    s.ClientIP(r),
			})
		}
	}

	auth.ClearSessionCookie(w, r, s.cfg.TrustedProxyNets)

	target := "/login"
	if next := safeNextPath(r.FormValue("next")); next != "" {
		target = "/login?" + url.Values{"next": {next}}.Encode()
	}
	http.Redirect(w, r, target, http.StatusSeeOther)
}

// sameOriginPost reports whether the request appears to come from our own
// origin. We require either Origin or Referer to be present and to match the
// request's effective host. Browsers attach Origin to all unsafe cross-origin
// requests, so a third-party POST from evil.example.com will either carry
// `Origin: https://evil.example.com` (rejected) or — if Origin is suppressed —
// a `Referer: https://evil.example.com/...` (also rejected). A request with
// neither header is rejected too; that closes the gap where a privacy-focused
// extension strips both.
//
// Comparison uses effectiveHost, not r.Host, so the check works behind a
// reverse proxy. Behind nginx/Caddy/Traefik the inbound TCP connection
// terminates at the proxy, so r.Host is whatever the proxy addressed us at
// (often 127.0.0.1:<port> or a Unix socket alias) — never the public hostname
// the browser put in Origin. effectiveHost trusts X-Forwarded-Host only when
// the direct peer is in TrustedProxyNets, so an attacker who can reach us
// directly cannot fake the header to bypass this check.
func (s *Server) sameOriginPost(r *http.Request) bool {
	host := s.effectiveHost(r)
	if origin := r.Header.Get("Origin"); origin != "" {
		u, err := url.Parse(origin)
		if err != nil || u.Host == "" {
			return false
		}
		return strings.EqualFold(u.Host, host)
	}
	if referer := r.Header.Get("Referer"); referer != "" {
		u, err := url.Parse(referer)
		if err != nil || u.Host == "" {
			return false
		}
		return strings.EqualFold(u.Host, host)
	}
	return false
}

// safeNextPath returns raw if it looks like a same-origin path the SPA can
// safely redirect to, otherwise "". Mirrors the SPA's consumeNextParam
// validation (relative path starting with a single `/`, no `//` protocol-
// relative form, no `\` Windows separator, not the bare `/` or `/login`).
func safeNextPath(raw string) string {
	if raw == "" {
		return ""
	}
	if !strings.HasPrefix(raw, "/") || strings.HasPrefix(raw, "//") || strings.Contains(raw, "\\") {
		return ""
	}
	if raw == "/" || raw == "/login" {
		return ""
	}
	return raw
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
