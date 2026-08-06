// Package fsx holds filesystem helpers whose behaviour the standard library
// deliberately leaves out.
package fsx

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
)

// RemoveAll removes path and everything beneath it, including trees whose own
// directory permissions block the unlink.
//
// os.RemoveAll cannot delete a file inside a directory that lacks the owner
// write bit, and build tooling creates exactly that: renv's package sandbox is
// mode 0555, so every R app whose activation ran leaves a tree os.RemoveAll
// refuses with EACCES. Deleting such an app then fails permanently - the slug
// stays occupied and the reconcile loop retries the same failing unlink
// forever - even though the server owns every byte involved.
//
// On a permission failure this restores owner rwx on the directories of the
// tree and retries once. Only entries that Lstat reports as directories are
// widened, so a symlink is never followed and nothing outside the tree is
// touched.
//
// A permission failure on path's own parent is returned unchanged: removing
// path is not this process's to do when the containing directory denies it.
func RemoveAll(path string) error {
	err := os.RemoveAll(path)
	if err == nil || !errors.Is(err, fs.ErrPermission) {
		return err
	}
	if cerr := widenTree(path); cerr != nil {
		return errors.Join(err, cerr)
	}
	return os.RemoveAll(path)
}

// widenTree grants owner rwx on path and every directory below it. Each
// directory is widened before it is listed: one with no owner read bit cannot
// be read, and one with no owner write bit cannot have its children unlinked,
// so both bits must be in place before the walk descends.
func widenTree(path string) error {
	fi, err := os.Lstat(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		return err
	}
	if !fi.IsDir() {
		return nil
	}
	if perm := fi.Mode().Perm(); perm&0o700 != 0o700 {
		if err := os.Chmod(path, perm|0o700); err != nil {
			return err
		}
	}
	entries, err := os.ReadDir(path)
	if err != nil {
		return err
	}
	var errs []error
	for _, e := range entries {
		// DirEntry.IsDir reports the readdir type, so a symlink to a
		// directory is not a directory here and is left alone.
		if !e.IsDir() {
			continue
		}
		errs = append(errs, widenTree(filepath.Join(path, e.Name())))
	}
	return errors.Join(errs...)
}
