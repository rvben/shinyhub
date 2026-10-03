package backup_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rvben/shinyhub/internal/backup"
	"github.com/rvben/shinyhub/internal/db"
	"github.com/rvben/shinyhub/internal/dbtest"
)

func TestRestoreRejectsIncompleteArchiveWithoutMovingState(t *testing.T) {
	dbtest.SkipIfPostgres(t)
	cfg := mkCfg(t)
	seed(t, cfg)
	archive := filepath.Join(t.TempDir(), "manifest-only.tar.gz")
	// An interrupted producer can leave a valid manifest and gzip stream,
	// but no database snapshot. It must not be reported as a successful restore.
	rewriteManifestSchema(t, archive, 1)
	moved, err := backup.Restore(cfg, archive)
	if err == nil || !strings.Contains(err.Error(), "missing") {
		t.Fatalf("incomplete archive must be rejected before replacing state: moved=%v err=%v", moved, err)
	}
	if len(moved) != 0 {
		t.Fatalf("invalid archive moved current state aside: %v", moved)
	}
	store, err := db.Open(cfg.Database.DSN)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if _, err := store.GetUserByUsername("alice"); err != nil {
		t.Fatalf("original database no longer usable: %v", err)
	}
	for path, want := range map[string]string{
		filepath.Join(cfg.Storage.AppsDir, "demo", "app.R"):        "shinyApp(ui, server)",
		filepath.Join(cfg.Storage.AppDataDir, "demo", "state.csv"): "a,b\n1,2\n",
	} {
		got, err := os.ReadFile(path)
		if err != nil || string(got) != want {
			t.Fatalf("original file changed: path=%s got=%q err=%v", path, got, err)
		}
	}
}

func TestRestoreRejectsCorruptGzipTrailerWithoutMovingState(t *testing.T) {
	dbtest.SkipIfPostgres(t)
	src := mkCfg(t)
	seed(t, src)
	archive := filepath.Join(t.TempDir(), "backup.tar.gz")
	if err := backup.Create(src, "test", archive); err != nil {
		t.Fatal(err)
	}
	contents, err := os.ReadFile(archive)
	if err != nil {
		t.Fatal(err)
	}
	// Corrupt only the gzip checksum: every tar entry still decodes, so a
	// reader that stops at tar EOF rather than validating gzip reports success.
	contents[len(contents)-8] ^= 1
	if err := os.WriteFile(archive, contents, 0o600); err != nil {
		t.Fatal(err)
	}
	dst := mkCfg(t)
	seed(t, dst)
	moved, err := backup.Restore(dst, archive)
	if err == nil || !strings.Contains(err.Error(), "checksum") {
		t.Fatalf("corrupt archive must be rejected: moved=%v err=%v", moved, err)
	}
	if len(moved) != 0 {
		t.Fatalf("corrupt archive moved current state aside: %v", moved)
	}
	if _, err := os.Stat(dst.Database.DSN); err != nil {
		t.Fatalf("original database moved: %v", err)
	}
}

func TestRestoreRejectsMissingActiveBundleWithoutMovingState(t *testing.T) {
	dbtest.SkipIfPostgres(t)
	src := mkCfg(t)
	seedAppWithActiveDeployment(t, src, "demo", "20260101T000000Z")
	archive := filepath.Join(t.TempDir(), "backup.tar.gz")
	if err := backup.Create(src, "test", archive); err != nil {
		t.Fatal(err)
	}
	missing := filepath.Join(t.TempDir(), "missing-bundle.tar.gz")
	dropTarPrefix(t, archive, missing, "apps/demo/versions/20260101T000000Z")
	dst := mkCfg(t)
	seed(t, dst)
	moved, err := backup.Restore(dst, missing)
	if err == nil || !strings.Contains(err.Error(), "bundle directory is missing") {
		t.Fatalf("unusable deployment must be rejected: moved=%v err=%v", moved, err)
	}
	if len(moved) != 0 {
		t.Fatalf("inconsistent archive moved current state aside: %v", moved)
	}
	if _, err := os.Stat(dst.Database.DSN); err != nil {
		t.Fatalf("original database moved: %v", err)
	}
}
