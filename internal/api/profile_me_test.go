package api_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/rvben/shinyhub/internal/api"
	"github.com/rvben/shinyhub/internal/auth"
	"github.com/rvben/shinyhub/internal/db"
)

type meUserResp struct {
	ID             int64  `json:"id"`
	Username       string `json:"username"`
	Role           string `json:"role"`
	DisplayName    string `json:"display_name"`
	CanSetPassword bool   `json:"can_set_password"`
}

type meResp struct {
	User *meUserResp `json:"user"`
}

func patchMe(t *testing.T, srv *api.Server, token string, body map[string]any) *httptest.ResponseRecorder {
	t.Helper()
	b, _ := json.Marshal(body)
	req := httptest.NewRequest("PATCH", "/api/auth/me", bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)
	return rec
}

func getMe(t *testing.T, srv *api.Server, token string) meResp {
	t.Helper()
	req := httptest.NewRequest("GET", "/api/auth/me", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/auth/me: expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var resp meResp
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode /me: %v", err)
	}
	return resp
}

func TestPatchMe_DisplayName(t *testing.T) {
	srv, store := newTestServer(t)
	token, id := seedUserAndJWT(t, store, "alice", "admin")

	// Surrounding whitespace is trimmed; the value persists and rides back in
	// the session payload so the dashboard updates without a reload.
	rec := patchMe(t, srv, token, map[string]any{"display_name": "  Alice Liddell  "})
	if rec.Code != http.StatusOK {
		t.Fatalf("set display name: expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var resp meResp
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.User.DisplayName != "Alice Liddell" {
		t.Errorf("display name = %q, want %q", resp.User.DisplayName, "Alice Liddell")
	}
	if !resp.User.CanSetPassword {
		t.Error("local account should report can_set_password=true")
	}
	if u, _ := store.GetUserByID(id); u.DisplayName != "Alice Liddell" {
		t.Errorf("persisted display name = %q, want %q", u.DisplayName, "Alice Liddell")
	}

	// A name over 80 runes is rejected.
	rec = patchMe(t, srv, token, map[string]any{"display_name": strings.Repeat("x", 81)})
	if rec.Code != http.StatusBadRequest {
		t.Errorf("over-long name: expected 400, got %d", rec.Code)
	}
}

func TestPatchMe_ChangeOwnPassword(t *testing.T) {
	srv, store := newTestServer(t)
	// seedUserAndJWT provisions the account with password "seed-password".
	token, id := seedUserAndJWT(t, store, "bob", "developer")

	// Wrong current password is rejected.
	rec := patchMe(t, srv, token, map[string]any{
		"current_password": "wrong", "new_password": "brand-new-pass",
	})
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("wrong current password: expected 401, got %d", rec.Code)
	}

	// New password below the minimum length is rejected.
	rec = patchMe(t, srv, token, map[string]any{
		"current_password": "seed-password", "new_password": "short",
	})
	if rec.Code != http.StatusBadRequest {
		t.Errorf("short new password: expected 400, got %d", rec.Code)
	}

	// Correct current password rotates the hash, and the new password verifies.
	rec = patchMe(t, srv, token, map[string]any{
		"current_password": "seed-password", "new_password": "brand-new-pass",
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("change password: expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	u, _ := store.GetUserByID(id)
	if err := auth.VerifyPassword(u.PasswordHash, "brand-new-pass"); err != nil {
		t.Errorf("new password does not verify: %v", err)
	}
}

func TestPatchMe_SSOAccountIsIdPManaged(t *testing.T) {
	srv, store := newTestServer(t)
	// An SSO/OAuth account carries an empty password hash and an IdP-set name.
	if err := store.CreateUser(db.CreateUserParams{
		Username: "okta-user", PasswordHash: "", Role: "viewer",
	}); err != nil {
		t.Fatalf("create sso user: %v", err)
	}
	u, _ := store.GetUserByUsername("okta-user")
	if err := store.SetDisplayNameFromIdP(u.ID, "Okta Person"); err != nil {
		t.Fatalf("seed idp name: %v", err)
	}
	token, err := auth.IssueJWT(u.ID, u.Username, u.Role, "test-secret")
	if err != nil {
		t.Fatalf("issue jwt: %v", err)
	}

	// The session payload advertises that self-management is unavailable.
	if me := getMe(t, srv, token); me.User.CanSetPassword {
		t.Error("SSO account should report can_set_password=false")
	}

	// A password-change attempt is refused with 403.
	rec := patchMe(t, srv, token, map[string]any{
		"current_password": "", "new_password": "irrelevant-but-long",
	})
	if rec.Code != http.StatusForbidden {
		t.Errorf("SSO password change: expected 403, got %d: %s", rec.Code, rec.Body.String())
	}

	// A display-name change is ALSO refused: the name is managed by the IdP.
	rec = patchMe(t, srv, token, map[string]any{"display_name": "Self Chosen"})
	if rec.Code != http.StatusForbidden {
		t.Errorf("SSO display name edit: expected 403, got %d: %s", rec.Code, rec.Body.String())
	}
	if got, _ := store.GetUserByID(u.ID); got.DisplayName != "Okta Person" {
		t.Errorf("SSO display name changed to %q, want the IdP value %q", got.DisplayName, "Okta Person")
	}
}
