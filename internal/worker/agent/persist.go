// internal/worker/agent/persist.go
package agent

import (
	"fmt"
	"os"
	"path/filepath"
)

// syncDirHook fsyncs a directory so a rename inside it survives a crash, not
// just becomes visible to other processes. It is a package var so tests can
// inject a failing sync without needing an unusual filesystem; production
// always uses syncDir.
var syncDirHook = syncDir

// createTempHook creates the temporary file persistAtomically writes through
// before its rename. It is a package var, defaulting to os.CreateTemp, so
// tests can force a deterministic persist failure (a full disk, a permissions
// error, anything CreateTemp itself can return) without depending on real
// filesystem permission semantics: an atomic write that goes through a
// rename is deliberately insensitive to the destination file's own
// permission bits (rename only checks the containing directory), so a
// filesystem-permission trick that fails a naive in-place os.WriteFile does
// not reliably fail this implementation.
var createTempHook = os.CreateTemp

func syncDir(dir string) error {
	f, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer f.Close()
	return f.Sync()
}

// persistAtomically durably writes data to path: it writes to a temporary file
// in the same directory, fsyncs it, then renames it over path. The rename is
// the commit point: on POSIX it is atomic, so a reader of path always sees
// either the old content in full or the new content in full, never a partial
// write. Any failure up to and including the rename leaves path untouched and
// is returned as err, with the temp file removed.
//
// Once the rename has succeeded the new content is already durable under its
// final name; persistAtomically then fsyncs the parent directory so the
// rename itself (the directory-entry update) survives a crash, not just the
// file content. That fsync is reported separately as dirErr rather than err:
// a failure there is a durability warning about surviving a crash between now
// and the next fsync of this directory, not a failure of this write, so
// callers should log it but still treat the write as successful.
func persistAtomically(path string, data []byte, perm os.FileMode) (dirErr, err error) {
	dir := filepath.Dir(path)
	tmp, err := createTempHook(dir, filepath.Base(path)+".tmp-*")
	if err != nil {
		return nil, fmt.Errorf("create temp file: %w", err)
	}
	tmpPath := tmp.Name()
	cleanup := true
	defer func() {
		if cleanup {
			_ = os.Remove(tmpPath)
		}
	}()

	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return nil, fmt.Errorf("write temp file: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return nil, fmt.Errorf("sync temp file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return nil, fmt.Errorf("close temp file: %w", err)
	}
	if err := os.Chmod(tmpPath, perm); err != nil {
		return nil, fmt.Errorf("chmod temp file: %w", err)
	}

	if err := os.Rename(tmpPath, path); err != nil {
		return nil, fmt.Errorf("rename into place: %w", err)
	}
	// The rename committed: the temp file no longer exists under tmpPath, so
	// there is nothing left for the deferred cleanup to remove.
	cleanup = false

	return syncDirHook(dir), nil
}
