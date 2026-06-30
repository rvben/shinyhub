package process_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/rvben/shinyhub/internal/process"
)

func TestSyncR_NoLockfile(t *testing.T) {
	dir := t.TempDir()
	// No renv.lock present — SyncR should be a no-op.
	if err := process.SyncR(context.Background(), dir); err != nil {
		t.Errorf("SyncR with no renv.lock: expected nil, got %v", err)
	}
}

func TestSyncR_WithLockfile_RNotInstalled(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "renv.lock"), []byte(`{"R":{"Version":"4.3.0"}}`), 0644)

	// If R is not installed, SyncR should fail with a clear error.
	// This test is only meaningful when R is absent; skip otherwise.
	if _, err := exec.LookPath("Rscript"); err == nil {
		t.Skip("R is installed; skipping absence test")
	}
	err := process.SyncR(context.Background(), dir)
	if err == nil {
		t.Error("expected error when R not installed, got nil")
	}
}

// SyncR reports a build-timeout when the context is already expired, regardless
// of whether R is installed (it keys off ctx.Err(), not the failure text).
func TestSyncR_BuildTimeout(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "renv.lock"), []byte(`{"R":{"Version":"4.3.0"}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Nanosecond)
	defer cancel()
	time.Sleep(2 * time.Millisecond)
	err := process.SyncR(ctx, dir)
	if err == nil || !strings.Contains(err.Error(), "build exceeded the build timeout") {
		t.Fatalf("want build-timeout error, got %v", err)
	}
}
