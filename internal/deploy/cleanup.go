package deploy

import (
	"os"
	"path/filepath"
	"sort"
)

// PruneOldVersions removes extracted version directories and bundle ZIPs beyond
// the newest `keep` entries for the given app. The activeDir is never deleted,
// even if it falls outside the retention window.
func PruneOldVersions(appsDir, slug string, keep int, activeDir string) error {
	if keep <= 0 {
		keep = 5
	}

	versionsDir := filepath.Join(appsDir, slug, "versions")
	bundlesDir := filepath.Join(appsDir, slug, "bundles")

	if err := pruneDir(versionsDir, keep, activeDir, false); err != nil {
		return err
	}
	return pruneDir(bundlesDir, keep, "", true)
}

// pruneDir removes old entries in dir, keeping the newest `keep` entries.
// skipPath (if non-empty) is never removed.
// isFiles=true treats entries as files (bundles); false treats them as directories (versions).
func pruneDir(dir string, keep int, skipPath string, isFiles bool) error {
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
	sort.Slice(all, func(i, j int) bool { return all[i].name < all[j].name })

	toDelete := len(all) - keep
	for i := 0; i < toDelete && i < len(all); i++ {
		c := all[i]
		if c.path == skipPath {
			continue
		}
		if isFiles {
			os.Remove(c.path)
		} else {
			os.RemoveAll(c.path)
		}
	}
	return nil
}
