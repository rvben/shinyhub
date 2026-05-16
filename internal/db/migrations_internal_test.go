package db

import "testing"

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
