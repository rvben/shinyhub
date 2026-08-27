package localrun

import (
	"context"
	"time"

	"github.com/rvben/shinyhub/internal/sourcewatch"
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
	changes := sourcewatch.Changes(ctx, dir, sourcewatch.Options{
		Interval:      500 * time.Millisecond,
		ExcludeDirs:   exclude,
		ExternalFiles: externalFiles,
	})
	if ready != nil {
		close(ready)
	}
	for {
		select {
		case <-ctx.Done():
			return nil
		case _, ok := <-changes:
			if !ok {
				return nil
			}
			onChange()
		}
	}
}
