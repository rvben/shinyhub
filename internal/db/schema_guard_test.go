package db_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/rvben/shinyhub/internal/db"
)

// TestVerifySchemaCompatibility proves the startup guard rejects a database
// that was migrated by a newer binary (downgrade), which would otherwise let an
// older build run against a schema it does not understand.
func TestVerifySchemaCompatibility(t *testing.T) {
	store, err := db.Open(":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer store.Close()
	if err := store.Migrate(); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	// A freshly migrated database is exactly at the binary's latest version.
	if err := store.VerifySchemaCompatibility(); err != nil {
		t.Fatalf("fresh DB should be compatible with its own binary: %v", err)
	}

	// Simulate a database that a newer build already migrated past this binary.
	if _, err := store.DB().Exec(
		`INSERT INTO schema_migrations (version, name, applied_at) VALUES (?, ?, ?)`,
		99999, "future_migration", "2099-01-01T00:00:00Z"); err != nil {
		t.Fatalf("insert future migration: %v", err)
	}

	err = store.VerifySchemaCompatibility()
	if err == nil {
		t.Fatal("a database newer than the binary must be rejected, got nil")
	}
	if !strings.Contains(err.Error(), "newer") {
		t.Errorf("error should explain the downgrade is unsafe, got: %v", err)
	}
}

// TestVerifySchemaCompatibility_IsSentinel proves the downgrade rejection is
// identifiable by type rather than by message text, so callers (the startup
// path, the CLI exit-code classifier) can branch on it without string matching.
func TestVerifySchemaCompatibility_IsSentinel(t *testing.T) {
	store := tooNewStore(t)

	err := store.VerifySchemaCompatibility()
	if err == nil {
		t.Fatal("a database newer than the binary must be rejected, got nil")
	}
	if !errors.Is(err, db.ErrSchemaTooNew) {
		t.Errorf("error must wrap db.ErrSchemaTooNew so callers can classify it, got %T: %v", err, err)
	}

	// Negative control: a compatible database must NOT match the sentinel, or
	// the classifier would route every startup failure to the same exit code.
	fresh, ferr := db.Open(":memory:")
	if ferr != nil {
		t.Fatalf("open: %v", ferr)
	}
	defer fresh.Close()
	if merr := fresh.Migrate(); merr != nil {
		t.Fatalf("migrate: %v", merr)
	}
	if cerr := fresh.VerifySchemaCompatibility(); errors.Is(cerr, db.ErrSchemaTooNew) {
		t.Errorf("compatible DB must not match ErrSchemaTooNew, got: %v", cerr)
	}
}

// TestVerifySchemaCompatibility_MessageIsActionable proves the operator-facing
// text says the condition is permanent and names the command that resolves it.
// Without both, a service supervisor restart-loops forever on an error whose
// text reads like a transient failure.
func TestVerifySchemaCompatibility_MessageIsActionable(t *testing.T) {
	store := tooNewStore(t)

	err := store.VerifySchemaCompatibility()
	if err == nil {
		t.Fatal("a database newer than the binary must be rejected, got nil")
	}
	msg := err.Error()
	for _, want := range []string{"will not succeed on retry", "shinyhub restore"} {
		if !strings.Contains(msg, want) {
			t.Errorf("message must contain %q so the operator knows what to do; got: %s", want, msg)
		}
	}
}

// tooNewStore returns an open store whose ledger claims a schema version far
// beyond anything this binary embeds, i.e. a database migrated by a newer build.
func tooNewStore(t *testing.T) *db.Store {
	t.Helper()
	store, err := db.Open(":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { store.Close() })
	if err := store.Migrate(); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if _, err := store.DB().Exec(
		`INSERT INTO schema_migrations (version, name, applied_at) VALUES (?, ?, ?)`,
		99999, "future_migration", "2099-01-01T00:00:00Z"); err != nil {
		t.Fatalf("insert future migration: %v", err)
	}
	return store
}
