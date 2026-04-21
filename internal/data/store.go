package data

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
)

// FileInfo describes a single entry returned by Put or List.
type FileInfo struct {
	Path       string `json:"path"`
	Size       int64  `json:"size"`
	SHA256     string `json:"sha256,omitempty"`
	ModifiedAt int64  `json:"modified_at,omitempty"`
}

// Put streams body into <dataDir>/<rel> via an atomic rename. It computes
// SHA-256 in the same pass and returns the resulting FileInfo.
//
// dataDir MUST already point at the per-app data dir (caller resolves via
// AppDataDir). rel is sanitized through SanitizeRelPath.
//
// The size parameter is accepted for API symmetry with quota-aware callers;
// quota enforcement happens at the HTTP boundary before Put is called.
func Put(dataDir, rel string, body io.Reader, size int64) (FileInfo, error) {
	clean, err := SanitizeRelPath(rel)
	if err != nil {
		return FileInfo{}, err
	}
	if err := os.MkdirAll(dataDir, 0o750); err != nil {
		return FileInfo{}, fmt.Errorf("mkdir data dir: %w", err)
	}
	dest, err := SafeJoin(dataDir, clean)
	if err != nil {
		return FileInfo{}, err
	}
	if err := os.MkdirAll(filepath.Dir(dest), 0o750); err != nil {
		return FileInfo{}, fmt.Errorf("mkdir parent: %w", err)
	}
	tempDir := filepath.Join(dataDir, UploadTempDir)
	if err := os.MkdirAll(tempDir, 0o750); err != nil {
		return FileInfo{}, fmt.Errorf("mkdir temp: %w", err)
	}

	tmpPath := filepath.Join(tempDir, uuid.NewString())
	f, err := os.OpenFile(tmpPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o640)
	if err != nil {
		return FileInfo{}, fmt.Errorf("open temp: %w", err)
	}
	success := false
	defer func() {
		if !success {
			_ = os.Remove(tmpPath)
		}
	}()

	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(f, h), body)
	if err != nil {
		f.Close()
		return FileInfo{}, fmt.Errorf("copy: %w", err)
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return FileInfo{}, fmt.Errorf("fsync: %w", err)
	}
	if err := f.Close(); err != nil {
		return FileInfo{}, fmt.Errorf("close: %w", err)
	}
	if err := os.Rename(tmpPath, dest); err != nil {
		return FileInfo{}, fmt.Errorf("rename: %w", err)
	}
	success = true

	return FileInfo{
		Path:   clean,
		Size:   n,
		SHA256: hex.EncodeToString(h.Sum(nil)),
	}, nil
}

// Sentinel errors returned by List, Delete, and DirSize.
var (
	ErrTooManyFiles = errors.New("too many files")
	ErrNotAFile     = errors.New("not a regular file")
	ErrFileNotFound = errors.New("file not found")
)

// List returns all regular files under dataDir, sorted by path, excluding the
// UploadTempDir subtree. It returns ErrTooManyFiles if the number of entries
// would exceed maxEntries. A missing dataDir is treated as empty.
func List(dataDir string, maxEntries int) ([]FileInfo, error) {
	var out []FileInfo
	err := filepath.WalkDir(dataDir, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, relErr := filepath.Rel(dataDir, p)
		if relErr != nil {
			return relErr
		}
		if rel == "." {
			return nil
		}
		// Exclude the upload temp dir and everything inside it.
		first := rel
		if i := strings.IndexByte(rel, filepath.Separator); i >= 0 {
			first = rel[:i]
		}
		if first == UploadTempDir {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if !d.Type().IsRegular() {
			return nil
		}
		if len(out) >= maxEntries {
			return ErrTooManyFiles
		}
		info, infoErr := d.Info()
		if infoErr != nil {
			return infoErr
		}
		out = append(out, FileInfo{
			Path:       filepath.ToSlash(rel),
			Size:       info.Size(),
			ModifiedAt: info.ModTime().Unix(),
		})
		return nil
	})
	if errors.Is(err, ErrTooManyFiles) {
		return nil, ErrTooManyFiles
	}
	// Treat a missing root as an empty result; surface every other walk error.
	if err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out, nil
}

// Delete removes the file at rel inside dataDir. It returns ErrFileNotFound if
// the path does not exist, ErrNotAFile if the path is a directory or non-regular
// entry, and ErrInvalidPath (via SanitizeRelPath) for traversal attempts or
// reserved prefixes.
func Delete(dataDir, rel string) error {
	clean, err := SanitizeRelPath(rel)
	if err != nil {
		return err
	}
	target, err := SafeJoin(dataDir, clean)
	if err != nil {
		return err
	}
	fi, err := os.Lstat(target)
	if err != nil {
		if os.IsNotExist(err) {
			return ErrFileNotFound
		}
		return err
	}
	if !fi.Mode().IsRegular() {
		return ErrNotAFile
	}
	return os.Remove(target)
}

// DirSize returns the total byte size of all regular files under dataDir,
// excluding the UploadTempDir subtree.
func DirSize(dataDir string) (int64, error) {
	var total int64
	err := filepath.WalkDir(dataDir, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() && d.Name() == UploadTempDir && p != dataDir {
			return filepath.SkipDir
		}
		if !d.Type().IsRegular() {
			return nil
		}
		info, infoErr := d.Info()
		if infoErr != nil {
			return infoErr
		}
		total += info.Size()
		return nil
	})
	if err != nil && os.IsNotExist(err) {
		return 0, nil
	}
	return total, err
}

// CleanupUploadTemp removes entries inside the UploadTempDir that are older
// than maxAge. Only immediate children are inspected (no recursion). Errors
// from individual removals are collected and joined.
func CleanupUploadTemp(dataDir string, maxAge time.Duration) error {
	tmpDir := filepath.Join(dataDir, UploadTempDir)
	entries, err := os.ReadDir(tmpDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	cutoff := time.Now().Add(-maxAge)
	var errs []error
	for _, entry := range entries {
		info, err := entry.Info()
		if err != nil {
			errs = append(errs, err)
			continue
		}
		if info.ModTime().Before(cutoff) {
			if err := os.Remove(filepath.Join(tmpDir, entry.Name())); err != nil && !os.IsNotExist(err) {
				errs = append(errs, err)
			}
		}
	}
	return errors.Join(errs...)
}
