package api_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/rvben/shinyhub/internal/api"
	"github.com/rvben/shinyhub/internal/auth"
	"github.com/rvben/shinyhub/internal/config"
	"github.com/rvben/shinyhub/internal/db"
)

// newOAuthTestServer creates a test server with a fake GitHub OAuth config so
// that s.github is non-nil and param/state validation logic is reachable.
func newOAuthTestServer(t *testing.T) (*api.Server, *db.Store) {
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
		OAuth: config.OAuthConfig{
			GitHub: config.GitHubOAuthConfig{
				ClientID:     "test-client-id",
				ClientSecret: "test-client-secret",
				CallbackURL:  "http://localhost/callback",
			},
		},
	}
	srv := api.New(cfg, store, nil, nil)
	t.Cleanup(func() { store.Close() })
	return srv, store
}

func TestGitHubLogin_NotConfigured(t *testing.T) {
	srv, _ := newTestServer(t) // no OAuth config
	req := httptest.NewRequest("GET", "/api/auth/github/login", nil)
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)
	if rec.Code != http.StatusNotImplemented {
		t.Errorf("expected 501 when OAuth not configured, got %d", rec.Code)
	}
}

func TestGitHubCallback_NotConfigured(t *testing.T) {
	srv, _ := newTestServer(t) // no OAuth config
	req := httptest.NewRequest("GET", "/api/auth/github/callback?state=x&code=y", nil)
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)
	if rec.Code != http.StatusNotImplemented {
		t.Errorf("expected 501 when OAuth not configured, got %d", rec.Code)
	}
}

func TestGitHubCallback_InvalidState(t *testing.T) {
	srv, _ := newOAuthTestServer(t)
	req := httptest.NewRequest("GET", "/api/auth/github/callback?state=bogus&code=xyz", nil)
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for invalid state, got %d", rec.Code)
	}
}

func TestGitHubCallback_MissingParams(t *testing.T) {
	srv, _ := newOAuthTestServer(t)
	req := httptest.NewRequest("GET", "/api/auth/github/callback", nil)
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for missing params, got %d", rec.Code)
	}
}

func TestOAuthUser_CreatedOnFirstLogin(t *testing.T) {
	_, store := newTestServer(t)

	// Simulate what the callback does: create user + oauth account.
	store.CreateUser(db.CreateUserParams{Username: "gh-alice", PasswordHash: "", Role: "developer"})
	u, _ := store.GetUserByUsername("gh-alice")
	store.CreateOAuthAccount(db.CreateOAuthAccountParams{UserID: u.ID, Provider: "github", ProviderID: "gh_999"})

	found, err := store.GetUserByOAuthAccount("github", "gh_999")
	if err != nil {
		t.Fatalf("GetUserByOAuthAccount: %v", err)
	}
	if found.Username != "gh-alice" {
		t.Errorf("expected gh-alice, got %s", found.Username)
	}

	// Verify JWT can be issued for this user.
	tok, err := auth.IssueJWT(u.ID, u.Username, u.Role, "test-secret")
	if err != nil {
		t.Fatalf("IssueJWT: %v", err)
	}
	if !strings.HasPrefix(tok, "ey") {
		t.Errorf("expected JWT, got %s", tok)
	}
}

// newGoogleOAuthTestServer creates a test server with a fake Google OAuth config
// so that s.googleOAuth is non-nil and param/state validation logic is reachable.
func newGoogleOAuthTestServer(t *testing.T) (*api.Server, *db.Store) {
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
		OAuth: config.OAuthConfig{
			Google: config.GoogleOAuthConfig{
				ClientID:     "test-google-client-id",
				ClientSecret: "test-google-client-secret",
				CallbackURL:  "http://localhost/google/callback",
			},
		},
	}
	srv := api.New(cfg, store, nil, nil)
	t.Cleanup(func() { store.Close() })
	return srv, store
}

func TestGoogleLogin_NotConfigured(t *testing.T) {
	srv, _ := newTestServer(t) // no OAuth config
	req := httptest.NewRequest("GET", "/api/auth/google/login", nil)
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)
	if rec.Code != http.StatusNotImplemented {
		t.Errorf("expected 501 when OAuth not configured, got %d", rec.Code)
	}
}

func TestGoogleCallback_NotConfigured(t *testing.T) {
	srv, _ := newTestServer(t) // no OAuth config
	req := httptest.NewRequest("GET", "/api/auth/google/callback?state=x&code=y", nil)
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)
	if rec.Code != http.StatusNotImplemented {
		t.Errorf("expected 501 when OAuth not configured, got %d", rec.Code)
	}
}

func TestGoogleCallback_MissingParams(t *testing.T) {
	srv, _ := newGoogleOAuthTestServer(t)
	req := httptest.NewRequest("GET", "/api/auth/google/callback", nil)
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for missing params, got %d", rec.Code)
	}
}

func TestGoogleCallback_InvalidState(t *testing.T) {
	srv, _ := newGoogleOAuthTestServer(t)
	req := httptest.NewRequest("GET", "/api/auth/google/callback?state=bogus&code=xyz", nil)
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for invalid state, got %d", rec.Code)
	}
}

func TestGoogleOAuthUser_CreatedOnFirstLogin(t *testing.T) {
	_, store := newTestServer(t)

	// Simulate what the callback does: create user + oauth account.
	store.CreateUser(db.CreateUserParams{Username: "alice", PasswordHash: "", Role: "developer"})
	u, _ := store.GetUserByUsername("alice")
	store.CreateOAuthAccount(db.CreateOAuthAccountParams{UserID: u.ID, Provider: "google", ProviderID: "google_12345"})

	found, err := store.GetUserByOAuthAccount("google", "google_12345")
	if err != nil {
		t.Fatalf("GetUserByOAuthAccount: %v", err)
	}
	if found.Username != "alice" {
		t.Errorf("expected alice, got %s", found.Username)
	}

	tok, err := auth.IssueJWT(u.ID, u.Username, u.Role, "test-secret")
	if err != nil {
		t.Fatalf("IssueJWT: %v", err)
	}
	if !strings.HasPrefix(tok, "ey") {
		t.Errorf("expected JWT, got %s", tok)
	}
}
