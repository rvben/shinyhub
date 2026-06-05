package backup_test

import (
	"archive/tar"
	"compress/gzip"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/rvben/shinyhub/internal/backup"
	"github.com/rvben/shinyhub/internal/config"
	"github.com/rvben/shinyhub/internal/db"
	"github.com/rvben/shinyhub/internal/dbtest"
)

func mkCfg(t *testing.T) *config.Config {
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

func seed(t *testing.T, cfg *config.Config) {
	t.Helper()
	store, err := db.Open(cfg.Database.DSN)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	if err := store.Migrate(); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if _, err := store.DB().Exec(
		`INSERT INTO users (username, password_hash, role) VALUES ('alice','x','admin')`); err != nil {
		t.Fatalf("seed user: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	for _, d := range []string{cfg.Storage.AppsDir, cfg.Storage.AppDataDir} {
		if err := os.MkdirAll(filepath.Join(d, "demo"), 0o750); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(cfg.Storage.AppsDir, "demo", "app.R"),
		[]byte("shinyApp(ui, server)"), 0o640); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cfg.Storage.AppDataDir, "demo", "state.csv"),
		[]byte("a,b\n1,2\n"), 0o640); err != nil {
		t.Fatal(err)
	}
}

// TestRoundTrip backs up a populated instance and restores it into a fresh
// config, asserting the DB row and both file trees survive intact.
func TestRoundTrip(t *testing.T) {
	dbtest.SkipIfPostgres(t)
	src := mkCfg(t)
	seed(t, src)

	archive := filepath.Join(t.TempDir(), "snap.tar.gz")
	if err := backup.Create(src, "v1.2.3", archive); err != nil {
		t.Fatalf("Create: %v", err)
	}

	m, err := backup.ReadManifest(archive)
	if err != nil {
		t.Fatalf("ReadManifest: %v", err)
	}
	if m.ShinyHubVersion != "v1.2.3" {
		t.Errorf("manifest version = %q, want v1.2.3", m.ShinyHubVersion)
	}
	latest, _ := db.LatestSchemaVersion()
	if m.SchemaVersion != latest {
		t.Errorf("manifest schema = %d, want %d", m.SchemaVersion, latest)
	}

	dst := mkCfg(t)
	if _, err := backup.Restore(dst, archive); err != nil {
		t.Fatalf("Restore: %v", err)
	}

	store, err := db.Open(dst.Database.DSN)
	if err != nil {
		t.Fatalf("open restored db: %v", err)
	}
	defer store.Close()
	var n int
	if err := store.DB().QueryRow(
		`SELECT COUNT(*) FROM users WHERE username='alice'`).Scan(&n); err != nil {
		t.Fatalf("query restored db: %v", err)
	}
	if n != 1 {
		t.Errorf("restored user count = %d, want 1", n)
	}
	got, err := os.ReadFile(filepath.Join(dst.Storage.AppsDir, "demo", "app.R"))
	if err != nil || string(got) != "shinyApp(ui, server)" {
		t.Errorf("restored app.R = %q, err=%v", got, err)
	}
	got, err = os.ReadFile(filepath.Join(dst.Storage.AppDataDir, "demo", "state.csv"))
	if err != nil || string(got) != "a,b\n1,2\n" {
		t.Errorf("restored state.csv = %q, err=%v", got, err)
	}
}

// TestRestorePreservesExistingState verifies restore moves current state aside
// (suffix .pre-restore-*) instead of destroying it.
func TestRestorePreservesExistingState(t *testing.T) {
	dbtest.SkipIfPostgres(t)
	src := mkCfg(t)
	seed(t, src)
	archive := filepath.Join(t.TempDir(), "snap.tar.gz")
	if err := backup.Create(src, "v1", archive); err != nil {
		t.Fatalf("Create: %v", err)
	}

	dst := mkCfg(t)
	seed(t, dst) // dst already has its own data
	canary := filepath.Join(dst.Storage.AppsDir, "demo", "canary.txt")
	if err := os.WriteFile(canary, []byte("keep me"), 0o640); err != nil {
		t.Fatal(err)
	}

	moved, err := backup.Restore(dst, archive)
	if err != nil {
		t.Fatalf("Restore: %v", err)
	}
	if len(moved) == 0 {
		t.Fatal("Restore reported nothing moved aside; existing state would have been lost")
	}
	found := false
	for _, p := range moved {
		if strings.Contains(p, ".pre-restore-") {
			if _, statErr := os.Stat(p); statErr == nil {
				found = true
			}
		}
	}
	if !found {
		t.Errorf("no surviving .pre-restore-* copy among %v", moved)
	}
}

func TestRestoreRefusesNewerSchema(t *testing.T) {
	dbtest.SkipIfPostgres(t)
	src := mkCfg(t)
	seed(t, src)
	archive := filepath.Join(t.TempDir(), "snap.tar.gz")
	if err := backup.Create(src, "v1", archive); err != nil {
		t.Fatalf("Create: %v", err)
	}
	rewriteManifestSchema(t, archive, 99999)

	dst := mkCfg(t)
	if _, err := backup.Restore(dst, archive); err == nil ||
		!strings.Contains(err.Error(), "newer than this binary supports") {
		t.Fatalf("want newer-schema refusal, got %v", err)
	}
	if _, statErr := os.Stat(dst.Database.DSN); statErr == nil {
		t.Error("incompatible restore still wrote a database")
	}
}

// TestCreateRejectsOutputInsideBackedUpTree guards against an archive that
// would capture its own partially written .partial file.
func TestCreateRejectsOutputInsideBackedUpTree(t *testing.T) {
	dbtest.SkipIfPostgres(t)
	cfg := mkCfg(t)
	seed(t, cfg)
	inside := filepath.Join(cfg.Storage.AppDataDir, "snap.tar.gz")
	err := backup.Create(cfg, "v1", inside)
	if err == nil || !strings.Contains(err.Error(), "inside backed-up dir") {
		t.Fatalf("want rejection for output inside app-data dir, got %v", err)
	}
}

// TestRoundTripPreservesExecutableBit verifies file modes survive the archive
// round trip (a helper script must stay executable after restore).
func TestRoundTripPreservesExecutableBit(t *testing.T) {
	dbtest.SkipIfPostgres(t)
	src := mkCfg(t)
	seed(t, src)
	script := filepath.Join(src.Storage.AppsDir, "demo", "run.sh")
	if err := os.WriteFile(script, []byte("#!/bin/sh\necho hi\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	archive := filepath.Join(t.TempDir(), "snap.tar.gz")
	if err := backup.Create(src, "v1", archive); err != nil {
		t.Fatalf("Create: %v", err)
	}
	dst := mkCfg(t)
	if _, err := backup.Restore(dst, archive); err != nil {
		t.Fatalf("Restore: %v", err)
	}
	fi, err := os.Stat(filepath.Join(dst.Storage.AppsDir, "demo", "run.sh"))
	if err != nil {
		t.Fatalf("stat restored script: %v", err)
	}
	if fi.Mode().Perm()&0o111 == 0 {
		t.Errorf("restored run.sh lost its executable bit: mode=%o", fi.Mode().Perm())
	}
}

func TestCreateRejectsMemoryDSN(t *testing.T) {
	dbtest.SkipIfPostgres(t)
	cfg := mkCfg(t)
	cfg.Database.DSN = ":memory:"
	err := backup.Create(cfg, "v1", filepath.Join(t.TempDir(), "x.tar.gz"))
	if err == nil || !strings.Contains(err.Error(), "in-memory") {
		t.Fatalf("want in-memory rejection, got %v", err)
	}
}

// rewriteManifestSchema replaces the archive with one whose manifest claims a
// forged schema_version, exercising the compatibility gate without needing a
// real future migration. Restore only reads the manifest before refusing, so a
// manifest-only archive is sufficient.
func rewriteManifestSchema(t *testing.T, archivePath string, schema int) {
	t.Helper()
	out, err := os.Create(archivePath)
	if err != nil {
		t.Fatal(err)
	}
	gzw := gzip.NewWriter(out)
	tw := tar.NewWriter(gzw)
	body := []byte(`{"shinyhub_version":"v1","schema_version":` +
		strconv.Itoa(schema) + `,"created_at":"2026-01-01T00:00:00Z"}`)
	if err := tw.WriteHeader(&tar.Header{
		Name: "manifest.json", Mode: 0o600, Size: int64(len(body)),
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write(body); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gzw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := out.Close(); err != nil {
		t.Fatal(err)
	}
}
