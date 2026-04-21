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
	srv, _ := newTestServer(t)
	token, _ := auth.IssueJWT(42, "alice", "admin", "test-secret")

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
	srv, _ := newTestServer(t)
	token, _ := auth.IssueJWT(1, "alice", "admin", "test-secret")

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
	srv, _ := newTestServer(t)

	// Build a stale but still-valid JWT (issued 1 hour ago).
	staleToken, staleIssuedAt := buildStaleJWT(42, "alice", "admin", "test-secret")

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
	if claims.UserID != 42 {
		t.Errorf("fresh JWT UserID = %d, want 42", claims.UserID)
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
	srv, _ := newTestServer(t)
	token, _ := auth.IssueJWT(1, "alice", "admin", "test-secret")

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
	srv, _ := newTestServer(t)
	token, _ := auth.IssueJWT(1, "bob", "viewer", "test-secret")

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
