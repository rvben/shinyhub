package storage

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"golang.org/x/sys/unix"
)

// BackupLockPath returns the path of the fence file that coordinates
// `shinyhub backup` with every operation that deletes a version directory or
// bundle ZIP under AppsDir. It is a name of its own, deliberately distinct
// from the fleet lifecycle lock internal/api uses to serialize deploys,
// rollbacks, and restarts against each other: every mutating API request
// takes that lock shared, and the demand path that wakes a hibernated app for
// a visitor blocks on it too. A backup's tar walk over a large app-data tree
// can run for minutes, so holding the same lock the whole control plane
// depends on for that long would freeze every deploy, stop, restart, and
// settings save, and stop hibernated apps from waking, for as long as the
// backup runs. This lock exists so backup and retention pruning coordinate
// with each other, and with nothing else.
func BackupLockPath(appsDir string) string {
	return filepath.Join(appsDir, LockDirName, "backup.lock")
}

func openBackupLockFile(appsDir string) (*os.File, error) {
	lockDir := filepath.Join(appsDir, LockDirName)
	if err := os.MkdirAll(lockDir, 0o750); err != nil {
		return nil, fmt.Errorf("create lock dir %s: %w", lockDir, err)
	}
	f, err := os.OpenFile(BackupLockPath(appsDir), os.O_CREATE|os.O_RDWR, 0o640)
	if err != nil {
		return nil, fmt.Errorf("open backup lock: %w", err)
	}
	return f, nil
}

// AcquireBackupFence opens and flocks the backup fence file in the given mode
// (unix.LOCK_SH or unix.LOCK_EX), blocking until it is available, and returns
// a release function. `shinyhub backup` holds it shared from before the DB
// snapshot through the end of the apps-tree walk: two backups running at once
// each hold it shared and do not conflict with each other (their archives are
// independent), but a shared holder blocks a new exclusive holder from
// starting, which is what stops a prune from running concurrently.
func AcquireBackupFence(appsDir string, mode int) (release func(), err error) {
	f, err := openBackupLockFile(appsDir)
	if err != nil {
		return nil, err
	}
	if err := unix.Flock(int(f.Fd()), mode); err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("lock backup fence: %w", err)
	}
	var once sync.Once
	return func() {
		once.Do(func() {
			_ = unix.Flock(int(f.Fd()), unix.LOCK_UN)
			_ = f.Close()
		})
	}, nil
}

// TryAcquireBackupFence attempts to take the backup fence file exclusively
// without blocking. ok=false (with err=nil) means a backup currently holds it
// shared, i.e. is mid-walk over AppsDir: the caller must treat that as "skip
// this round", never as an error, because retention is best-effort and a
// skipped round is caught up by the next deploy's prune once the backup has
// released the fence.
func TryAcquireBackupFence(appsDir string) (release func(), ok bool, err error) {
	f, err := openBackupLockFile(appsDir)
	if err != nil {
		return nil, false, err
	}
	if flockErr := unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB); flockErr != nil {
		_ = f.Close()
		if errors.Is(flockErr, unix.EWOULDBLOCK) || errors.Is(flockErr, unix.EAGAIN) {
			return nil, false, nil
		}
		return nil, false, fmt.Errorf("lock backup fence: %w", flockErr)
	}
	var once sync.Once
	return func() {
		once.Do(func() {
			_ = unix.Flock(int(f.Fd()), unix.LOCK_UN)
			_ = f.Close()
		})
	}, true, nil
}
