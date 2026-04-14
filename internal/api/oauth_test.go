package api_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/rvben/shinyhub/internal/auth"
	"github.com/rvben/shinyhub/internal/db"
)

func TestGitHubLogin_NotConfigured(t *testing.T) {
	srv, _ := newTestServer(t) // no OAuth config
	req := httptest.NewRequest("GET", "/api/auth/github/login", nil)
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)
	if rec.Code != http.StatusNotImplemented {
		t.Errorf("expected 501 when OAuth not configured, got %d", rec.Code)
	}
}

func TestGitHubCallback_InvalidState(t *testing.T) {
	srv, _ := newTestServer(t)
	req := httptest.NewRequest("GET", "/api/auth/github/callback?state=bogus&code=xyz", nil)
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for invalid state, got %d", rec.Code)
	}
}

func TestGitHubCallback_MissingParams(t *testing.T) {
	srv, _ := newTestServer(t)
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
