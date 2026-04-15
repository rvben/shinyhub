package deploy_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/rvben/shinyhub/internal/deploy"
)

func TestPruneOldVersions_KeepsNewest(t *testing.T) {
	appsDir := t.TempDir()
	slug := "myapp"
	versionsDir := filepath.Join(appsDir, slug, "versions")
	bundlesDir := filepath.Join(appsDir, slug, "bundles")
	os.MkdirAll(versionsDir, 0755)
	os.MkdirAll(bundlesDir, 0755)

	// Create 7 version dirs and bundle zips.
	for _, name := range []string{"001", "002", "003", "004", "005", "006", "007"} {
		os.MkdirAll(filepath.Join(versionsDir, name), 0755)
		os.WriteFile(filepath.Join(bundlesDir, name+".zip"), []byte("x"), 0644)
	}

	active := filepath.Join(appsDir, slug, "versions", "007")
	if err := deploy.PruneOldVersions(appsDir, slug, 5, active); err != nil {
		t.Fatalf("PruneOldVersions: %v", err)
	}

	entries, _ := os.ReadDir(versionsDir)
	if len(entries) != 5 {
		t.Errorf("expected 5 version dirs, got %d", len(entries))
	}
	// Newest 5 (003–007) should remain.
	for _, name := range []string{"003", "004", "005", "006", "007"} {
		if _, err := os.Stat(filepath.Join(versionsDir, name)); err != nil {
			t.Errorf("expected %s to exist", name)
		}
	}
	// Oldest 2 (001, 002) should be gone.
	for _, name := range []string{"001", "002"} {
		if _, err := os.Stat(filepath.Join(versionsDir, name)); !os.IsNotExist(err) {
			t.Errorf("expected %s to be deleted", name)
		}
	}
	// Bundle zips: should also have 5 remaining.
	bundleEntries, _ := os.ReadDir(bundlesDir)
	if len(bundleEntries) != 5 {
		t.Errorf("expected 5 bundle zips, got %d", len(bundleEntries))
	}
}

func TestPruneOldVersions_SkipsActiveDir(t *testing.T) {
	appsDir := t.TempDir()
	slug := "myapp"
	versionsDir := filepath.Join(appsDir, slug, "versions")
	bundlesDir := filepath.Join(appsDir, slug, "bundles")
	os.MkdirAll(versionsDir, 0755)
	os.MkdirAll(bundlesDir, 0755)

	for _, name := range []string{"001", "002", "003", "004", "005", "006"} {
		os.MkdirAll(filepath.Join(versionsDir, name), 0755)
		os.WriteFile(filepath.Join(bundlesDir, name+".zip"), []byte("x"), 0644)
	}

	// Active dir is "001" (oldest) — must not be deleted even though it's outside retention.
	active := filepath.Join(appsDir, slug, "versions", "001")
	if err := deploy.PruneOldVersions(appsDir, slug, 5, active); err != nil {
		t.Fatalf("PruneOldVersions: %v", err)
	}

	if _, err := os.Stat(active); err != nil {
		t.Errorf("active dir should not have been deleted")
	}

	// The active bundle zip should also survive.
	activeBundlePath := filepath.Join(bundlesDir, "001.zip")
	if _, err := os.Stat(activeBundlePath); err != nil {
		t.Errorf("active bundle zip should not have been deleted: %v", err)
	}

	// With keep=5 and 6 entries, 1 must be deleted. Since "001" is skipped,
	// "002" is deleted instead. Remaining: "001", "003"–"006" = 5 version dirs.
	versionEntries, _ := os.ReadDir(versionsDir)
	if len(versionEntries) != 5 {
		t.Errorf("expected 5 version dirs after pruning, got %d", len(versionEntries))
	}
	if _, err := os.Stat(filepath.Join(versionsDir, "002")); !os.IsNotExist(err) {
		t.Errorf("expected version 002 to be deleted")
	}

	// Bundles: same logic — "001.zip" skipped, "002.zip" deleted, 5 remain.
	bundleEntries, _ := os.ReadDir(bundlesDir)
	if len(bundleEntries) != 5 {
		t.Errorf("expected 5 bundle zips after pruning, got %d", len(bundleEntries))
	}
	if _, err := os.Stat(filepath.Join(bundlesDir, "002.zip")); !os.IsNotExist(err) {
		t.Errorf("expected bundle 002.zip to be deleted")
	}
}

func TestPruneOldVersions_NothingToDelete(t *testing.T) {
	appsDir := t.TempDir()
	slug := "myapp"
	versionsDir := filepath.Join(appsDir, slug, "versions")
	bundlesDir := filepath.Join(appsDir, slug, "bundles")
	os.MkdirAll(versionsDir, 0755)
	os.MkdirAll(bundlesDir, 0755)

	// Fewer entries than retention limit — nothing should be deleted.
	os.MkdirAll(filepath.Join(versionsDir, "001"), 0755)
	if err := deploy.PruneOldVersions(appsDir, slug, 5, filepath.Join(appsDir, slug, "versions", "001")); err != nil {
		t.Fatalf("PruneOldVersions: %v", err)
	}

	entries, _ := os.ReadDir(versionsDir)
	if len(entries) != 1 {
		t.Errorf("expected 1 entry, got %d", len(entries))
	}
}
