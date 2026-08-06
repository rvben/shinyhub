package process

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

// TestWithDelegateHint_NamesDelegateOnPermissionError asserts that a
// permission/read-only-class cgroup failure - the signature of a systemd unit
// missing Delegate= - is rewritten to name the remediation. This is the guard
// against regressing to the opaque "mkdir ...: permission denied" the operator
// cannot act on. The wrapped errno must still be recoverable via errors.Is so
// callers that classify the cause keep working.
func TestWithDelegateHint_NamesDelegateOnPermissionError(t *testing.T) {
	for _, errno := range []syscall.Errno{syscall.EACCES, syscall.EPERM, syscall.EROFS} {
		// Wrap the errno exactly as ensureDelegatedBase does: a PathError from an
		// os.Mkdir, then an fmt.Errorf %w on top. This mirrors the real
		// "mkdir /sys/fs/cgroup/.../_supervisor: permission denied" chain.
		cause := &os.PathError{Op: "mkdir", Path: "/sys/fs/cgroup/system.slice/shinyhub.service/_supervisor", Err: errno}
		in := fmt.Errorf("mkdir %s: %w", cause.Path, cause)

		got := withDelegateHint(in)
		msg := got.Error()
		// The hint is copy-pasted verbatim into a unit file, so it must name
		// every controller: one omitted here becomes a limit that silently
		// stops being enforced on the operator's host.
		if !strings.Contains(msg, "Delegate="+RequiredDelegatedControllers) {
			t.Errorf("errno %v: message does not name the fix; got %q", errno, msg)
		}
		for _, controller := range strings.Fields(RequiredDelegatedControllers) {
			if !strings.Contains(msg, controller) {
				t.Errorf("errno %v: remediation omits the %s controller; got %q", errno, controller, msg)
			}
		}
		if !errors.Is(got, errno) {
			t.Errorf("errno %v: wrapped error no longer unwraps to the cause", errno)
		}
		// The original context (the offending path) must be preserved, not swallowed.
		if !strings.Contains(msg, "_supervisor") {
			t.Errorf("errno %v: original error context lost; got %q", errno, msg)
		}
	}
}

// TestWithDelegateHint_PassesThroughOtherErrors asserts a non-permission error
// is returned verbatim, so the more specific "need systemd Delegate=memory"
// message (memory controller genuinely absent) and non-linux stubs are never
// clobbered with a misleading permission remediation.
func TestWithDelegateHint_PassesThroughOtherErrors(t *testing.T) {
	// The exact message ensureDelegatedBase emits when the memory controller is
	// not delegated at all - a distinct, already-actionable failure mode.
	in := errors.New("cgroup /system.slice/shinyhub.service: memory controller not delegated (need systemd Delegate=memory)")
	got := withDelegateHint(in)
	if got != in {
		t.Fatalf("non-permission error was altered: got %q, want it returned unchanged", got.Error())
	}
	if strings.Contains(got.Error(), "Delegate="+RequiredDelegatedControllers) {
		t.Errorf("non-permission error gained a spurious permission remediation: %q", got.Error())
	}
}

// TestShippedUnitDelegatesEveryController pins the shipped systemd unit to the
// controller set the code actually manages. The two drift silently: an operator
// who installs a unit delegating only cpu and memory still gets both visible
// limits enforced, so the missing pids delegation shows up only as a log line
// nobody reads and every replica runs without a fork-bomb cap.
func TestShippedUnitDelegatesEveryController(t *testing.T) {
	unit, err := os.ReadFile(filepath.Join("..", "..", "deploy", "systemd", "shinyhub.service"))
	if err != nil {
		t.Fatalf("read shipped unit: %v", err)
	}
	want := "Delegate=" + RequiredDelegatedControllers
	if !strings.Contains(string(unit), want) {
		t.Fatalf("shipped unit does not contain %q", want)
	}
}
