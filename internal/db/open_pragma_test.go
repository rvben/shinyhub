package db_test

import (
	"path/filepath"
	"testing"

	"github.com/rvben/shinyhub/internal/db"
	"github.com/rvben/shinyhub/internal/dbtest"
)

// TestOpen_FileEnablesWALAndForeignKeys verifies a file-backed database comes
// up in WAL mode with foreign-key enforcement on every pooled connection.
func TestOpen_FileEnablesWALAndForeignKeys(t *testing.T) {
	dbtest.SkipIfPostgres(t) // probes SQLite-only PRAGMA journal_mode
	dsn := filepath.Join(t.TempDir(), "shinyhub.db")
	store, err := db.Open(dsn)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { store.Close() })
	if err := store.Migrate(); err != nil {
		t.Fatalf("Migrate: %v", err)
	}

	var mode string
	if err := store.DB().QueryRow("PRAGMA journal_mode").Scan(&mode); err != nil {
		t.Fatalf("query journal_mode: %v", err)
	}
	if mode != "wal" {
		t.Errorf("journal_mode = %q, want wal", mode)
	}

	// Foreign keys must be enforced: an api_keys row referencing a missing
	// user has to be rejected. Probe more times than the pool size so a
	// connection that skipped the pragma would be caught.
	for range 12 {
		_, err := store.DB().Exec(
			`INSERT INTO api_keys (user_id, key_hash, name) VALUES (?, ?, ?)`,
			999999, "h", "k")
		if err == nil {
			t.Fatal("insert with dangling foreign key succeeded; foreign_keys not enforced")
		}
	}
}

// TestOpen_MemoryStaysSingleDatabase verifies the in-memory DSN keeps a single
// connection so every query sees the same database (a pool would hand out
// connections to distinct empty in-memory databases).
func TestOpen_MemoryStaysSingleDatabase(t *testing.T) {
	dbtest.SkipIfPostgres(t) // verifies SQLite :memory: single-connection behavior
	store, err := db.Open(":memory:")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { store.Close() })
	if err := store.Migrate(); err != nil {
		t.Fatalf("Migrate: %v", err)
	}

	if err := store.CreateUser(db.CreateUserParams{
		Username: "alice", PasswordHash: "h", Role: "admin",
	}); err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	// A second op on a fresh pool checkout must observe the row written above.
	for i := range 20 {
		if _, err := store.GetUserByUsername("alice"); err != nil {
			t.Fatalf("GetUserByUsername after insert (iter %d): %v", i, err)
		}
	}
}
