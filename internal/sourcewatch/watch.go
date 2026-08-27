// Package sourcewatch provides a dependency-free filesystem change stream for
// ShinyHub's local and remote development loops.
package sourcewatch

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

// Options controls how a source tree is observed.
type Options struct {
	// Interval is the polling cadence. Values <= 0 use 500 ms.
	Interval time.Duration
	// ExcludeDirs contains directory basenames whose complete subtrees are
	// ignored, such as .git and generated dependency environments.
	ExcludeDirs []string
	// ExternalFiles adds a bounded set of files outside Root. Fleet development
	// uses these for shared bundle inputs.
	ExternalFiles []string
}

// Changes establishes a baseline synchronously, then returns a coalescing
// stream that receives whenever the source snapshot changes. The channel closes
// when ctx is cancelled. A capacity of one is deliberate: a slow deployment
// needs to know that something changed while it was running, not how many
// editor writes produced the final tree.
func Changes(ctx context.Context, root string, opts Options) <-chan struct{} {
	interval := opts.Interval
	if interval <= 0 {
		interval = 500 * time.Millisecond
	}
	excludeSet := make(map[string]bool, len(opts.ExcludeDirs))
	for _, name := range opts.ExcludeDirs {
		excludeSet[name] = true
	}
	last := scanSourcesSnapshot(root, excludeSet, opts.ExternalFiles)
	changes := make(chan struct{}, 1)
	go func() {
		defer close(changes)
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				current := scanSourcesSnapshot(root, excludeSet, opts.ExternalFiles)
				if current == last {
					continue
				}
				last = current
				select {
				case changes <- struct{}{}:
				default:
				}
			}
		}
	}()
	return changes
}

func scanSourcesSnapshot(root string, excludeSet map[string]bool, externalFiles []string) [sha256.Size]byte {
	h := sha256.New()
	tree := scanTreeSnapshot(root, excludeSet)
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

// scanTreeSnapshot fingerprints metadata rather than contents, keeping the
// frequent scan cheap even for bundles with large static assets. The canonical
// bundle digest remains the final authority before a remote deployment.
func scanTreeSnapshot(root string, excludeSet map[string]bool) [sha256.Size]byte {
	h := sha256.New()
	_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			_, _ = fmt.Fprintf(h, "error:%s:%v\n", path, err)
			return nil
		}
		if d.IsDir() && excludeSet[d.Name()] && path != root {
			return filepath.SkipDir
		}
		info, err := d.Info()
		if err != nil {
			_, _ = fmt.Fprintf(h, "error:%s:%v\n", path, err)
			return nil
		}
		rel, _ := filepath.Rel(root, path)
		_, _ = fmt.Fprintf(h, "%s\x00%d\x00%d\x00%d\n", filepath.ToSlash(rel), info.Mode(), info.Size(), info.ModTime().UnixNano())
		return nil
	})
	var snapshot [sha256.Size]byte
	copy(snapshot[:], h.Sum(nil))
	return snapshot
}
