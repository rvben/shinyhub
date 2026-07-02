package main

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/rvben/shinyhub/internal/db"
	"github.com/rvben/shinyhub/internal/secrets"
)

func TestValidateRotationSecret(t *testing.T) {
	const old = "current-secret-current-secret-32ch"
	cases := []struct {
		name, oldS, newS string
		wantErr          bool
	}{
		{"empty new", old, "", true},
		{"placeholder", old, "change-me-to-a-random-string", true},
		{"too short", old, "short", true},
		{"same as old", old, old, true},
		{"valid", old, "brand-new-secret-brand-new-secret32", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := validateRotationSecret(c.oldS, c.newS)
			if (err != nil) != c.wantErr {
				t.Errorf("validateRotationSecret(%q,%q) err=%v, wantErr=%v", c.oldS, c.newS, err, c.wantErr)
			}
		})
	}
}

// TestRotateSecretCmd_ReEncryptsEnvSecret drives the full command end-to-end: it
// seeds a secret encrypted under the current auth.secret, runs rotate-secret
// with a new secret, and asserts the value now decrypts under the new key and
// not the old. A command that derived the keys backwards would fail to decrypt
// and error out, so a clean pass also proves the old/new wiring is correct.
func TestRotateSecretCmd_ReEncryptsEnvSecret(t *testing.T) {
	const oldSecret = "old-auth-secret-old-auth-secret-32c"
	const newSecret = "new-auth-secret-new-auth-secret-32c"

	dir := t.TempDir()
	dbPath := filepath.Join(dir, "shinyhub.db")
	yaml := "auth:\n  secret: " + oldSecret + "\ndatabase:\n  dsn: " + dbPath + "\n"
	cfgPath := filepath.Join(dir, "shinyhub.yaml")
	if err := os.WriteFile(cfgPath, []byte(yaml), 0o600); err != nil {
		t.Fatal(err)
	}

	// Seed an app + a secret env var encrypted under the OLD key, then close so
	// the command opens its own connection.
	store, err := db.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Migrate(); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if err := store.CreateUser(db.CreateUserParams{Username: "o", PasswordHash: "h", Role: "admin"}); err != nil {
		t.Fatal(err)
	}
	owner, _ := store.GetUserByUsername("o")
	if err := store.CreateApp(db.CreateAppParams{Slug: "demo", Name: "Demo", OwnerID: owner.ID}); err != nil {
		t.Fatal(err)
	}
	app, _ := store.GetAppBySlug("demo")
	ct, _ := secrets.Encrypt(secrets.DeriveKey(oldSecret), []byte("sk-secret-value"))
	if err := store.UpsertAppEnvVar(app.ID, "API_KEY", ct, true); err != nil {
		t.Fatal(err)
	}
	store.Close()

	t.Setenv("SHINYHUB_CONFIG", cfgPath)
	t.Setenv("SHINYHUB_NEW_AUTH_SECRET", newSecret)

	rotateSecretCmd.SetOut(&bytes.Buffer{})
	rotateSecretCmd.SetErr(&bytes.Buffer{})
	if err := rotateSecretCmd.RunE(rotateSecretCmd, nil); err != nil {
		t.Fatalf("rotate-secret failed: %v", err)
	}

	// Reopen and verify the value now decrypts under the NEW key only.
	store2, err := db.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer store2.Close()
	v, err := store2.GetAppEnvVar(app.ID, "API_KEY")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := secrets.Decrypt(secrets.DeriveKey(oldSecret), v.Value); err == nil {
		t.Error("value must NOT decrypt under the old secret after rotation")
	}
	got, err := secrets.Decrypt(secrets.DeriveKey(newSecret), v.Value)
	if err != nil {
		t.Fatalf("value must decrypt under the new secret: %v", err)
	}
	if string(got) != "sk-secret-value" {
		t.Errorf("plaintext = %q, want sk-secret-value", got)
	}
}
