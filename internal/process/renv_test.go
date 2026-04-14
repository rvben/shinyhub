package process_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/rvben/shinyhub/internal/process"
)

func TestSyncR_NoLockfile(t *testing.T) {
	dir := t.TempDir()
	// No renv.lock present — SyncR should be a no-op.
	if err := process.SyncR(dir); err != nil {
		t.Errorf("SyncR with no renv.lock: expected nil, got %v", err)
	}
}

func TestSyncR_WithLockfile_RNotInstalled(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "renv.lock"), []byte(`{"R":{"Version":"4.3.0"}}`), 0644)

	// If R is not installed, SyncR should fail with a clear error.
	// This test is only meaningful when R is absent; skip otherwise.
	if _, err := os.Stat("/usr/bin/Rscript"); err == nil {
		t.Skip("R is installed; skipping absence test")
	}
	err := process.SyncR(dir)
	if err == nil {
		t.Error("expected error when R not installed, got nil")
	}
}
