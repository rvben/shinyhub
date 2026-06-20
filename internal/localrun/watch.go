package localrun

import (
	"context"
	"io/fs"
	"path/filepath"
	"time"
)

// watchAndRestart polls dir every ~500 ms for changes in file mtimes. When the
// maximum mtime of non-excluded files advances past the last-seen value, it
// calls onChange (debounced: one call per change event, no matter how many
// files changed in a burst). The watcher returns when ctx is cancelled.
//
// exclude lists directory-name basenames (e.g. ".venv", ".git") whose entire
// subtree is skipped during the mtime scan. No external dependencies: stdlib only.
func watchAndRestart(ctx context.Context, dir string, exclude []string, onChange func()) error {
	excludeSet := make(map[string]bool, len(exclude))
	for _, e := range exclude {
		excludeSet[e] = true
	}

	// Initial scan: establish the baseline mtime so a pre-existing recent
	// modification does not immediately trigger onChange.
	lastMax := scanMaxMtime(dir, excludeSet)

	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			current := scanMaxMtime(dir, excludeSet)
			if current.After(lastMax) {
				lastMax = current
				onChange()
			}
		}
	}
}

// scanMaxMtime walks dir and returns the maximum mtime found among non-excluded
// files and directories. Excluded directory subtrees are skipped via fs.SkipDir,
// so their contents never affect the returned timestamp.
func scanMaxMtime(dir string, excludeSet map[string]bool) time.Time {
	var max time.Time
	_ = filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil // skip unreadable entries; don't abort the walk
		}
		if d.IsDir() && excludeSet[d.Name()] && path != dir {
			return filepath.SkipDir
		}
		info, err := d.Info()
		if err != nil {
			return nil
		}
		if t := info.ModTime(); t.After(max) {
			max = t
		}
		return nil
	})
	return max
}
