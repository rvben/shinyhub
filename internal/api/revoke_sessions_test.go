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

// mkSecondSession issues a second live JWT for an existing account, standing in
// for the same person signed in on another device.
func mkSecondSession(t *testing.T, store *db.Store, username string) string {
	t.Helper()
	u, err := store.GetUserByUsername(username)
	if err != nil {
		t.Fatal(err)
	}
	tok, err := auth.IssueJWT(u.ID, u.Username, u.Role, "test-secret")
	if err != nil {
		t.Fatal(err)
	}
	return tok
}

// TestRevokeOwnSessions_EndsEveryCredentialForTheAccount pins the self-service
// force-logout: someone who believes a credential has been taken can end every
// live session for their own account without an administrator.
func TestRevokeOwnSessions_EndsEveryCredentialForTheAccount(t *testing.T) {
	srv, store := newTestServer(t)
	_, tok1 := mkUser(t, store, "bob", "developer")
	tok2 := mkSecondSession(t, store, "bob")

	for name, tok := range map[string]string{"caller": tok1, "other device": tok2} {
		if rec := do(t, srv, "GET", "/api/auth/me", tok, nil); rec.Code != http.StatusOK {
			t.Fatalf("pre-revoke me via %s = %d", name, rec.Code)
		}
	}

	rec := do(t, srv, "POST", "/api/auth/revoke-sessions", tok1, nil)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("revoke-sessions = %d; body=%s", rec.Code, rec.Body.String())
	}

	for name, tok := range map[string]string{"caller": tok1, "other device": tok2} {
		if rec := do(t, srv, "GET", "/api/auth/me", tok, nil); rec.Code != http.StatusUnauthorized {
			t.Errorf("post-revoke me via %s = %d, want 401", name, rec.Code)
		}
	}

	// Signing out everywhere must not lock the account: a fresh login works.
	rec = do(t, srv, "POST", "/api/auth/login", "", []byte(`{"username":"bob","password":"pass"}`))
	if rec.Code != http.StatusOK {
		t.Fatalf("re-login after revoking own sessions = %d; body=%s", rec.Code, rec.Body.String())
	}
	var login struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &login); err != nil {
		t.Fatal(err)
	}
	if rec := do(t, srv, "GET", "/api/auth/me", login.Token, nil); rec.Code != http.StatusOK {
		t.Errorf("fresh session after revoking own sessions = %d, want 200", rec.Code)
	}

	// It is audited, so an operator reviewing the log sees the account sign
	// itself out rather than an unexplained gap in its sessions.
	_, auditorTok := mkUser(t, store, "auditor", "admin")
	rec = do(t, srv, "GET", "/api/audit?action=revoke_sessions", auditorTok, nil)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "revoke_sessions") {
		t.Errorf("expected a revoke_sessions audit event, got %d %s", rec.Code, rec.Body.String())
	}
}

// TestLogout_LeavesOtherSessionsAlone is the negative control for the test
// above, and pins a deliberate distinction. Logout ends the credential it was
// called with and nothing else, so signing out of a borrowed machine does not
// sign you out of your own. Widening it would make revoke-sessions redundant;
// narrowing revoke-sessions to match would leave no way to do the other thing.
func TestLogout_LeavesOtherSessionsAlone(t *testing.T) {
	srv, store := newTestServer(t)
	_, tok1 := mkUser(t, store, "bob", "developer")
	tok2 := mkSecondSession(t, store, "bob")

	if rec := do(t, srv, "POST", "/api/auth/logout", tok1, nil); rec.Code != http.StatusNoContent {
		t.Fatalf("logout = %d; body=%s", rec.Code, rec.Body.String())
	}
	if rec := do(t, srv, "GET", "/api/auth/me", tok1, nil); rec.Code != http.StatusUnauthorized {
		t.Errorf("me via the logged-out credential = %d, want 401", rec.Code)
	}
	if rec := do(t, srv, "GET", "/api/auth/me", tok2, nil); rec.Code != http.StatusOK {
		t.Errorf("me via the other device after logout = %d, want 200", rec.Code)
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
