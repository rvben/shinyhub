//go:build linux

package data

import (
	"errors"
	"fmt"
	"path/filepath"

	"golang.org/x/sys/unix"
)

// errAtomicUnsupported signals that the kernel lacks openat2 (pre-4.18) and
// the caller should fall back to the portable SafeJoin guard. Any other
// resolution failure (a symlink in the path, an escape attempt) is reported
// as ErrInvalidPath instead.
var errAtomicUnsupported = errors.New("openat2 unsupported")

// openParentBeneath opens the directory that should contain clean's final
// segment, resolving every intermediate component atomically under dataDir
// with RESOLVE_BENEATH|RESOLVE_NO_SYMLINKS. dataDir itself is trusted (it may
// legitimately be a symlinked volume) so it is opened following symlinks; only
// the rel path underneath it is symlink-checked. The returned fd must be
// closed by the caller. base is clean's last segment.
func openParentBeneath(dataDir, clean string) (fd int, base string, err error) {
	rootFd, err := unix.Open(dataDir, unix.O_DIRECTORY|unix.O_RDONLY|unix.O_CLOEXEC, 0)
	if err != nil {
		return -1, "", fmt.Errorf("open data dir: %w", err)
	}
	defer unix.Close(rootFd)

	dir := filepath.Dir(clean)
	base = filepath.Base(clean)
	how := &unix.OpenHow{
		Flags:   uint64(unix.O_DIRECTORY | unix.O_RDONLY | unix.O_CLOEXEC),
		Resolve: unix.RESOLVE_BENEATH | unix.RESOLVE_NO_SYMLINKS,
	}
	parentFd, err := unix.Openat2(rootFd, dir, how)
	if err != nil {
		switch {
		case errors.Is(err, unix.ENOSYS):
			return -1, "", errAtomicUnsupported
		case errors.Is(err, unix.ELOOP), errors.Is(err, unix.EXDEV):
			return -1, "", fmt.Errorf("%w: symlink in path", ErrInvalidPath)
		case errors.Is(err, unix.ENOENT), errors.Is(err, unix.ENOTDIR):
			return -1, "", fmt.Errorf("%w: parent missing", ErrInvalidPath)
		default:
			return -1, "", fmt.Errorf("resolve parent: %w", err)
		}
	}
	return parentFd, base, nil
}

// atomicReplace renames tmpPath onto dataDir/clean using renameat against a
// symlink-safe parent fd, so no component of the destination can be swapped
// for a symlink between resolution and the rename. tmpPath lives in the
// platform-owned UploadTempDir, which is resolved the same way.
func atomicReplace(dataDir, clean, tmpPath string) error {
	destFd, destBase, err := openParentBeneath(dataDir, clean)
	if err != nil {
		return err
	}
	defer unix.Close(destFd)

	tmpRel, err := filepath.Rel(dataDir, tmpPath)
	if err != nil {
		return fmt.Errorf("temp path outside data dir: %w", err)
	}
	tmpFd, tmpBase, err := openParentBeneath(dataDir, tmpRel)
	if err != nil {
		return err
	}
	defer unix.Close(tmpFd)

	if err := unix.Renameat(tmpFd, tmpBase, destFd, destBase); err != nil {
		return fmt.Errorf("rename: %w", err)
	}
	return nil
}

// atomicDelete unlinks dataDir/clean through a symlink-safe parent fd. It
// fstatat's the entry first (without following symlinks) and refuses anything
// that is not a regular file, mirroring the portable Delete contract.
func atomicDelete(dataDir, clean string) error {
	parentFd, base, err := openParentBeneath(dataDir, clean)
	if err != nil {
		return err
	}
	defer unix.Close(parentFd)

	var st unix.Stat_t
	if err := unix.Fstatat(parentFd, base, &st, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		if errors.Is(err, unix.ENOENT) {
			return ErrFileNotFound
		}
		return err
	}
	if st.Mode&unix.S_IFMT != unix.S_IFREG {
		return ErrNotAFile
	}
	if err := unix.Unlinkat(parentFd, base, 0); err != nil {
		if errors.Is(err, unix.ENOENT) {
			return ErrFileNotFound
		}
		return err
	}
	return nil
}
