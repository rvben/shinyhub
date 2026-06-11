package lifecycle

import (
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rvben/shinyhub/internal/db"
	"github.com/rvben/shinyhub/internal/dbtest"
)

// TestValidateNativeProcess is the P1 regression: recovery must not adopt a
// PID/port pair unless the process is really our app (working dir matches the
// bundle) and something is actually serving on the recorded port. It runs
// against the test process itself so it exercises the real gopsutil cwd read
// and a real TCP dial without spawning external binaries.
func TestValidateNativeProcess(t *testing.T) {
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	pid := os.Getpid()

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()
	servingPort := ln.Addr().(*net.TCPAddr).Port

	// A port nothing is listening on: open then immediately close.
	ln2, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen2: %v", err)
	}
	deadPort := ln2.Addr().(*net.TCPAddr).Port
	ln2.Close()

	t.Run("valid: matching cwd and live port", func(t *testing.T) {
		if err := validateNativeProcess(pid, servingPort, cwd); err != nil {
			t.Errorf("expected valid, got: %v", err)
		}
	})

	t.Run("reject: cwd mismatch (pid reuse)", func(t *testing.T) {
		err := validateNativeProcess(pid, servingPort, "/definitely/not/our/bundle")
		if err == nil {
			t.Fatal("expected rejection on cwd mismatch, got nil")
		}
		if !strings.Contains(err.Error(), "does not match bundle") {
			t.Errorf("unexpected error: %v", err)
		}
	})

	t.Run("reject: stale port (nothing listening)", func(t *testing.T) {
		err := validateNativeProcess(pid, deadPort, cwd)
		if err == nil {
			t.Fatal("expected rejection on dead port, got nil")
		}
		if !strings.Contains(err.Error(), "not accepting connections") {
			t.Errorf("unexpected error: %v", err)
		}
	})

	t.Run("reject: nonexistent pid", func(t *testing.T) {
		// PID 2^31-1 is effectively never allocated.
		if err := validateNativeProcess(1<<31-1, servingPort, ""); err == nil {
			t.Error("expected rejection for nonexistent pid, got nil")
		}
	})

	t.Run("no bundle dir: port probe still enforced", func(t *testing.T) {
		if err := validateNativeProcess(pid, servingPort, ""); err != nil {
			t.Errorf("expected valid with empty bundleDir + live port, got: %v", err)
		}
		if err := validateNativeProcess(pid, deadPort, ""); err == nil {
			t.Error("expected rejection with empty bundleDir + dead port, got nil")
		}
	})
}

// TestValidateNativeProcess_SymlinkBundleDir is the symlink regression guard:
// on macOS /tmp is a symlink to /private/tmp, so p.Cwd() returns the
// OS-resolved path (/private/tmp/...) while the configured bundle_dir column
// was written with the unresolved symlinked path (/tmp/...). A bare
// filepath.Abs comparison rejects healthy processes as "pid reuse", leaving
// them unmanaged across every ZDT handoff.
//
// The fix: normalize BOTH sides with filepath.EvalSymlinks before comparing.
//
// This test creates a symlink that points at the real test-process cwd, then
// passes the symlinked path as bundleDir. Pre-fix the comparison fails (the
// symlink path != the resolved cwd); post-fix both sides resolve to the same
// real path and the process is accepted.
func TestValidateNativeProcess_SymlinkBundleDir(t *testing.T) {
	realCwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	// Resolve any symlinks in the real cwd so we have a canonical path.
	resolvedCwd, err := filepath.EvalSymlinks(realCwd)
	if err != nil {
		t.Fatalf("eval symlinks cwd: %v", err)
	}

	// Create a symlink in t.TempDir() that points at the resolved real cwd.
	// The temp dir itself may be under /tmp (symlinked to /private/tmp on macOS),
	// so place the symlink in a dir we know is resolvable, and have it target
	// the already-resolved path to keep the setup simple.
	symlinkPath := filepath.Join(t.TempDir(), "bundle-via-symlink")
	if err := os.Symlink(resolvedCwd, symlinkPath); err != nil {
		t.Fatalf("create symlink: %v", err)
	}

	// Verify the symlink really is different from the resolved target
	// (i.e. that filepath.Abs alone would not canonicalize it on this platform).
	// symlinkPath is an absolute path ending in "bundle-via-symlink", which is
	// never equal to resolvedCwd, so the pre-fix comparison always fails.
	if symlinkPath == resolvedCwd {
		t.Skip("symlink path accidentally equals real cwd - skip on this platform")
	}

	pid := os.Getpid()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()
	port := ln.Addr().(*net.TCPAddr).Port

	// Post-fix: validateNativeProcess must accept the symlinked bundleDir because
	// EvalSymlinks(symlinkPath) == resolvedCwd == the process's real cwd.
	if err := validateNativeProcess(pid, port, symlinkPath); err != nil {
		t.Errorf("symlink bundleDir rejected a healthy process (pre-fix behavior): %v", err)
	}
}

// TestWorkerDeclaredGone_JoiningIsNotGone pins the worker-gone contract recovery
// uses to decide whether a remote replica's slot enters the lost-replica healing
// path. A worker is "gone" only when affirmatively down (revoked workers carry
// status down) or its row is missing. A "joining" worker has registered but not
// yet sent its first heartbeat: it is coming up, not gone, so recovery must not
// strand its replicas. An up worker is never gone.
func TestWorkerDeclaredGone_JoiningIsNotGone(t *testing.T) {
	store := dbtest.New(t)

	seed := func(nodeID, status string) {
		t.Helper()
		if err := store.UpsertWorker(db.Worker{
			NodeID: nodeID, AdvertiseAddr: nodeID + ":8443", Tier: "remote", Status: status,
		}); err != nil {
			t.Fatalf("seed worker %s: %v", nodeID, err)
		}
	}
	seed("up-node", "up")
	seed("joining-node", "joining")
	seed("down-node", "down")

	cases := []struct {
		workerID string
		wantGone bool
	}{
		{"up-node", false},
		{"joining-node", false}, // transitional: coming up, not gone
		{"down-node", true},
		{"missing-node", true}, // no row -> treat as gone
		{"", true},             // no owner to wait on
	}
	for _, c := range cases {
		if got := workerDeclaredGone(store, c.workerID); got != c.wantGone {
			t.Errorf("workerDeclaredGone(%q) = %v, want %v", c.workerID, got, c.wantGone)
		}
	}
}
