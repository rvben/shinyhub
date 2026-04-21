// Package data implements the persistent per-app storage area exposed via
// `PUT/DELETE/GET /api/apps/:slug/data[/*path]` and the `shiny data` CLI.
package data

import (
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"
)

const (
	// ReservedPrefix marks platform-owned filenames inside a data dir. The
	// `.shinyhub-upload-tmp/` directory uses this prefix.
	ReservedPrefix = ".shinyhub-"
	// UploadTempDir is the per-app subdir under which atomic-rename tempfiles
	// are written.
	UploadTempDir  = ".shinyhub-upload-tmp"
	maxRelPathLen  = 512
)

// ErrInvalidPath is returned for any rel path that fails sanitization.
var ErrInvalidPath = errors.New("invalid path")

// AppDataDir returns the absolute (or root-relative) path of slug's data dir.
func AppDataDir(root, slug string) string {
	return filepath.Join(root, slug)
}

// SanitizeRelPath validates the user-supplied rel path inside a data dir.
// Returns the cleaned, forward-slash relative path on success; ErrInvalidPath
// otherwise. Caller is responsible for any further `os.Lstat` per-segment
// symlink checks during traversal.
func SanitizeRelPath(rel string) (string, error) {
	if rel == "" {
		return "", fmt.Errorf("%w: empty", ErrInvalidPath)
	}
	if len(rel) > maxRelPathLen {
		return "", fmt.Errorf("%w: too long", ErrInvalidPath)
	}
	if strings.ContainsRune(rel, '\x00') {
		return "", fmt.Errorf("%w: null byte", ErrInvalidPath)
	}
	if strings.HasSuffix(rel, "/") {
		return "", fmt.Errorf("%w: trailing slash", ErrInvalidPath)
	}
	if filepath.IsAbs(rel) || strings.HasPrefix(rel, "/") {
		return "", fmt.Errorf("%w: absolute", ErrInvalidPath)
	}
	// Reject any '..' segment in the raw input — even if path.Clean would
	// collapse it to a safe location, it's a sign of a probing client and
	// we don't want to silently accept traversal attempts.
	for _, seg := range strings.Split(rel, "/") {
		if seg == ".." {
			return "", fmt.Errorf("%w: traversal segment", ErrInvalidPath)
		}
	}
	clean := path.Clean(rel)
	if clean == "." || clean == ".." || strings.HasPrefix(clean, "../") {
		return "", fmt.Errorf("%w: escape", ErrInvalidPath)
	}
	first := clean
	if i := strings.IndexByte(clean, '/'); i >= 0 {
		first = clean[:i]
	}
	if strings.HasPrefix(first, ReservedPrefix) {
		return "", fmt.Errorf("%w: reserved prefix %q", ErrInvalidPath, ReservedPrefix)
	}
	return clean, nil
}

// SafeJoin resolves dataDir/rel to an absolute file path while refusing any
// symlink traversal: if any existing segment along the path (parent or
// destination) is a symbolic link, ErrInvalidPath is returned. The dataDir
// itself is trusted (operators may legitimately point app_data_dir at a
// symlinked volume); only entries created underneath it are checked.
//
// Pass an already-cleaned relative path (output of SanitizeRelPath). Missing
// trailing segments are allowed — they're what Put will create — but a
// non-directory in the middle of the path is rejected.
//
// NOTE on TOCTOU: the check is followed by a separate write/unlink syscall,
// so a determined attacker who can create symlinks inside the data dir
// concurrently with the request can still race past this guard. Closing
// that fully needs openat2(RESOLVE_NO_SYMLINKS), which is Linux-specific.
// This guard catches the realistic case (symlink planted in advance) and
// matches the security posture of similar Go services.
func SafeJoin(dataDir, rel string) (string, error) {
	cur := dataDir
	segments := strings.Split(rel, "/")
	for i, seg := range segments {
		cur = filepath.Join(cur, seg)
		fi, err := os.Lstat(cur)
		if err != nil {
			if os.IsNotExist(err) {
				// Everything from here down is unborn; nothing to check.
				return filepath.Join(dataDir, rel), nil
			}
			return "", err
		}
		if fi.Mode()&os.ModeSymlink != 0 {
			return "", fmt.Errorf("%w: symlink in path", ErrInvalidPath)
		}
		if i < len(segments)-1 && !fi.IsDir() {
			return "", fmt.Errorf("%w: not a directory", ErrInvalidPath)
		}
	}
	return cur, nil
}
