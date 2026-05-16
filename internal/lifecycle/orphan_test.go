package lifecycle_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/rvben/shinyhub/internal/config"
	"github.com/rvben/shinyhub/internal/db"
	"github.com/rvben/shinyhub/internal/lifecycle"
)

func mkStorageCfg(t *testing.T) *config.Config {
	t.Helper()
	root := t.TempDir()
	return &config.Config{Storage: config.StorageConfig{
		AppsDir:    filepath.Join(root, "apps"),
		AppDataDir: filepath.Join(root, "app-data"),
	}}
}

// TestReconcileDeletingApps_FinishesTombstone verifies a row left in the
// 'deleting' state (delete interrupted between tombstone and row removal) is
// cleaned from disk and dropped on startup, while a normal app is untouched.
func TestReconcileDeletingApps_FinishesTombstone(t *testing.T) {
	store := mustOpenStore(t)
	cfg := mkStorageCfg(t)

	gone := mustCreateApp(t, store, "gone")
	_ = mustCreateApp(t, store, "kept")
	if err := store.UpdateAppStatus(db.UpdateAppStatusParams{Slug: gone.Slug, Status: "deleting"}); err != nil {
		t.Fatal(err)
	}
	for _, base := range []string{cfg.Storage.AppsDir, cfg.Storage.AppDataDir} {
		if err := os.MkdirAll(filepath.Join(base, "gone"), 0o750); err != nil {
			t.Fatal(err)
		}
	}

	lifecycle.ReconcileDeletingApps(store, cfg)

	if _, err := store.GetAppBySlug("gone"); err == nil {
		t.Fatal("tombstoned app row still present after reconcile")
	}
	if _, err := os.Stat(filepath.Join(cfg.Storage.AppsDir, "gone")); !os.IsNotExist(err) {
		t.Errorf("apps dir not cleaned: %v", err)
	}
	if _, err := store.GetAppBySlug("kept"); err != nil {
		t.Errorf("non-deleting app was affected: %v", err)
	}
}

// TestLogOrphanAppDirs_DoesNotDelete verifies the sweep is report-only: a slug
// dir with no owning row is left intact (an operator reclaims it deliberately).
func TestLogOrphanAppDirs_DoesNotDelete(t *testing.T) {
	store := mustOpenStore(t)
	cfg := mkStorageCfg(t)
	_ = mustCreateApp(t, store, "real")

	orphan := filepath.Join(cfg.Storage.AppDataDir, "orphan")
	if err := os.MkdirAll(orphan, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(cfg.Storage.AppsDir, "real"), 0o750); err != nil {
		t.Fatal(err)
	}

	lifecycle.LogOrphanAppDirs(store, cfg)

	if _, err := os.Stat(orphan); err != nil {
		t.Errorf("orphan sweep deleted bytes (must only log): %v", err)
	}
	if _, err := os.Stat(filepath.Join(cfg.Storage.AppsDir, "real")); err != nil {
		t.Errorf("owned dir disturbed: %v", err)
	}
}
