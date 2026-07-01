package api

import (
	"testing"

	"github.com/rvben/shinyhub/internal/config"
	"github.com/rvben/shinyhub/internal/db"
	"github.com/rvben/shinyhub/internal/dbtest"
)

// TestUserLookup_ResolvesPersistedEmail proves the native session/JWT identity
// path carries an SSO user's persisted email into ContextUser.Email, which the
// reverse proxy forwards as X-Shinyhub-Email. Before email was persisted, only
// forward-auth requests (reading an upstream header) got the email; a GitHub /
// Google / OIDC session user got none even though the provider asserted one at
// login. userLookup is what runs on every JWT-authenticated request, so this is
// the seam that makes X-Shinyhub-Email work for native SSO sessions.
func TestUserLookup_ResolvesPersistedEmail(t *testing.T) {
	store := dbtest.New(t)
	cfg := &config.Config{
		Auth:    config.AuthConfig{Secret: "test-secret"},
		Storage: config.StorageConfig{AppsDir: t.TempDir(), AppDataDir: t.TempDir()},
	}
	srv := New(cfg, store, nil, nil)

	// SSO account (no local password) whose provider email was persisted on login.
	if err := store.CreateUser(db.CreateUserParams{Username: "sso", PasswordHash: "", Role: "developer"}); err != nil {
		t.Fatalf("create sso: %v", err)
	}
	u, err := store.GetUserByUsername("sso")
	if err != nil {
		t.Fatalf("get sso: %v", err)
	}
	if err := store.SetEmailFromIdP(u.ID, "sso@example.com"); err != nil {
		t.Fatalf("persist email: %v", err)
	}

	cu, err := srv.userLookup(u.ID)
	if err != nil {
		t.Fatalf("userLookup: %v", err)
	}
	if cu.Email != "sso@example.com" {
		t.Errorf("ContextUser.Email = %q, want %q (persisted SSO email not resolved on the session path)", cu.Email, "sso@example.com")
	}

	// A local username/password account carries no email (never set from an IdP),
	// so no X-Shinyhub-Email is forwarded for it.
	if err := store.CreateUser(db.CreateUserParams{Username: "local", PasswordHash: "$2y$10$hash", Role: "developer"}); err != nil {
		t.Fatalf("create local: %v", err)
	}
	l, _ := store.GetUserByUsername("local")
	lcu, err := srv.userLookup(l.ID)
	if err != nil {
		t.Fatalf("userLookup local: %v", err)
	}
	if lcu.Email != "" {
		t.Errorf("local account ContextUser.Email = %q, want empty", lcu.Email)
	}
}
