package db_test

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/rvben/shinyhub/internal/db"
	"github.com/rvben/shinyhub/internal/dbtest"
)

// openFileStore opens a migrated file-backed SQLite store in a temp dir and
// returns it with the database path.
func openFileStore(t *testing.T) (*db.Store, string) {
	t.Helper()
	dbtest.SkipIfPostgres(t)
	dsn := filepath.Join(t.TempDir(), "shinyhub.db")
	dbtest.WriteSQLiteFile(t, dsn)
	store, err := db.Open(dsn)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { store.Close() })
	return store, dsn
}

// growWAL commits enough small transactions that the write-ahead log holds
// frames which have not been moved back into the database file.
func growWAL(t *testing.T, store *db.Store, dsn string) {
	t.Helper()
	for i := range 64 {
		if err := store.CreateUser(db.CreateUserParams{
			Username: fmt.Sprintf("writer-%d", i), PasswordHash: "h", Role: "developer",
		}); err != nil {
			t.Fatalf("CreateUser %d: %v", i, err)
		}
	}
	if size := walSize(t, dsn); size == 0 {
		t.Fatal("precondition: the WAL is empty after 64 commits, so the test cannot observe a checkpoint")
	}
}

func walSize(t *testing.T, dsn string) int64 {
	t.Helper()
	info, err := os.Stat(dsn + "-wal")
	if os.IsNotExist(err) {
		return 0
	}
	if err != nil {
		t.Fatalf("stat WAL: %v", err)
	}
	return info.Size()
}

// A file-backed store bounds its write-ahead log and sizes its per-connection
// caches on every pooled connection, not only the first one opened.
func TestOpen_FileSetsMaintenancePragmas(t *testing.T) {
	store, _ := openFileStore(t)
	want := map[string]int64{
		"journal_size_limit": 64 << 20,
		"wal_autocheckpoint": 1000,
		"cache_size":         -16384,
		"temp_store":         2,
	}
	// Probe more times than the pool size so a connection that skipped a
	// pragma would be caught.
	for range 12 {
		for pragma, expected := range want {
			var got int64
			if err := store.DB().QueryRow("PRAGMA " + pragma).Scan(&got); err != nil {
				t.Fatalf("PRAGMA %s: %v", pragma, err)
			}
			if got != expected {
				t.Fatalf("PRAGMA %s = %d, want %d", pragma, got, expected)
			}
		}
	}
}

// CheckpointWAL moves every frame into the database file and truncates the
// log to zero bytes, so an idle server's WAL does not keep its high-water mark.
func TestCheckpointWAL_TruncatesWALFile(t *testing.T) {
	store, dsn := openFileStore(t)
	growWAL(t, store, dsn)

	result, err := store.CheckpointWAL()
	if err != nil {
		t.Fatalf("CheckpointWAL: %v", err)
	}
	if result.Blocked || result.PendingFrames != 0 {
		t.Fatalf("checkpoint did not complete with no concurrent readers: %+v", result)
	}
	if size := walSize(t, dsn); size != 0 {
		t.Fatalf("WAL is %d bytes after a TRUNCATE checkpoint, want 0", size)
	}
	// The frames moved into the database file, they were not dropped.
	if _, err := store.GetUserByUsername("writer-63"); err != nil {
		t.Fatalf("row committed through the WAL is unreadable after the checkpoint: %v", err)
	}
}

// Memory and Postgres stores have no WAL file to maintain; the call is a
// no-op there rather than an error, so one maintenance loop serves every backend.
func TestCheckpointWAL_NoOpWithoutWALFile(t *testing.T) {
	store := dbtest.New(t)
	result, err := store.CheckpointWAL()
	if err != nil {
		t.Fatalf("CheckpointWAL on a store without a WAL file: %v", err)
	}
	if result != (db.WALCheckpoint{}) {
		t.Fatalf("CheckpointWAL on a store without a WAL file = %+v, want the zero result", result)
	}
}

// Closing a file-backed store refreshes the planner statistics for the tables
// its connections queried, so the next process starts with fresh sqlite_stat1
// rows instead of planning against none.
func TestClose_RefreshesPlannerStatistics(t *testing.T) {
	dbtest.SkipIfPostgres(t)
	dsn := filepath.Join(t.TempDir(), "shinyhub.db")
	dbtest.WriteSQLiteFile(t, dsn)
	store, err := db.Open(dsn)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := store.CreateUser(db.CreateUserParams{Username: "alice", PasswordHash: "h", Role: "admin"}); err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	if _, err := store.GetUserByUsername("alice"); err != nil {
		t.Fatalf("GetUserByUsername: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	reopened, err := db.Open(dsn)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	t.Cleanup(func() { reopened.Close() })
	var rows int
	if err := reopened.DB().QueryRow(`SELECT count(*) FROM sqlite_stat1 WHERE tbl = 'users'`).Scan(&rows); err != nil {
		t.Fatalf("read planner statistics: %v", err)
	}
	if rows == 0 {
		t.Fatal("no sqlite_stat1 rows for the users table after Close; PRAGMA optimize did not run")
	}
}
