package db

import (
	"fmt"
	"testing"
)

// columnExists reports whether the given table has the named column.
func columnExists(t *testing.T, store *Store, table, column string) bool {
	t.Helper()
	var n int
	q := fmt.Sprintf(`SELECT COUNT(*) FROM pragma_table_info('%s') WHERE name = '%s'`, table, column)
	if err := store.db.QueryRow(q).Scan(&n); err != nil {
		t.Fatalf("pragma %s.%s: %v", table, column, err)
	}
	return n > 0
}

// TestMigrate_LegacyBaselineAppliesPostBaselineMigrations proves that a
// pre-ledger database (schema through the legacy baseline version, no ledger)
// still receives migrations that were added after the ledger. Recording every
// embedded migration as "applied" without running them would leave a legacy DB
// permanently missing the columns those later migrations add.
func TestMigrate_LegacyBaselineAppliesPostBaselineMigrations(t *testing.T) {
	store, err := Open(":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	// Build a pre-ledger schema: apply only migrations up to the legacy
	// baseline version, leaving no schema_migrations ledger behind.
	ms, err := loadMigrations()
	if err != nil {
		t.Fatalf("loadMigrations: %v", err)
	}
	for _, m := range ms {
		if m.version > legacyBaselineVersion {
			continue
		}
		if _, err := store.db.Exec(m.sql); err != nil {
			t.Fatalf("seed legacy migration %s: %v", m.name, err)
		}
	}

	// Precondition: a post-baseline column must be absent in the legacy schema.
	if columnExists(t, store, "apps", "replica_placement") {
		t.Fatal("precondition failed: apps.replica_placement should be absent before migrate")
	}

	if err := store.Migrate(); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	// Post-baseline migrations must have actually run against the legacy DB.
	if !columnExists(t, store, "apps", "replica_placement") {
		t.Error("migration 018 not applied to legacy DB: apps.replica_placement missing")
	}
	if !columnExists(t, store, "replicas", "deployment_id") {
		t.Error("migration 019 not applied to legacy DB: replicas.deployment_id missing")
	}

	// The ledger must record the full embedded set (baselined + freshly run).
	latest, err := LatestSchemaVersion()
	if err != nil {
		t.Fatalf("latest schema version: %v", err)
	}
	got, err := store.SchemaVersion()
	if err != nil {
		t.Fatalf("schema version: %v", err)
	}
	if got != latest {
		t.Errorf("ledger schema version = %d, want %d", got, latest)
	}
}

// TestLoadMigrationsOrderedAndUnique guards the migration filename convention
// and ordering. A misnamed or duplicate-versioned file is a build-time
// mistake and must fail loudly rather than silently reorder schema changes.
func TestLoadMigrationsOrderedAndUnique(t *testing.T) {
	ms, err := loadMigrations()
	if err != nil {
		t.Fatalf("loadMigrations: %v", err)
	}
	if len(ms) == 0 {
		t.Fatal("no migrations loaded")
	}
	seen := map[int]bool{}
	for i, m := range ms {
		if m.sql == "" {
			t.Errorf("migration %s has empty body", m.name)
		}
		if seen[m.version] {
			t.Errorf("duplicate version %d", m.version)
		}
		seen[m.version] = true
		if i > 0 && ms[i-1].version >= m.version {
			t.Errorf("migrations not strictly ascending: %s (v%d) after v%d",
				m.name, m.version, ms[i-1].version)
		}
	}
	// Version 1 (the init schema) must sort first.
	if ms[0].version != 1 {
		t.Errorf("first migration version = %d, want 1", ms[0].version)
	}
}

func TestMigrations015And016AddColumns(t *testing.T) {
	store, err := Open(":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if err := store.Migrate(); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	var n int
	row := store.DB().QueryRow(
		`SELECT COUNT(*) FROM pragma_table_info('deployments') WHERE name = 'content_digest'`)
	if err := row.Scan(&n); err != nil {
		t.Fatalf("pragma deployments: %v", err)
	}
	if n != 1 {
		t.Fatalf("deployments.content_digest: want 1 column, got %d", n)
	}

	row = store.DB().QueryRow(
		`SELECT COUNT(*) FROM pragma_table_info('apps') WHERE name = 'managed_by'`)
	if err := row.Scan(&n); err != nil {
		t.Fatalf("pragma apps: %v", err)
	}
	if n != 1 {
		t.Fatalf("apps.managed_by: want 1 column, got %d", n)
	}
}
