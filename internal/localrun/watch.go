package localrun

import (
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
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
	return watchAndRestartSources(ctx, dir, exclude, nil, onChange)
}

// watchAndRestartSources observes the app tree plus explicit files that live
// outside it, such as fleet bundle inputs. External files are content-hashed:
// they are few, bounded bundle inputs, and content hashing avoids missing an
// edit that preserves both size and coarse filesystem timestamps.
func watchAndRestartSources(ctx context.Context, dir string, exclude, externalFiles []string, onChange func()) error {
	return watchAndRestartSourcesReady(ctx, dir, exclude, externalFiles, nil, onChange)
}

func watchAndRestartSourcesReady(ctx context.Context, dir string, exclude, externalFiles []string, ready chan<- struct{}, onChange func()) error {
	excludeSet := make(map[string]bool, len(exclude))
	for _, e := range exclude {
		excludeSet[e] = true
	}

	// Initial scan: establish the baseline so a pre-existing recent
	// modification does not immediately trigger onChange.
	lastSnapshot := scanSourcesSnapshot(dir, excludeSet, externalFiles)
	if ready != nil {
		close(ready)
	}

	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			current := scanSourcesSnapshot(dir, excludeSet, externalFiles)
			if current != lastSnapshot {
				lastSnapshot = current
				onChange()
			}
		}
	}
}

func scanSourcesSnapshot(dir string, excludeSet map[string]bool, externalFiles []string) [sha256.Size]byte {
	h := sha256.New()
	tree := scanSnapshot(dir, excludeSet)
	_, _ = fmt.Fprintf(h, "tree:%x\n", tree)
	paths := append([]string(nil), externalFiles...)
	sort.Strings(paths)
	last := ""
	for _, path := range paths {
		if path == last {
			continue
		}
		last = path
		info, err := os.Lstat(path)
		if err != nil {
			_, _ = fmt.Fprintf(h, "external:%s:error:%v\n", path, err)
			continue
		}
		_, _ = fmt.Fprintf(h, "external:%s:%d:%d:%d\n", path, info.Mode(), info.Size(), info.ModTime().UnixNano())
		if !info.Mode().IsRegular() {
			continue
		}
		f, err := os.Open(path)
		if err != nil {
			_, _ = fmt.Fprintf(h, "open-error:%v\n", err)
			continue
		}
		_, _ = io.Copy(h, f)
		_ = f.Close()
	}
	var snapshot [sha256.Size]byte
	copy(snapshot[:], h.Sum(nil))
	return snapshot
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
