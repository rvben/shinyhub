package deploy

import (
	"log/slog"
	"os"
	"path/filepath"

	"github.com/rvben/shinyhub/internal/fsx"
	"github.com/rvben/shinyhub/internal/storage"
)

// PruneOldVersions removes extracted version directories and bundle ZIPs beyond
// the newest `keep` entries for the given app. The activeDir and every
// pinnedDir are never deleted, even outside the retention window.
//
// Retention is best-effort against a concurrent `shinyhub backup`: pruning
// takes the backup fence exclusive and non-blocking before touching any file,
// and skips this round entirely (logging it, returning nil rather than an
// error) when a backup is mid-walk over appsDir. Blocking here instead would
// stall the deploy holding this call for as long as the backup's tar walk
// runs; a skipped round is caught up by the next deploy's prune once the
// backup releases the fence.
func PruneOldVersions(appsDir, slug string, keep int, activeDir string, pinnedDirs ...string) error {
	if keep <= 0 {
		keep = 5
	}

	release, ok, err := storage.TryAcquireBackupFence(appsDir)
	if err != nil {
		return err
	}
	if !ok {
		slog.Warn("prune_old_versions_skipped_backup_in_progress", "slug", slug)
		return nil
	}
	defer release()

	versionsDir := filepath.Join(appsDir, slug, "versions")
	bundlesDir := filepath.Join(appsDir, slug, "bundles")

	pinnedVersions := map[string]bool{filepath.Clean(activeDir): true}
	pinnedBundles := map[string]bool{filepath.Join(bundlesDir, filepath.Base(activeDir)+".zip"): true}
	for _, dir := range pinnedDirs {
		if dir == "" {
			continue
		}
		pinnedVersions[filepath.Clean(dir)] = true
		pinnedBundles[filepath.Join(bundlesDir, filepath.Base(dir)+".zip")] = true
	}

	if err := pruneDir(versionsDir, keep, pinnedVersions, false); err != nil {
		return err
	}
	return pruneDir(bundlesDir, keep, pinnedBundles, true)
}

// pruneDir removes old entries in dir, keeping the newest `keep` entries.
// pinned paths are never removed.
// isFiles=true treats entries as files (bundles); false treats them as directories (versions).
func pruneDir(dir string, keep int, pinned map[string]bool, isFiles bool) error {
	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}

	// os.ReadDir returns entries sorted by name (ascending = oldest first for timestamp names).
	type candidate struct {
		name string
		path string
	}
	var all []candidate
	for _, e := range entries {
		if isFiles && e.IsDir() {
			continue
		}
		if !isFiles && !e.IsDir() {
			continue
		}
		all = append(all, candidate{e.Name(), filepath.Join(dir, e.Name())})
	}

	toDelete := len(all) - keep
	deleted := 0
	for i := 0; deleted < toDelete && i < len(all); i++ {
		c := all[i]
		if pinned[filepath.Clean(c.path)] {
			continue
		}
		if isFiles {
			if err := os.Remove(c.path); err != nil && !os.IsNotExist(err) {
				return err
			}
		} else {
			// A version dir is a build tree, so it can contain directories the
			// standard remove cannot descend into (renv's sandbox is mode
			// 0555). Failing here would silently stop retention from ever
			// reclaiming space for that app.
			if err := fsx.RemoveAll(c.path); err != nil {
				return err
			}
		}
		deleted++
	}
	return nil
}
