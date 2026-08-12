package localrun

import (
	"context"
	"crypto/sha256"
	"fmt"
	"io/fs"
	"path/filepath"
	"time"
)

// watchAndRestart polls dir every ~500 ms for changes in its source snapshot.
// The snapshot includes every path, type, size, and nanosecond mtime, so file
// deletion and file/directory replacement are detected even when the newest
// file in the tree did not change. The watcher returns when ctx is cancelled.
//
// exclude lists directory-name basenames (e.g. ".venv", ".git") whose entire
// subtree is skipped during the snapshot. No external dependencies: stdlib only.
func watchAndRestart(ctx context.Context, dir string, exclude []string, onChange func()) error {
	excludeSet := make(map[string]bool, len(exclude))
	for _, e := range exclude {
		excludeSet[e] = true
	}

	// Initial scan: establish the baseline so a pre-existing recent
	// modification does not immediately trigger onChange.
	lastSnapshot := scanSnapshot(dir, excludeSet)

	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			current := scanSnapshot(dir, excludeSet)
			if current != lastSnapshot {
				lastSnapshot = current
				onChange()
			}
		}
	}
}

// scanSnapshot fingerprints metadata, not contents, keeping frequent scans
// cheap even for bundles with large static assets. filepath.WalkDir is lexical,
// so identical trees produce identical hashes.
func scanSnapshot(dir string, excludeSet map[string]bool) [sha256.Size]byte {
	h := sha256.New()
	_ = filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			_, _ = fmt.Fprintf(h, "error:%s:%v\n", path, err)
			return nil // skip unreadable entries; don't abort the walk
		}
		if d.IsDir() && excludeSet[d.Name()] && path != dir {
			return filepath.SkipDir
		}
		info, err := d.Info()
		if err != nil {
			_, _ = fmt.Fprintf(h, "error:%s:%v\n", path, err)
			return nil
		}
		rel, _ := filepath.Rel(dir, path)
		_, _ = fmt.Fprintf(h, "%s\x00%d\x00%d\x00%d\n", filepath.ToSlash(rel), info.Mode(), info.Size(), info.ModTime().UnixNano())
		return nil
	})
	var snapshot [sha256.Size]byte
	copy(snapshot[:], h.Sum(nil))
	return snapshot
}
