package deploy

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
)

// ErrQuotaExceeded is returned when an app's on-disk footprint exceeds its
// configured per-app quota after a deploy would be committed.
var ErrQuotaExceeded = errors.New("app disk quota exceeded")

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
