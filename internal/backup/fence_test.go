package backup

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/rvben/shinyhub/internal/config"
	"github.com/rvben/shinyhub/internal/db"
	"github.com/rvben/shinyhub/internal/dbtest"
	"github.com/rvben/shinyhub/internal/deploy"
)

// fenceTestCfg builds a config pointing at a fresh, file-backed SQLite DB and
// fresh apps/app-data dirs, matching the shape backup_test.go's mkCfg uses in
// the external test package (duplicated here because this file lives in the
// internal package, to reach the unexported fence helpers and test hook).
func fenceTestCfg(t *testing.T) *config.Config {
	t.Helper()
	root := t.TempDir()
	return &config.Config{
		Database: config.DatabaseConfig{Driver: "sqlite", DSN: filepath.Join(root, "shinyhub.db")},
		Storage: config.StorageConfig{
			AppsDir:    filepath.Join(root, "apps"),
			AppDataDir: filepath.Join(root, "app-data"),
		},
	}
}

// mkVersionDir creates a version directory populated the way a real deploy's
// extracted bundle would be.
func mkVersionDir(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "app.R"), []byte("shinyApp(ui, server)"), 0o640); err != nil {
		t.Fatal(err)
	}
}

// fenceTestSeedApp seeds cfg's DB with one user and one app whose active
// deployment's bundle_dir is a real, populated version directory under
// cfg.Storage.AppsDir, mirroring what a real deploy commits.
func fenceTestSeedApp(t *testing.T, cfg *config.Config, slug, version string) (appID int64, versionDir string) {
	t.Helper()
	dbtest.WriteSQLiteFile(t, cfg.Database.DSN)
	store, err := db.Open(cfg.Database.DSN)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer store.Close()

	if _, err := store.DB().Exec(
		`INSERT INTO users (username, password_hash, role) VALUES ('bob','x','admin')`); err != nil {
		t.Fatalf("seed user: %v", err)
	}
	var userID int64
	if err := store.DB().QueryRow(`SELECT id FROM users WHERE username='bob'`).Scan(&userID); err != nil {
		t.Fatalf("read seeded user id: %v", err)
	}
	if _, err := store.CreateApp(db.CreateAppParams{
		Slug: slug, Name: slug, ProjectSlug: slug, OwnerID: userID, Access: "private",
	}); err != nil {
		t.Fatalf("create app: %v", err)
	}
	app, err := store.GetAppBySlug(slug)
	if err != nil {
		t.Fatalf("get app: %v", err)
	}
	versionDir = filepath.Join(cfg.Storage.AppsDir, slug, "versions", version)
	mkVersionDir(t, versionDir)
	if _, err := store.CreateDeployment(db.CreateDeploymentParams{
		AppID: app.ID, Version: version, BundleDir: versionDir,
	}); err != nil {
		t.Fatalf("create deployment: %v", err)
	}
	return app.ID, versionDir
}

// fenceTestCommitAndPrune simulates a deploy completing concurrently: it
// creates a new active deployment (v2) and then prunes old versions with
// keep=1, exactly as internal/api/apps.go's deploy handler does after
// committing the new bundle_dir.
func fenceTestCommitAndPrune(t *testing.T, cfg *config.Config, slug string, appID int64) {
	t.Helper()
	store, err := db.Open(cfg.Database.DSN)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer store.Close()

	newVersionDir := filepath.Join(cfg.Storage.AppsDir, slug, "versions", "20260102T000000Z")
	mkVersionDir(t, newVersionDir)
	if _, err := store.CreateDeployment(db.CreateDeploymentParams{
		AppID: appID, Version: "20260102T000000Z", BundleDir: newVersionDir,
	}); err != nil {
		t.Fatalf("create deployment v2: %v", err)
	}
	if err := deploy.PruneOldVersions(cfg.Storage.AppsDir, slug, 1, newVersionDir); err != nil {
		t.Fatalf("prune old versions: %v", err)
	}
}

// TestCreate_FencesConcurrentDeployDuringSnapshotWalk regresses the race
// described in backup.go's package doc: Create takes a DB snapshot, then
// walks AppsDir/AppDataDir. A deploy completing in that gap commits a new
// bundle_dir (harmless: the snapshot still names the old one) and prunes the
// old version directory the snapshot's active deployment still points at
// (not harmless), leaving the archive's database pointing at a version
// directory the archive never contains, with no error raised.
//
// testHookAfterSnapshot fires exactly in that gap (after the DB snapshot,
// before the filesystem walk) and runs the real production call a deploy
// makes, deploy.PruneOldVersions, the same function
// internal/api/apps.go's deploy handler calls after committing a new
// bundle_dir. Create holds the backup fence shared for exactly this window,
// so that call's own internal storage.TryAcquireBackupFence must find it
// busy and skip rather than delete v1's directory.
func TestCreate_FencesConcurrentDeployDuringSnapshotWalk(t *testing.T) {
	dbtest.SkipIfPostgres(t)
	cfg := fenceTestCfg(t)
	appID, v1Dir := fenceTestSeedApp(t, cfg, "demo", "20260101T000000Z")

	var hookRan bool
	testHookAfterSnapshot = func(hookCfg *config.Config) {
		hookRan = true
		fenceTestCommitAndPrune(t, hookCfg, "demo", appID)
	}
	defer func() { testHookAfterSnapshot = nil }()

	archive := filepath.Join(t.TempDir(), "snap.tar.gz")
	if err := Create(cfg, "v1", archive); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if !hookRan {
		t.Fatal("testHookAfterSnapshot never ran; test is not exercising the snapshot-to-walk gap")
	}

	// The strongest direct evidence: with the fence held, the simulated prune
	// (the real deploy.PruneOldVersions) must have skipped rather than run, so
	// v1's directory must still be on disk and must have been captured by the
	// walk.
	if _, err := os.Stat(v1Dir); err != nil {
		t.Errorf("v1 version dir gone after Create despite the backup fence: %v", err)
	}
	if err := Verify(archive); err != nil {
		t.Errorf("Verify on archive produced under simulated concurrent deploy pressure: %v", err)
	}
}
