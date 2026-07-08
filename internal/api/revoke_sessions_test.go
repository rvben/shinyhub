package api_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/rvben/shinyhub/internal/auth"
	"github.com/rvben/shinyhub/internal/db"
)

// TestRevokeSessions_KillsLiveSessions pins the admin force-logout: bumping a
// user's token epoch invalidates every outstanding JWT immediately, while a
// fresh login (which embeds the new epoch) works.
func TestRevokeSessions_KillsLiveSessions(t *testing.T) {
	srv, store := newTestServer(t)
	devID, devTok := mkUser(t, store, "dev", "developer")
	_, adminTok := mkUser(t, store, "boss", "admin")

	if rec := do(t, srv, "GET", "/api/auth/me", devTok, nil); rec.Code != http.StatusOK {
		t.Fatalf("pre-revoke me = %d", rec.Code)
	}

	rec := do(t, srv, "POST", fmt.Sprintf("/api/users/%d/revoke-sessions", devID), adminTok, nil)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("revoke-sessions = %d; body=%s", rec.Code, rec.Body.String())
	}

	if rec := do(t, srv, "GET", "/api/auth/me", devTok, nil); rec.Code != http.StatusUnauthorized {
		t.Errorf("post-revoke me = %d, want 401 (old session dead)", rec.Code)
	}

	// A fresh login works and its token carries the new epoch.
	rec = do(t, srv, "POST", "/api/auth/login", "", []byte(`{"username":"dev","password":"pass"}`))
	if rec.Code != http.StatusOK {
		t.Fatalf("re-login = %d; body=%s", rec.Code, rec.Body.String())
	}
	var login struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &login); err != nil {
		t.Fatal(err)
	}
	if rec := do(t, srv, "GET", "/api/auth/me", login.Token, nil); rec.Code != http.StatusOK {
		t.Errorf("fresh session after revoke = %d, want 200", rec.Code)
	}

	// The revocation is audited.
	rec = do(t, srv, "GET", "/api/audit?action=revoke_sessions", adminTok, nil)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "revoke_sessions") {
		t.Errorf("expected a revoke_sessions audit event, got %d %s", rec.Code, rec.Body.String())
	}
}

// TestRevokeSessions_Authz pins the gate: admin only, system users refused.
func TestRevokeSessions_Authz(t *testing.T) {
	srv, store := newTestServer(t)
	devID, devTok := mkUser(t, store, "dev", "developer")
	_, adminTok := mkUser(t, store, "boss", "admin")
	sys, err := store.UpsertSystemUser(db.SystemUsernameDeploy, "developer")
	if err != nil {
		t.Fatal(err)
	}

	rec := do(t, srv, "POST", fmt.Sprintf("/api/users/%d/revoke-sessions", devID), devTok, nil)
	if rec.Code != http.StatusForbidden {
		t.Errorf("non-admin revoke = %d, want 403", rec.Code)
	}
	rec = do(t, srv, "POST", fmt.Sprintf("/api/users/%d/revoke-sessions", sys.ID), adminTok, nil)
	if rec.Code != http.StatusForbidden {
		t.Errorf("revoke on system user = %d, want 403", rec.Code)
	}
}

// TestAdminPasswordReset_RevokesSessions pins the compromised-account playbook:
// an admin password reset kills the (possibly hijacked) live sessions too.
func TestAdminPasswordReset_RevokesSessions(t *testing.T) {
	srv, store := newTestServer(t)
	devID, devTok := mkUser(t, store, "dev", "developer")
	_, adminTok := mkUser(t, store, "boss", "admin")

	rec := do(t, srv, "PATCH", fmt.Sprintf("/api/users/%d/password", devID), adminTok,
		[]byte(`{"password":"rotated-password-1"}`))
	if rec.Code != http.StatusNoContent && rec.Code != http.StatusOK {
		t.Fatalf("password reset = %d; body=%s", rec.Code, rec.Body.String())
	}
	if rec := do(t, srv, "GET", "/api/auth/me", devTok, nil); rec.Code != http.StatusUnauthorized {
		t.Errorf("session after admin reset = %d, want 401", rec.Code)
	}
}

// TestSelfPasswordChange_RevokesSessions pins that changing your own password
// signs out every session authenticated with the old credential.
func TestSelfPasswordChange_RevokesSessions(t *testing.T) {
	srv, store := newTestServer(t)
	_, tok1 := mkUser(t, store, "bob", "developer")
	// A second live session for the same account.
	u, err := store.GetUserByUsername("bob")
	if err != nil {
		t.Fatal(err)
	}
	tok2, err := auth.IssueJWT(u.ID, u.Username, u.Role, "test-secret")
	if err != nil {
		t.Fatal(err)
	}

	rec := do(t, srv, "PATCH", "/api/auth/me", tok1,
		[]byte(`{"current_password":"pass","new_password":"brand-new-pass1"}`))
	if rec.Code != http.StatusOK {
		t.Fatalf("self change = %d; body=%s", rec.Code, rec.Body.String())
	}
	if rec := do(t, srv, "GET", "/api/auth/me", tok2, nil); rec.Code != http.StatusUnauthorized {
		t.Errorf("other session after self password change = %d, want 401", rec.Code)
	}
}
