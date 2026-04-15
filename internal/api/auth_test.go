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
// The token was "issued" 1 hour ago with a 24-hour expiry, so it still
// authenticates but its IssuedAt is clearly in the past.
func buildStaleJWT(userID int64, username, role, secret string) (token string, issuedAt time.Time) {
	issuedAt = time.Now().Add(-time.Hour)
	claims := auth.Claims{
		UserID: userID,
		Role:   role,
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   username,
			IssuedAt:  jwt.NewNumericDate(issuedAt),
			ExpiresAt: jwt.NewNumericDate(issuedAt.Add(24 * time.Hour)),
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
		Storage: config.StorageConfig{AppsDir: t.TempDir()},
	}
	srv := api.New(cfg, store, nil, nil) // no manager/proxy for auth tests
	t.Cleanup(func() { store.Close() })
	return srv, store
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
	var resp map[string]string
	json.NewDecoder(rec.Body).Decode(&resp)
	if resp["token"] == "" {
		t.Error("expected token in response")
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

	req := httptest.NewRequest("POST", "/api/auth/logout", nil)
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
	if len(cookies) == 0 {
		t.Fatal("handleMe must set a refreshed session cookie")
	}
	freshToken := cookies[0].Value

	// The response token must differ — same string means the old JWT was echoed.
	if freshToken == staleToken {
		t.Fatal("handleMe re-used the existing JWT; session expiry does not slide")
	}

	// The replacement token must be structurally valid.
	claims, err := auth.ValidateJWT(freshToken, "test-secret")
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
