package db_test

import (
	"testing"

	"github.com/rvben/shinyhub/internal/db"
	"github.com/rvben/shinyhub/internal/dbtest"
)

// TestPendingMigrations_EmptyAfterMigrate proves the pre-migration snapshot has
// a reliable "would Migrate change anything?" signal: every start would
// otherwise write a snapshot, and an operator drowning in snapshots deletes the
// one that mattered.
func TestPendingMigrations_EmptyAfterMigrate(t *testing.T) {
	t.Parallel()
	store, err := db.Open(":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer store.Close()

	// Positive control: before Migrate, everything the binary embeds is pending.
	before, err := store.PendingMigrations()
	if err != nil {
		t.Fatalf("pending before migrate: %v", err)
	}
	if len(before) == 0 {
		t.Fatal("an unmigrated database must report pending migrations, got none")
	}

	if err := store.Migrate(); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	after, err := store.PendingMigrations()
	if err != nil {
		t.Fatalf("pending after migrate: %v", err)
	}
	if len(after) != 0 {
		t.Errorf("a fully migrated database must report nothing pending, got %v", after)
	}
}

// TestPendingMigrations_ReportsGaps proves the signal is the set Migrate would
// apply, not a version comparison. A ledger missing a middle version is still
// at the latest version, yet Migrate would run the missing one.
func TestPendingMigrations_ReportsGaps(t *testing.T) {
	store := dbtest.New(t)

	// Forget the second-lowest applied migration, leaving MAX(version)
	// untouched. It is read from the ledger rather than hard-coded because the
	// dialects embed different sets: SQLite has 002, while Postgres baselines
	// everything before 025 into one file and records only version 1 for it.
	var gap int
	if err := store.DB().QueryRow(
		`SELECT version FROM schema_migrations ORDER BY version LIMIT 1 OFFSET 1`).Scan(&gap); err != nil {
		t.Fatalf("pick ledger row: %v", err)
	}
	if _, err := store.DB().Exec(`DELETE FROM schema_migrations WHERE version = ?`, gap); err != nil {
		t.Fatalf("delete ledger row: %v", err)
	}
	latest, err := store.SchemaVersion()
	if err != nil {
		t.Fatalf("schema version: %v", err)
	}
	if gap >= latest {
		t.Fatalf("gap %d must sit below MAX(version)=%d for this test to mean anything", gap, latest)
	}

	pending, err := store.PendingMigrations()
	if err != nil {
		t.Fatalf("pending: %v", err)
	}
	if len(pending) != 1 || pending[0] != gap {
		t.Fatalf("pending = %v, want exactly [%d] (a gap below MAX(version)=%d)", pending, gap, latest)
	}
}
