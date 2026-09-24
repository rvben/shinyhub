package api

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/rvben/shinyhub/internal/config"
	"github.com/rvben/shinyhub/internal/storage"
	"golang.org/x/sys/unix"
)

// TestFleetMutationFence_NotBlockedByBackupMidWalk regresses the freeze
// described in internal/backup's package doc: every mutating API request
// takes the fleet mutation fence shared (router.go), and the foreground path
// that wakes a hibernated app for a visitor blocks on the same fence.
// `shinyhub backup` used to hold that identical lock file exclusively for as
// long as its apps+app-data tar walk took, which could be minutes, freezing
// every deploy, stop, restart, and settings save for that whole time.
//
// A backup now holds a separate, dedicated fence (storage.AcquireBackupFence)
// instead. This test simulates a backup mid-walk by holding that fence
// shared, then proves acquireFleetMutationFence still completes promptly:
// the two fences live on different files and must never contend.
func TestFleetMutationFence_NotBlockedByBackupMidWalk(t *testing.T) {
	appsDir := t.TempDir()
	lockDir := filepath.Join(appsDir, storage.LockDirName)
	if err := os.MkdirAll(lockDir, 0o750); err != nil {
		t.Fatalf("mkdir lock dir: %v", err)
	}
	s := &Server{cfg: &config.Config{}, appOperationLockDir: lockDir}

	releaseBackup, err := storage.AcquireBackupFence(appsDir, unix.LOCK_SH)
	if err != nil {
		t.Fatalf("simulate backup mid-walk: %v", err)
	}
	defer releaseBackup()

	done := make(chan error, 1)
	go func() {
		release, err := s.acquireFleetMutationFence(unix.LOCK_SH)
		if release != nil {
			release()
		}
		done <- err
	}()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("acquireFleetMutationFence: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("acquireFleetMutationFence blocked while a backup held only the separate backup fence; " +
			"a mutating API request, and the demand path that wakes a hibernated app, must never wait " +
			"on a backup's tar walk")
	}
}
