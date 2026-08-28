package backup_test

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/rvben/shinyhub/internal/backup"
	"github.com/rvben/shinyhub/internal/config"
	"github.com/rvben/shinyhub/internal/db"
	"github.com/rvben/shinyhub/internal/dbtest"
)

var snapAt = time.Date(2026, 8, 19, 9, 30, 0, 0, time.UTC)

// snapCfg returns a SQLite config whose database lives in a fresh temp dir,
// with the snapshot enabled, plus the open store for it.
func snapCfg(t *testing.T) (*config.Config, *db.Store, string) {
	t.Helper()
	root := t.TempDir()
	dbPath := filepath.Join(root, "shinyhub.db")
	cfg := &config.Config{
		Database: config.DatabaseConfig{Driver: "sqlite", DSN: dbPath, PreMigrationSnapshot: true},
	}
	dbtest.WriteSQLiteFile(t, dbPath)
	store, err := db.Open(dbPath)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { store.Close() })
	return cfg, store, dbPath
}

// makePending forgets one applied migration so Migrate would have work to do,
// standing in for an upgrade that carries new migrations.
func makePending(t *testing.T, store *db.Store) {
	t.Helper()
	if _, err := store.DB().Exec(`DELETE FROM schema_migrations WHERE version = ?`, 2); err != nil {
		t.Fatalf("delete ledger row: %v", err)
	}
}

// The headline behaviour: an upgrade that will change the schema leaves a
// restorable copy of the pre-upgrade database beside it first.
func TestPreMigrationSnapshot_WritesCopyWhenMigrationsPending(t *testing.T) {
	cfg, store, dbPath := snapCfg(t)
	makePending(t, store)
	wantVer, err := store.SchemaVersion()
	if err != nil {
		t.Fatalf("schema version: %v", err)
	}

	res, err := backup.PreMigrationSnapshot(cfg, store, snapAt)
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	if res.Path == "" {
		t.Fatalf("pending migrations must produce a snapshot; skipped with reason %q", res.Skipped)
	}
	if len(res.Pending) != 1 || res.Pending[0] != 2 {
		t.Errorf("Pending = %v, want [2]", res.Pending)
	}

	// The path must be a sibling of the database, name the schema version it
	// was taken at, and carry the timestamp, so several snapshots coexist and
	// an operator can tell which upgrade each one preceded.
	if dir := filepath.Dir(res.Path); dir != filepath.Dir(dbPath) {
		t.Errorf("snapshot dir = %s, want beside the database at %s", dir, filepath.Dir(dbPath))
	}
	base := filepath.Base(res.Path)
	for _, want := range []string{"shinyhub.db.pre-migration-", fmt.Sprintf("v%d", wantVer), "20260819T093000Z", ".sqlite"} {
		if !strings.Contains(base, want) {
			t.Errorf("snapshot name %q must contain %q", base, want)
		}
	}

	// The copy must be a real, openable database holding the pre-upgrade rows,
	// not a zero-byte file that only looks like a backup.
	info, err := os.Stat(res.Path)
	if err != nil {
		t.Fatalf("stat snapshot: %v", err)
	}
	if info.Size() == 0 {
		t.Fatal("snapshot is empty")
	}
	snap, err := db.Open(res.Path)
	if err != nil {
		t.Fatalf("open snapshot: %v", err)
	}
	defer snap.Close()
	ver, err := snap.SchemaVersion()
	if err != nil {
		t.Fatalf("snapshot schema version: %v", err)
	}
	if ver != wantVer {
		t.Errorf("snapshot schema version = %d, want the pre-migration %d", ver, wantVer)
	}

	// The live database must be untouched: a snapshot is a copy, not a move.
	if _, err := os.Stat(dbPath); err != nil {
		t.Errorf("live database must survive the snapshot: %v", err)
	}
}

// A fresh install has every migration pending and nothing worth preserving.
// db.Open creates the file before the snapshot runs, so file existence and even
// file size say "there is a database here" when there is no data in it - the
// version ledger is the only honest signal.
func TestPreMigrationSnapshot_SkipsFreshDatabase(t *testing.T) {
	root := t.TempDir()
	dbPath := filepath.Join(root, "shinyhub.db")
	cfg := &config.Config{
		Database: config.DatabaseConfig{Driver: "sqlite", DSN: dbPath, PreMigrationSnapshot: true},
	}
	store, err := db.Open(dbPath) // opened, never migrated
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer store.Close()

	res, err := backup.PreMigrationSnapshot(cfg, store, snapAt)
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	if res.Path != "" {
		t.Errorf("a fresh install must not write a snapshot of an empty database, got %s", res.Path)
	}
	if !strings.Contains(res.Skipped, "new database") {
		t.Errorf("Skipped = %q, want it to name the fresh-install case", res.Skipped)
	}
	assertNoSnapshots(t, dbPath)
}

// The other half of the version-0 rule: a pre-ledger database reports version 0
// yet holds real data, and adopting it into the ledger is exactly the upgrade an
// operator most wants a rollback point for.
func TestPreMigrationSnapshot_SnapshotsLegacyDatabase(t *testing.T) {
	cfg, store, dbPath := snapCfg(t)
	// Drop the ledger, leaving the core tables: the pre-ledger shape.
	if _, err := store.DB().Exec(`DROP TABLE schema_migrations`); err != nil {
		t.Fatalf("drop ledger: %v", err)
	}
	ver, err := store.SchemaVersion()
	if err != nil {
		t.Fatalf("schema version: %v", err)
	}
	if ver != 0 {
		t.Fatalf("setup: a ledger-less database must report version 0, got %d", ver)
	}

	res, err := backup.PreMigrationSnapshot(cfg, store, snapAt)
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	if res.Path == "" {
		t.Fatalf("a pre-ledger database holds data and must be snapshotted; skipped with reason %q", res.Skipped)
	}
	if !strings.Contains(filepath.Base(res.Path), "v0") {
		t.Errorf("snapshot name %q should record the version it was taken at", filepath.Base(res.Path))
	}
	_ = dbPath
}

// Every restart would otherwise write a snapshot, burying the one that matters
// under hundreds of identical copies.
func TestPreMigrationSnapshot_SkipsWhenNothingPending(t *testing.T) {
	cfg, store, dbPath := snapCfg(t)

	res, err := backup.PreMigrationSnapshot(cfg, store, snapAt)
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	if res.Path != "" {
		t.Errorf("no pending migrations must write no snapshot, got %s", res.Path)
	}
	if res.Skipped == "" {
		t.Error("a skipped snapshot must say why, so the absence is explainable")
	}
	assertNoSnapshots(t, dbPath)
}

// The opt-out exists for operators whose database is too large to copy inside
// the start timeout, or who already snapshot the volume externally.
func TestPreMigrationSnapshot_RespectsOptOut(t *testing.T) {
	cfg, store, dbPath := snapCfg(t)
	makePending(t, store)
	cfg.Database.PreMigrationSnapshot = false

	res, err := backup.PreMigrationSnapshot(cfg, store, snapAt)
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	if res.Path != "" {
		t.Errorf("the opt-out must write no snapshot, got %s", res.Path)
	}
	if !strings.Contains(res.Skipped, "disabled") {
		t.Errorf("Skipped = %q, want it to name the opt-out", res.Skipped)
	}
	// Pending is still reported: the caller logs what it is about to apply
	// whether or not a snapshot was taken.
	if len(res.Pending) != 1 {
		t.Errorf("Pending = %v, want the pending set even when opted out", res.Pending)
	}
	assertNoSnapshots(t, dbPath)
}

// A snapshot that cannot be written is an error, never a silent skip: the whole
// point is that the operator has a rollback point before the schema changes.
func TestPreMigrationSnapshot_FailureIsAnError(t *testing.T) {
	cfg, store, dbPath := snapCfg(t)
	makePending(t, store)

	// Make the directory unwritable so VACUUM INTO cannot create the file.
	dir := filepath.Dir(dbPath)
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })

	res, err := backup.PreMigrationSnapshot(cfg, store, snapAt)
	if err == nil {
		t.Fatalf("an unwritable snapshot destination must be an error, got path %q skipped %q", res.Path, res.Skipped)
	}
}

// assertNoSnapshots proves nothing matching the snapshot naming pattern was
// written next to the database.
func assertNoSnapshots(t *testing.T, dbPath string) {
	t.Helper()
	matches, err := filepath.Glob(dbPath + ".pre-migration-*")
	if err != nil {
		t.Fatalf("glob: %v", err)
	}
	if len(matches) != 0 {
		t.Errorf("expected no snapshot files, found %v", matches)
	}
}
