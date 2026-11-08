package main

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/rvben/shinyhub/internal/config"
	"github.com/rvben/shinyhub/internal/db"
	"github.com/rvben/shinyhub/internal/dbtest"
)

var prepareAt = time.Date(2026, 11, 8, 10, 15, 0, 0, time.UTC)

// pendingMigration is a migration whose effect the fixture can undo exactly
// (it only creates an index), so Migrate genuinely re-applies it.
const pendingMigration = 88

// pendingStore opens an on-disk SQLite database with every migration applied
// except pendingMigration, standing in for an upgrade that carries a new one.
func pendingStore(t *testing.T) (*config.Config, *db.Store, string) {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "shinyhub.db")
	dbtest.WriteSQLiteFile(t, dbPath)
	store, err := db.Open(dbPath)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { store.Close() })
	if _, err := store.DB().Exec(`DROP INDEX idx_apps_created_at`); err != nil {
		t.Fatalf("undo migration %d: %v", pendingMigration, err)
	}
	if _, err := store.DB().Exec(`DELETE FROM schema_migrations WHERE version = ?`, pendingMigration); err != nil {
		t.Fatalf("forget migration %d: %v", pendingMigration, err)
	}
	cfg := &config.Config{Database: config.DatabaseConfig{
		Driver: "sqlite", DSN: dbPath, PreMigrationSnapshot: true, PreMigrationSnapshotRetention: 3,
	}}
	return cfg, store, dbPath
}

func migrationApplied(t *testing.T, path string, version int) bool {
	t.Helper()
	store, err := db.Open(path)
	if err != nil {
		t.Fatalf("open %s: %v", path, err)
	}
	defer store.Close()
	var n int
	if err := store.DB().QueryRow(`SELECT COUNT(*) FROM schema_migrations WHERE version = ?`, version).Scan(&n); err != nil {
		t.Fatalf("read ledger of %s: %v", path, err)
	}
	return n == 1
}

func snapshotsBeside(t *testing.T, dbPath string) []string {
	t.Helper()
	matches, err := filepath.Glob(dbPath + ".pre-migration-v*.sqlite")
	if err != nil {
		t.Fatal(err)
	}
	return matches
}

// The rollback copy is the database as it was before this binary changed it:
// it is written first, then the pending migration is applied to the live file.
func TestPrepareDatabaseSnapshotsBeforeMigrating(t *testing.T) {
	cfg, store, dbPath := pendingStore(t)

	if err := prepareDatabase(cfg, store, slog.New(slog.DiscardHandler), prepareAt); err != nil {
		t.Fatalf("prepareDatabase: %v", err)
	}
	snaps := snapshotsBeside(t, dbPath)
	if len(snaps) != 1 {
		t.Fatalf("snapshots = %v, want exactly one", snaps)
	}
	if migrationApplied(t, snaps[0], pendingMigration) {
		t.Fatal("snapshot already carries the pending migration; it was taken after migrating")
	}
	if !migrationApplied(t, dbPath, pendingMigration) {
		t.Fatal("the pending migration was not applied to the live database")
	}
}

// A database the operator cannot roll back must not be migrated.
func TestPrepareDatabaseRefusesToMigrateWithoutASnapshot(t *testing.T) {
	cfg, store, dbPath := pendingStore(t)
	from, err := store.SchemaVersion()
	if err != nil {
		t.Fatal(err)
	}
	// Occupy the snapshot's destination so writing it fails.
	dest := fmt.Sprintf("%s.pre-migration-v%d-%s.sqlite", dbPath, from, prepareAt.Format("20060102T150405Z"))
	if err := os.Mkdir(dest, 0o700); err != nil {
		t.Fatal(err)
	}

	err = prepareDatabase(cfg, store, slog.New(slog.DiscardHandler), prepareAt)
	if err == nil || !strings.Contains(err.Error(), "pre_migration_snapshot: false") {
		t.Fatalf("err = %v, want a snapshot failure naming the opt-out", err)
	}
	if migrationApplied(t, dbPath, pendingMigration) {
		t.Fatal("the pending migration was applied although no snapshot could be written")
	}
}

func TestPrepareDatabaseRefusesASchemaFromANewerBuild(t *testing.T) {
	cfg, store, _ := pendingStore(t)
	latest, err := db.LatestSchemaVersion()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.DB().Exec(
		`INSERT INTO schema_migrations (version, name, applied_at) VALUES (?, 'future', '2099-01-01T00:00:00Z')`, latest+1,
	); err != nil {
		t.Fatal(err)
	}

	err = prepareDatabase(cfg, store, slog.New(slog.DiscardHandler), prepareAt)
	if err == nil || !strings.HasPrefix(err.Error(), "schema compatibility:") {
		t.Fatalf("err = %v, want a schema compatibility refusal", err)
	}
}

// Schedule runs that were live across the 0.12 upgrade may have left writers
// the new fencing cannot see; serving would race them.
func TestPrepareDatabaseRefusesWhileLegacyScheduleWritersAreUnresolved(t *testing.T) {
	cfg, store, _ := pendingStore(t)
	if err := store.Migrate(); err != nil {
		t.Fatal(err)
	}
	if err := store.CreateUser(db.CreateUserParams{Username: "owner", PasswordHash: "hash", Role: "developer"}); err != nil {
		t.Fatal(err)
	}
	owner, err := store.GetUserByUsername("owner")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateApp(db.CreateAppParams{
		Slug: "fenced", Name: "Fenced", ProjectSlug: "fenced", OwnerID: owner.ID, Access: "private",
	}); err != nil {
		t.Fatalf("create app: %v", err)
	}
	app, err := store.GetAppBySlug("fenced")
	if err != nil {
		t.Fatal(err)
	}
	scheduleID, err := store.CreateSchedule(db.CreateScheduleParams{
		AppID: app.ID, Name: "refresh", CronExpr: "0 5 * * *", CommandJSON: `["producer"]`,
		Enabled: true, TimeoutSeconds: 60, OverlapPolicy: "skip", MissedPolicy: "skip",
		DeployTrigger: "never", OnSuccess: "roll", RollFallback: "defer",
	})
	if err != nil {
		t.Fatalf("create schedule: %v", err)
	}
	var runID int64
	if err := store.DB().QueryRow(`
		INSERT INTO schedule_runs (schedule_id, status, trigger, started_at, finished_at, on_success)
		VALUES (?, 'interrupted', 'schedule', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP, 'roll')
		RETURNING id`, scheduleID).Scan(&runID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.DB().Exec(`INSERT INTO legacy_unfenced_schedule_runs (run_id) VALUES (?)`, runID); err != nil {
		t.Fatal(err)
	}

	err = prepareDatabase(cfg, store, slog.New(slog.DiscardHandler), prepareAt)
	if err == nil || !strings.Contains(err.Error(), "1 schedule run(s)") ||
		!strings.Contains(err.Error(), "resolve-legacy-schedule-writers") {
		t.Fatalf("err = %v, want the legacy writer fence naming the count and the remedy", err)
	}
}
