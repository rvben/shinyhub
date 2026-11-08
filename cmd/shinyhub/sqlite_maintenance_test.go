package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/rvben/shinyhub/internal/db"
	"github.com/rvben/shinyhub/internal/dbtest"
)

// The SQLite maintenance loop truncates the write-ahead log on its prompt
// first pass, so a log left behind by the previous process is bounded at
// startup rather than an hour later.
func TestRunSQLiteMaintenance_TruncatesWALPromptly(t *testing.T) {
	dbtest.SkipIfPostgres(t)
	dsn := filepath.Join(t.TempDir(), "shinyhub.db")
	dbtest.WriteSQLiteFile(t, dsn)
	store, err := db.Open(dsn)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { store.Close() })
	for i := range 64 {
		if err := store.CreateUser(db.CreateUserParams{
			Username: fmt.Sprintf("writer-%d", i), PasswordHash: "h", Role: "developer",
		}); err != nil {
			t.Fatalf("CreateUser %d: %v", i, err)
		}
	}
	if info, err := os.Stat(dsn + "-wal"); err != nil || info.Size() == 0 {
		t.Fatalf("precondition: WAL must hold frames before the loop runs (stat: %v)", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // the loop still runs its prompt first pass, then exits.
	runSQLiteMaintenance(ctx, store, time.Hour)

	info, err := os.Stat(dsn + "-wal")
	if err != nil {
		t.Fatalf("stat WAL after maintenance: %v", err)
	}
	if info.Size() != 0 {
		t.Fatalf("WAL is %d bytes after the maintenance pass, want 0", info.Size())
	}
}
