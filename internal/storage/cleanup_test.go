package storage

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/rvben/shinyhub/internal/config"
)

func mkCfg(t *testing.T) *config.Config {
	t.Helper()
	root := t.TempDir()
	return &config.Config{Storage: config.StorageConfig{
		AppsDir:    filepath.Join(root, "apps"),
		AppDataDir: filepath.Join(root, "app-data"),
	}}
}

func TestRequireFreeSlug_NoDirsOK(t *testing.T) {
	cfg := mkCfg(t)
	if err := RequireFreeSlug(cfg, "demo"); err != nil {
		t.Fatalf("expected nil, got %v", err)
	}
}

func TestRequireFreeSlug_AppsDirExists(t *testing.T) {
	cfg := mkCfg(t)
	if err := os.MkdirAll(filepath.Join(cfg.Storage.AppsDir, "demo"), 0o750); err != nil {
		t.Fatal(err)
	}
	err := RequireFreeSlug(cfg, "demo")
	if !errors.Is(err, ErrSlugInUse) {
		t.Fatalf("want ErrSlugInUse, got %v", err)
	}
}

func TestRequireFreeSlug_DataDirExists(t *testing.T) {
	cfg := mkCfg(t)
	if err := os.MkdirAll(filepath.Join(cfg.Storage.AppDataDir, "demo"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := RequireFreeSlug(cfg, "demo"); !errors.Is(err, ErrSlugInUse) {
		t.Fatalf("want ErrSlugInUse, got %v", err)
	}
}

func TestOnAppDelete_RemovesBoth(t *testing.T) {
	cfg := mkCfg(t)
	if err := os.MkdirAll(filepath.Join(cfg.Storage.AppsDir, "demo"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(cfg.Storage.AppDataDir, "demo"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := OnAppDelete(cfg, "demo"); err != nil {
		t.Fatalf("OnAppDelete: %v", err)
	}
	if _, err := os.Stat(filepath.Join(cfg.Storage.AppsDir, "demo")); !os.IsNotExist(err) {
		t.Errorf("apps dir still present: %v", err)
	}
	if _, err := os.Stat(filepath.Join(cfg.Storage.AppDataDir, "demo")); !os.IsNotExist(err) {
		t.Errorf("data dir still present: %v", err)
	}
}

func TestOnAppDelete_TolerantOfMissingDirs(t *testing.T) {
	cfg := mkCfg(t)
	if err := OnAppDelete(cfg, "ghost"); err != nil {
		t.Fatalf("expected nil for missing dirs, got %v", err)
	}
}

func TestSweepOrphanDirs_ReportsUnownedDirsOnly(t *testing.T) {
	cfg := mkCfg(t)
	mk := func(base, name string) {
		if err := os.MkdirAll(filepath.Join(base, name), 0o750); err != nil {
			t.Fatal(err)
		}
	}
	// "known" has rows; "ghost" / "stray" do not.
	mk(cfg.Storage.AppsDir, "known")
	mk(cfg.Storage.AppsDir, "ghost")
	mk(cfg.Storage.AppDataDir, "known")
	mk(cfg.Storage.AppDataDir, "stray")

	orphans, err := SweepOrphanDirs(cfg, map[string]bool{"known": true})
	if err != nil {
		t.Fatalf("SweepOrphanDirs: %v", err)
	}
	got := map[string]bool{}
	for _, p := range orphans {
		got[filepath.Base(p)] = true
	}
	if got["known"] {
		t.Error("reported a dir that has an owning row")
	}
	if !got["ghost"] || !got["stray"] {
		t.Fatalf("orphans = %v, want both ghost and stray", orphans)
	}
	// Nothing must have been deleted.
	if _, err := os.Stat(filepath.Join(cfg.Storage.AppsDir, "ghost")); err != nil {
		t.Errorf("sweep deleted an orphan dir (must only report): %v", err)
	}
}

func TestSweepOrphanDirs_MissingRootsOK(t *testing.T) {
	cfg := mkCfg(t) // neither root created yet
	orphans, err := SweepOrphanDirs(cfg, nil)
	if err != nil {
		t.Fatalf("expected nil for missing roots, got %v", err)
	}
	if len(orphans) != 0 {
		t.Fatalf("orphans = %v, want none", orphans)
	}
}
