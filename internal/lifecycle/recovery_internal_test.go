package lifecycle

import (
	"net"
	"os"
	"strings"
	"testing"
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
