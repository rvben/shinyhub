package data

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"

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
	dest := filepath.Join(dataDir, clean)
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
