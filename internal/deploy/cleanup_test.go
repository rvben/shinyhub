package deploy_test

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/rvben/shinyhub/internal/deploy"
	"github.com/rvben/shinyhub/internal/storage"
	"golang.org/x/sys/unix"
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

	// "007" is both the newest entry and the pinned active dir, so it never
	// counts against keep=5: 6 unpinned entries (001-006) minus keep=5 leaves
	// exactly 1 to delete (the oldest, "001").
	active := filepath.Join(appsDir, slug, "versions", "007")
	if err := deploy.PruneOldVersions(appsDir, slug, 5, active); err != nil {
		t.Fatalf("PruneOldVersions: %v", err)
	}

	entries, _ := os.ReadDir(versionsDir)
	if len(entries) != 6 {
		t.Errorf("expected 6 version dirs, got %d", len(entries))
	}
	// Newest 6 (002-007) should remain: the 5 newest unpinned (002-006) plus
	// the pinned active dir (007), which is kept in addition to keep=5.
	for _, name := range []string{"002", "003", "004", "005", "006", "007"} {
		if _, err := os.Stat(filepath.Join(versionsDir, name)); err != nil {
			t.Errorf("expected %s to exist", name)
		}
	}
	// Only the oldest unpinned entry (001) should be gone.
	if _, err := os.Stat(filepath.Join(versionsDir, "001")); !os.IsNotExist(err) {
		t.Errorf("expected 001 to be deleted")
	}
	// Bundle zips: same accounting, 6 remaining.
	bundleEntries, _ := os.ReadDir(bundlesDir)
	if len(bundleEntries) != 6 {
		t.Errorf("expected 6 bundle zips, got %d", len(bundleEntries))
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

	// "001" is pinned and does not count against keep=5: the 5 unpinned
	// entries (002-006) all fit inside keep=5, so nothing is deleted and all
	// 6 version dirs survive.
	versionEntries, _ := os.ReadDir(versionsDir)
	if len(versionEntries) != 6 {
		t.Errorf("expected 6 version dirs after pruning, got %d", len(versionEntries))
	}
	if _, err := os.Stat(filepath.Join(versionsDir, "002")); err != nil {
		t.Errorf("expected version 002 to survive: %v", err)
	}

	// Bundles: same accounting, nothing deleted.
	bundleEntries, _ := os.ReadDir(bundlesDir)
	if len(bundleEntries) != 6 {
		t.Errorf("expected 6 bundle zips after pruning, got %d", len(bundleEntries))
	}
	if _, err := os.Stat(filepath.Join(bundlesDir, "002.zip")); err != nil {
		t.Errorf("expected bundle 002.zip to survive: %v", err)
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

func TestPruneOldVersions_PreservesSchedulePinnedBundle(t *testing.T) {
	appsDir := t.TempDir()
	slug := "producer-app"
	versionsDir := filepath.Join(appsDir, slug, "versions")
	bundlesDir := filepath.Join(appsDir, slug, "bundles")
	if err := os.MkdirAll(versionsDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(bundlesDir, 0755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"001", "002", "003", "004"} {
		if err := os.MkdirAll(filepath.Join(versionsDir, name), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(bundlesDir, name+".zip"), []byte("x"), 0644); err != nil {
			t.Fatal(err)
		}
	}
	// "001" and "004" are both pinned (schedule-pinned and active) and neither
	// counts against keep=2. The 2 unpinned entries ("002", "003") fit inside
	// keep=2, so nothing is deleted and all 4 versions/bundles survive.
	active := filepath.Join(versionsDir, "004")
	pinnedProducer := filepath.Join(versionsDir, "001")
	if err := deploy.PruneOldVersions(appsDir, slug, 2, active, pinnedProducer); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"001", "002", "003", "004"} {
		if _, err := os.Stat(filepath.Join(versionsDir, name)); err != nil {
			t.Errorf("version %s should not have been pruned: %v", name, err)
		}
		if _, err := os.Stat(filepath.Join(bundlesDir, name+".zip")); err != nil {
			t.Errorf("bundle %s.zip should not have been pruned: %v", name, err)
		}
	}
}

// TestPruneOldVersions_SkipsAndDoesNotBlockDuringBackup regresses the freeze
// described in internal/backup's package doc: retention pruning must never
// wait on a `shinyhub backup` that is mid-walk over AppsDir, because
// PruneOldVersions runs synchronously while a deploy holds its per-slug
// lock (see internal/api/apps.go), and a backup's tar walk over a large
// app-data tree can run for minutes. It simulates a backup mid-walk by
// holding storage.AcquireBackupFence shared (the same fence backup takes
// before its DB snapshot and releases only after the apps-tree walk), then
// proves PruneOldVersions returns quickly, without error, and without
// deleting anything: retention for this round is skipped, not delayed, and
// is caught up by the next deploy's prune once the backup releases the
// fence.
func TestPruneOldVersions_SkipsAndDoesNotBlockDuringBackup(t *testing.T) {
	appsDir := t.TempDir()
	slug := "myapp"
	versionsDir := filepath.Join(appsDir, slug, "versions")
	bundlesDir := filepath.Join(appsDir, slug, "bundles")
	os.MkdirAll(versionsDir, 0755)
	os.MkdirAll(bundlesDir, 0755)

	for _, name := range []string{"001", "002", "003"} {
		os.MkdirAll(filepath.Join(versionsDir, name), 0755)
		os.WriteFile(filepath.Join(bundlesDir, name+".zip"), []byte("x"), 0644)
	}
	active := filepath.Join(versionsDir, "003")

	release, err := storage.AcquireBackupFence(appsDir, unix.LOCK_SH)
	if err != nil {
		t.Fatalf("simulate backup mid-walk: %v", err)
	}
	defer release()

	done := make(chan error, 1)
	go func() { done <- deploy.PruneOldVersions(appsDir, slug, 1, active) }()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("PruneOldVersions: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("PruneOldVersions blocked while the backup fence was held; retention must skip this " +
			"round, not wait for the backup to finish")
	}

	// Retention is best-effort here: the skipped round must leave every
	// version dir and bundle zip untouched, including ones outside the
	// keep=1 window that a real prune would otherwise have removed.
	for _, name := range []string{"001", "002", "003"} {
		if _, err := os.Stat(filepath.Join(versionsDir, name)); err != nil {
			t.Errorf("expected %s to survive the skipped prune round: %v", name, err)
		}
		if _, err := os.Stat(filepath.Join(bundlesDir, name+".zip")); err != nil {
			t.Errorf("expected bundle %s.zip to survive the skipped prune round: %v", name, err)
		}
	}
}
