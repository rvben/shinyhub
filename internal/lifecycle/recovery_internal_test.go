package lifecycle

import (
	"net"
	"os"
	"strings"
	"testing"

	"github.com/rvben/shinyhub/internal/db"
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

// TestWorkerDeclaredGone_JoiningIsNotGone pins the worker-gone contract recovery
// uses to decide whether a remote replica's slot enters the lost-replica healing
// path. A worker is "gone" only when affirmatively down (revoked workers carry
// status down) or its row is missing. A "joining" worker has registered but not
// yet sent its first heartbeat: it is coming up, not gone, so recovery must not
// strand its replicas. An up worker is never gone.
func TestWorkerDeclaredGone_JoiningIsNotGone(t *testing.T) {
	store, err := db.Open(":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if err := store.Migrate(); err != nil {
		t.Fatalf("migrate: %v", err)
	}

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
