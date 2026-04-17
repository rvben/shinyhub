package deploy

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

// ErrQuotaExceeded is returned when an app's on-disk footprint exceeds its
// configured per-app quota after a deploy would be committed.
var ErrQuotaExceeded = errors.New("app disk quota exceeded")

// MiB is 1 mebibyte expressed in bytes.
const MiB = int64(1) << 20

// CheckAppQuota returns the measured on-disk usage (in bytes) of appsDir/slug.
// If quotaMB > 0 and usage exceeds that limit, the returned error wraps
// ErrQuotaExceeded with the measured and allowed byte counts. quotaMB <= 0
// disables the check and CheckAppQuota always returns a nil error.
func CheckAppQuota(appsDir, slug string, quotaMB int) (int64, error) {
	used, err := DirSize(filepath.Join(appsDir, slug))
	if err != nil {
		return 0, fmt.Errorf("measure app size: %w", err)
	}
	if quotaMB <= 0 {
		return used, nil
	}
	limit := int64(quotaMB) * MiB
	if used > limit {
		return used, fmt.Errorf("%w: %d bytes used, %d bytes allowed", ErrQuotaExceeded, used, limit)
	}
	return used, nil
}

// DirSize returns the sum of sizes (in bytes) of every regular file reachable
// from root. Symlinks are not followed — only their own metadata is counted,
// which is what we want for quota accounting. A missing root returns (0, nil)
// so callers can use it for first-deploy paths without a pre-stat.
func DirSize(root string) (int64, error) {
	var total int64
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			if os.IsNotExist(err) && path == root {
				return fs.SkipAll
			}
			return err
		}
		if !d.Type().IsRegular() {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			if os.IsNotExist(err) {
				return nil
			}
			return err
		}
		total += info.Size()
		return nil
	})
	if err != nil {
		return 0, err
	}
	return total, nil
}
