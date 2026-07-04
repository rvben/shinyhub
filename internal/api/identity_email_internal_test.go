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

// TestUserLookup_ResolvesDisplayName proves the native session/JWT identity path
// carries a user's display name into ContextUser.DisplayName, which the reverse
// proxy forwards as X-Shinyhub-Name and stamps into the identity token's `name`
// claim. userLookup runs on every JWT-authenticated request, so this is the seam
// that makes the display name reach apps for native (password/OAuth/OIDC) sessions
// - forward-auth already sets it request-scoped from an upstream header.
func TestUserLookup_ResolvesDisplayName(t *testing.T) {
	store := dbtest.New(t)
	cfg := &config.Config{
		Auth:    config.AuthConfig{Secret: "test-secret"},
		Storage: config.StorageConfig{AppsDir: t.TempDir(), AppDataDir: t.TempDir()},
	}
	srv := New(cfg, store, nil, nil)

	if err := store.CreateUser(db.CreateUserParams{Username: "sso", PasswordHash: "", Role: "developer"}); err != nil {
		t.Fatalf("create sso: %v", err)
	}
	u, err := store.GetUserByUsername("sso")
	if err != nil {
		t.Fatalf("get sso: %v", err)
	}
	if err := store.SetDisplayNameFromIdP(u.ID, "Ana Smith"); err != nil {
		t.Fatalf("persist display name: %v", err)
	}

	cu, err := srv.userLookup(u.ID)
	if err != nil {
		t.Fatalf("userLookup: %v", err)
	}
	if cu.DisplayName != "Ana Smith" {
		t.Errorf("ContextUser.DisplayName = %q, want %q (persisted display name not resolved on the session path)", cu.DisplayName, "Ana Smith")
	}
}
