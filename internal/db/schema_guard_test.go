package db_test

import (
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
