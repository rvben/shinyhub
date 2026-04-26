package api_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/rvben/shinyhub/internal/api"
	"github.com/rvben/shinyhub/internal/auth"
	"github.com/rvben/shinyhub/internal/config"
	"github.com/rvben/shinyhub/internal/db"
)

// buildStaleJWT creates a valid but old JWT for testing session refresh.
// The token was "issued" 30 minutes ago with a 1-hour expiry, so it still
// authenticates but its IssuedAt is clearly in the past.
func buildStaleJWT(userID int64, username, role, secret string) (token string, issuedAt time.Time) {
	issuedAt = time.Now().Add(-30 * time.Minute)
	claims := auth.Claims{
		UserID: userID,
		Role:   role,
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   username,
			IssuedAt:  jwt.NewNumericDate(issuedAt),
			ExpiresAt: jwt.NewNumericDate(issuedAt.Add(1 * time.Hour)),
		},
	}
	t := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	token, _ = t.SignedString([]byte(secret))
	return token, issuedAt
}

func newTestServer(t *testing.T) (*api.Server, *db.Store) {
	t.Helper()
	store, err := db.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Migrate(); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{
		Auth:    config.AuthConfig{Secret: "test-secret"},
		Storage: config.StorageConfig{AppsDir: t.TempDir(), AppDataDir: t.TempDir()},
	}
	srv := api.New(cfg, store, nil, nil) // no manager/proxy for auth tests
	t.Cleanup(func() { store.Close() })
	return srv, store
}

// seedUserAndJWT creates a user in the test store and returns a JWT bound to
// the user's actual database ID. BearerMiddleware re-resolves users against
// the live DB on every request, so handing out JWTs for IDs that don't exist
// in the store now returns 401. Tests that need an authenticated request
// should mint their token via this helper.
func seedUserAndJWT(t *testing.T, store *db.Store, username, role string) (token string, userID int64) {
	t.Helper()
	hash, err := auth.HashPassword("seed-password")
	if err != nil {
		t.Fatalf("hash password: %v", err)
	}
	if err := store.CreateUser(db.CreateUserParams{Username: username, PasswordHash: hash, Role: role}); err != nil {
		t.Fatalf("create user: %v", err)
	}
	u, err := store.GetUserByUsername(username)
	if err != nil {
		t.Fatalf("lookup user: %v", err)
	}
	token, err = auth.IssueJWT(u.ID, u.Username, u.Role, "test-secret")
	if err != nil {
		t.Fatalf("issue jwt: %v", err)
	}
	return token, u.ID
}

// setCSRF attaches a matching csrf_token cookie and X-CSRF-Token header so a
// request using session-cookie auth can pass the CSRF middleware in tests.
func setCSRF(req *http.Request) {
	req.AddCookie(&http.Cookie{Name: "csrf_token", Value: "test-csrf-token"})
	req.Header.Set("X-CSRF-Token", "test-csrf-token")
}

func TestLogin(t *testing.T) {
	srv, store := newTestServer(t)
	hash, _ := auth.HashPassword("pass123")
	store.CreateUser(db.CreateUserParams{Username: "alice", PasswordHash: hash, Role: "admin"})

	body, _ := json.Marshal(map[string]string{"username": "alice", "password": "pass123"})
	req := httptest.NewRequest("POST", "/api/auth/login", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)

	if rec.Code != 200 {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var resp struct {
		Token string `json:"token"`
		User  *struct {
			ID       int64  `json:"id"`
			Username string `json:"username"`
			Role     string `json:"role"`
		} `json:"user"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.Token == "" {
		t.Error("expected token in response")
	}
	if resp.User == nil {
		t.Fatal("expected user object in response")
	}
	if resp.User.Username != "alice" {
		t.Errorf("user.username = %q, want %q", resp.User.Username, "alice")
	}
	if resp.User.Role != "admin" {
		t.Errorf("user.role = %q, want %q", resp.User.Role, "admin")
	}
}

func TestLoginWrongPassword(t *testing.T) {
	srv, store := newTestServer(t)
	hash, _ := auth.HashPassword("pass123")
	store.CreateUser(db.CreateUserParams{Username: "alice", PasswordHash: hash, Role: "admin"})

	body, _ := json.Marshal(map[string]string{"username": "alice", "password": "wrong"})
	req := httptest.NewRequest("POST", "/api/auth/login", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)

	if rec.Code != 401 {
		t.Errorf("expected 401, got %d", rec.Code)
	}
}

func TestSessionLoginSetsHttpOnlyCookie(t *testing.T) {
	srv, store := newTestServer(t)
	hash, _ := auth.HashPassword("pass123")
	store.CreateUser(db.CreateUserParams{Username: "alice", PasswordHash: hash, Role: "admin"})

	body, _ := json.Marshal(map[string]string{"username": "alice", "password": "pass123"})
	req := httptest.NewRequest("POST", "/api/auth/session", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	resp := rec.Result()
	defer resp.Body.Close()
	cookies := resp.Cookies()
	if len(cookies) == 0 {
		t.Fatal("expected session cookie")
	}
	if cookies[0].Name != auth.SessionCookieName {
		t.Fatalf("expected cookie %q, got %q", auth.SessionCookieName, cookies[0].Name)
	}
	if !cookies[0].HttpOnly {
		t.Error("expected HttpOnly session cookie")
	}

	var payload struct {
		User *struct {
			ID       int64  `json:"id"`
			Username string `json:"username"`
			Role     string `json:"role"`
		} `json:"user"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if payload.User == nil || payload.User.Username != "alice" {
		t.Fatalf("unexpected user payload: %+v", payload.User)
	}
}

func TestMeUsesSessionCookie(t *testing.T) {
	srv, store := newTestServer(t)
	token, _ := seedUserAndJWT(t, store, "alice", "admin")

	req := httptest.NewRequest("GET", "/api/auth/me", nil)
	req.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: token})
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var payload struct {
		User *struct {
			ID       int64  `json:"id"`
			Username string `json:"username"`
			Role     string `json:"role"`
		} `json:"user"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&payload); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if payload.User == nil || payload.User.Username != "alice" {
		t.Fatalf("unexpected user payload: %+v", payload.User)
	}
}

func TestLogoutClearsSessionCookie(t *testing.T) {
	srv, store := newTestServer(t)
	token, _ := seedUserAndJWT(t, store, "alice", "admin")

	req := httptest.NewRequest("POST", "/api/auth/logout", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("expected 204, got %d", rec.Code)
	}

	resp := rec.Result()
	defer resp.Body.Close()
	cookies := resp.Cookies()
	if len(cookies) == 0 {
		t.Fatal("expected cleared session cookie")
	}
	if cookies[0].Name != auth.SessionCookieName || cookies[0].MaxAge >= 0 {
		t.Fatalf("expected expired session cookie, got %+v", cookies[0])
	}
}

func TestLogoutUnauthenticatedReturns401(t *testing.T) {
	srv, _ := newTestServer(t)

	req := httptest.NewRequest("POST", "/api/auth/logout", nil)
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated logout = %d, want 401", rec.Code)
	}
}

func TestLogoutRevokesJWT(t *testing.T) {
	srv, store := newTestServer(t)
	hash, _ := auth.HashPassword("pass123")
	store.CreateUser(db.CreateUserParams{Username: "alice", PasswordHash: hash, Role: "admin"})

	token, _ := auth.IssueJWT(1, "alice", "admin", "test-secret")
	claims, err := auth.ValidateJWT(token, "test-secret", nil)
	if err != nil {
		t.Fatal(err)
	}

	// Sanity: before logout the token unlocks /api/auth/me.
	meReq := httptest.NewRequest("GET", "/api/auth/me", nil)
	meReq.Header.Set("Authorization", "Bearer "+token)
	meRec := httptest.NewRecorder()
	srv.Router().ServeHTTP(meRec, meReq)
	if meRec.Code != http.StatusOK {
		t.Fatalf("pre-logout /api/auth/me = %d, want 200", meRec.Code)
	}

	// Logout.
	logoutReq := httptest.NewRequest("POST", "/api/auth/logout", nil)
	logoutReq.Header.Set("Authorization", "Bearer "+token)
	logoutRec := httptest.NewRecorder()
	srv.Router().ServeHTTP(logoutRec, logoutReq)
	if logoutRec.Code != http.StatusNoContent {
		t.Fatalf("logout = %d, want 204", logoutRec.Code)
	}

	// The jti must now be on the revocation list.
	revoked, err := store.IsTokenRevoked(claims.ID)
	if err != nil {
		t.Fatalf("is revoked: %v", err)
	}
	if !revoked {
		t.Fatal("expected jti to be revoked after logout")
	}

	// Replaying the same token must be rejected.
	replayReq := httptest.NewRequest("GET", "/api/auth/me", nil)
	replayReq.Header.Set("Authorization", "Bearer "+token)
	replayRec := httptest.NewRecorder()
	srv.Router().ServeHTTP(replayRec, replayReq)
	if replayRec.Code != http.StatusUnauthorized {
		t.Fatalf("post-logout replay = %d, want 401", replayRec.Code)
	}
}

// TestMeIssuesFreshJWT verifies that GET /api/auth/me always issues a brand-new
// JWT rather than echoing the token from the incoming cookie.  Re-using the
// original token would mean the JWT's exp claim never advances, so the session
// would expire exactly 24 h after first login regardless of how often the user
// is active — the sliding-window behaviour would be broken.
func TestMeIssuesFreshJWT(t *testing.T) {
	srv, store := newTestServer(t)

	// Seed the user before minting the stale JWT — BearerMiddleware
	// re-resolves the user against the live DB on every request.
	hash, _ := auth.HashPassword("seed")
	if err := store.CreateUser(db.CreateUserParams{Username: "alice", PasswordHash: hash, Role: "admin"}); err != nil {
		t.Fatalf("create user: %v", err)
	}
	alice, _ := store.GetUserByUsername("alice")

	// Build a stale but still-valid JWT (issued 1 hour ago).
	staleToken, staleIssuedAt := buildStaleJWT(alice.ID, "alice", "admin", "test-secret")

	req := httptest.NewRequest("GET", "/api/auth/me", nil)
	req.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: staleToken})
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	cookies := rec.Result().Cookies()
	var freshToken string
	for _, c := range cookies {
		if c.Name == auth.SessionCookieName {
			freshToken = c.Value
			break
		}
	}
	if freshToken == "" {
		t.Fatal("handleMe must set a refreshed session cookie")
	}

	// The response token must differ — same string means the old JWT was echoed.
	if freshToken == staleToken {
		t.Fatal("handleMe re-used the existing JWT; session expiry does not slide")
	}

	// The replacement token must be structurally valid.
	claims, err := auth.ValidateJWT(freshToken, "test-secret", nil)
	if err != nil {
		t.Fatalf("fresh JWT must be valid: %v", err)
	}
	if claims.UserID != alice.ID {
		t.Errorf("fresh JWT UserID = %d, want %d", claims.UserID, alice.ID)
	}
	// The new token must have been issued after the stale one.
	if !claims.IssuedAt.Time.After(staleIssuedAt) {
		t.Errorf("fresh JWT IssuedAt %v is not after original IssuedAt %v",
			claims.IssuedAt.Time, staleIssuedAt)
	}
}

func TestListTokens_Empty(t *testing.T) {
	srv, store := newTestServer(t)
	hash, _ := auth.HashPassword("pass")
	store.CreateUser(db.CreateUserParams{Username: "alice", PasswordHash: hash, Role: "developer"})
	u, _ := store.GetUserByUsername("alice")
	token, _ := auth.IssueJWT(u.ID, "alice", "developer", "test-secret")

	req := authedRequest(t, "GET", "/api/tokens", nil, token)
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var keys []any
	json.NewDecoder(rec.Body).Decode(&keys)
	if len(keys) != 0 {
		t.Errorf("expected empty list, got %d items", len(keys))
	}
}

func TestListTokens_AfterCreate(t *testing.T) {
	srv, store := newTestServer(t)
	hash, _ := auth.HashPassword("pass")
	store.CreateUser(db.CreateUserParams{Username: "alice", PasswordHash: hash, Role: "developer"})
	u, _ := store.GetUserByUsername("alice")
	token, _ := auth.IssueJWT(u.ID, "alice", "developer", "test-secret")

	// Create a token first.
	body, _ := json.Marshal(map[string]string{"name": "my-ci-token"})
	req := authedRequest(t, "POST", "/api/tokens", body, token)
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", rec.Code, rec.Body.String())
	}

	// List should return it.
	req = authedRequest(t, "GET", "/api/tokens", nil, token)
	rec = httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	var keys []map[string]any
	json.NewDecoder(rec.Body).Decode(&keys)
	if len(keys) != 1 {
		t.Fatalf("expected 1 token, got %d", len(keys))
	}
	if keys[0]["name"] != "my-ci-token" {
		t.Errorf("expected name=my-ci-token, got %v", keys[0]["name"])
	}
	if keys[0]["id"] == nil {
		t.Error("expected id in response")
	}
}

func TestDeleteToken(t *testing.T) {
	srv, store := newTestServer(t)
	hash, _ := auth.HashPassword("pass")
	store.CreateUser(db.CreateUserParams{Username: "alice", PasswordHash: hash, Role: "developer"})
	u, _ := store.GetUserByUsername("alice")
	token, _ := auth.IssueJWT(u.ID, "alice", "developer", "test-secret")

	// Create a token.
	body, _ := json.Marshal(map[string]string{"name": "to-delete"})
	req := authedRequest(t, "POST", "/api/tokens", body, token)
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create token: expected 201, got %d", rec.Code)
	}

	// List to get the ID.
	req = authedRequest(t, "GET", "/api/tokens", nil, token)
	rec = httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)
	var keys []map[string]any
	json.NewDecoder(rec.Body).Decode(&keys)
	id := int64(keys[0]["id"].(float64))

	// Delete it.
	path := fmt.Sprintf("/api/tokens/%d", id)
	req = authedRequest(t, "DELETE", path, nil, token)
	rec = httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Errorf("expected 204, got %d: %s", rec.Code, rec.Body.String())
	}

	// List should now be empty.
	req = authedRequest(t, "GET", "/api/tokens", nil, token)
	rec = httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)
	json.NewDecoder(rec.Body).Decode(&keys)
	if len(keys) != 0 {
		t.Errorf("expected empty list after delete, got %d items", len(keys))
	}
}

func TestCreateToken_DuplicateName(t *testing.T) {
	srv, store := newTestServer(t)
	hash, _ := auth.HashPassword("pass")
	store.CreateUser(db.CreateUserParams{Username: "alice", PasswordHash: hash, Role: "developer"})
	u, _ := store.GetUserByUsername("alice")
	token, _ := auth.IssueJWT(u.ID, "alice", "developer", "test-secret")

	body, _ := json.Marshal(map[string]string{"name": "my-token"})

	// First create: success.
	req := authedRequest(t, "POST", "/api/tokens", body, token)
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d", rec.Code)
	}

	// Second create with same name: conflict.
	req = authedRequest(t, "POST", "/api/tokens", body, token)
	rec = httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)
	if rec.Code != http.StatusConflict {
		t.Errorf("expected 409 on duplicate name, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestMeIncludesCanCreateApps_Admin(t *testing.T) {
	srv, store := newTestServer(t)
	token, _ := seedUserAndJWT(t, store, "alice", "admin")

	req := httptest.NewRequest("GET", "/api/auth/me", nil)
	req.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: token})
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var payload struct {
		User *struct {
			ID       int64  `json:"id"`
			Username string `json:"username"`
			Role     string `json:"role"`
		} `json:"user"`
		CanCreateApps bool `json:"can_create_apps"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&payload); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if !payload.CanCreateApps {
		t.Errorf("admin should see can_create_apps=true, got false")
	}
}

func TestMeIncludesCanCreateApps_Viewer(t *testing.T) {
	srv, store := newTestServer(t)
	token, _ := seedUserAndJWT(t, store, "bob", "viewer")

	req := httptest.NewRequest("GET", "/api/auth/me", nil)
	req.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: token})
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var payload struct {
		User *struct {
			ID       int64  `json:"id"`
			Username string `json:"username"`
			Role     string `json:"role"`
		} `json:"user"`
		CanCreateApps bool `json:"can_create_apps"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&payload); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if payload.CanCreateApps {
		t.Errorf("viewer should see can_create_apps=false, got true")
	}
}

func TestSessionLoginIncludesCanCreateApps_Developer(t *testing.T) {
	srv, store := newTestServer(t)
	hash, _ := auth.HashPassword("pass123")
	store.CreateUser(db.CreateUserParams{Username: "dev", PasswordHash: hash, Role: "developer"})

	body, _ := json.Marshal(map[string]string{"username": "dev", "password": "pass123"})
	req := httptest.NewRequest("POST", "/api/auth/session", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var payload struct {
		User *struct {
			ID       int64  `json:"id"`
			Username string `json:"username"`
			Role     string `json:"role"`
		} `json:"user"`
		CanCreateApps bool `json:"can_create_apps"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&payload); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if !payload.CanCreateApps {
		t.Errorf("developer should see can_create_apps=true, got false")
	}
}

// Handoff happy path: a same-origin POST with a valid session cookie revokes
// the JWT, clears the cookie, and 303-redirects to /?next=<safe-next>. This
// is the path the access-denied 403 page exercises when a user signed in to
// the wrong account clicks "Sign in as a different user". The form lives on
// an HTML page rendered outside the SPA, so the SPA's CSRF token isn't
// available — Origin/Referer same-origin is the defence in depth here.
func TestSessionHandoff_RevokesAndRedirects(t *testing.T) {
	srv, store := newTestServer(t)
	token, _ := seedUserAndJWT(t, store, "alice", "developer")
	claims, err := auth.ValidateJWT(token, "test-secret", nil)
	if err != nil {
		t.Fatal(err)
	}

	form := bytes.NewBufferString("next=%2Fapp%2Fsecret%2F")
	req := httptest.NewRequest("POST", "/api/auth/handoff", form)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Origin", "http://"+req.Host)
	req.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: token})
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)

	if rec.Code != http.StatusSeeOther {
		t.Fatalf("expected 303, got %d: %s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Location"); got != "/?next=%2Fapp%2Fsecret%2F" {
		t.Errorf("Location = %q, want /?next=%%2Fapp%%2Fsecret%%2F", got)
	}

	// The session cookie must be expired so the next request to /api/auth/me
	// returns 401 and the SPA shows the login form.
	cookies := rec.Result().Cookies()
	var cleared bool
	for _, c := range cookies {
		if c.Name == auth.SessionCookieName && c.MaxAge < 0 {
			cleared = true
		}
	}
	if !cleared {
		t.Errorf("expected expired session cookie, got %+v", cookies)
	}

	// The JWT must now be on the revocation list — without this a stolen
	// cookie could continue to authenticate until natural expiry.
	revoked, err := store.IsTokenRevoked(claims.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !revoked {
		t.Error("expected jti to be on the revocation list after handoff")
	}
}

// Cross-origin POST is rejected: a malicious site can fire an HTML form POST
// at us, but the browser attaches a third-party Origin header. The handler
// must refuse before clearing the cookie or revoking anything.
func TestSessionHandoff_RejectsCrossOrigin(t *testing.T) {
	srv, store := newTestServer(t)
	token, _ := seedUserAndJWT(t, store, "alice", "developer")
	claims, _ := auth.ValidateJWT(token, "test-secret", nil)

	form := bytes.NewBufferString("next=%2Fapp%2Fsecret%2F")
	req := httptest.NewRequest("POST", "/api/auth/handoff", form)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Origin", "https://evil.example.com")
	req.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: token})
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("cross-origin handoff = %d, want 403", rec.Code)
	}
	revoked, _ := store.IsTokenRevoked(claims.ID)
	if revoked {
		t.Error("cross-origin handoff must not revoke the JWT")
	}
}

// A POST with no Origin and no Referer is also rejected. A bare same-origin
// claim isn't enough on its own — privacy extensions sometimes strip both
// headers, and we'd rather fail closed than open a CSRF hole.
func TestSessionHandoff_RejectsMissingOriginAndReferer(t *testing.T) {
	srv, store := newTestServer(t)
	token, _ := seedUserAndJWT(t, store, "alice", "developer")

	form := bytes.NewBufferString("next=%2Fapp%2Fsecret%2F")
	req := httptest.NewRequest("POST", "/api/auth/handoff", form)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: token})
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("origin-less handoff = %d, want 403", rec.Code)
	}
}

// Open-redirect guard: an attacker-supplied `next` pointing to a different
// host (or to a protocol-relative URL) must be ignored — we redirect to the
// bare login page instead. Otherwise the handoff is a phishing pivot:
// "click here to switch accounts" → POST → 303 to evil.example.com.
func TestSessionHandoff_ScrubsUnsafeNext(t *testing.T) {
	srv, store := newTestServer(t)
	token, _ := seedUserAndJWT(t, store, "alice", "developer")

	cases := []struct {
		name string
		next string
	}{
		{"protocol-relative", "//evil.example.com/"},
		{"absolute URL", "https://evil.example.com/"},
		{"backslash trick", "/\\evil.example.com"},
		{"bare slash loops", "/"},
		{"bare /login loops", "/login"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			form := bytes.NewBufferString("next=" + tc.next)
			req := httptest.NewRequest("POST", "/api/auth/handoff", form)
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			req.Header.Set("Origin", "http://"+req.Host)
			req.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: token})
			rec := httptest.NewRecorder()
			srv.Router().ServeHTTP(rec, req)

			if rec.Code != http.StatusSeeOther {
				t.Fatalf("expected 303, got %d", rec.Code)
			}
			if got := rec.Header().Get("Location"); got != "/" {
				t.Errorf("unsafe next %q yielded Location %q, want /", tc.next, got)
			}
		})
	}
}
