//go:build linux

package deploy

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// pidAlive reports whether pid still exists, using a signal-0 probe.
func pidAlive(pid int) bool {
	err := syscall.Kill(pid, 0)
	return err == nil || err != syscall.ESRCH
}

// TestHelperHookExec_OrphanSweptAfterCleanExit only runs its assertion when
// re-exec'd by TestRunHookExec_OrphanSweptAfterCleanExit with
// HOOKEXEC_HELPER_MODE=orphan set, for the same reason the timeout helper
// above does: a failure to sweep the orphan here means runHookExec returns
// normally but leaves a live process behind, which this process itself
// cannot safely detect and clean up after without the subprocess boundary.
func TestHelperHookExec_OrphanSweptAfterCleanExit(t *testing.T) {
	if os.Getenv("HOOKEXEC_HELPER_MODE") != "orphan" {
		t.Skip("helper test; invoked only via HOOKEXEC_HELPER_MODE=orphan")
	}
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Fatalf("sh not found: %v", err)
	}
	dir := t.TempDir()
	pidFile := filepath.Join(dir, "grandchild.pid")

	var buf bytes.Buffer
	start := time.Now()
	// The leader backgrounds a long-sleeping grandchild, records its PID,
	// then exits immediately with success. No timeout is involved: the
	// context has ample time left when the leader exits on its own.
	err = runHookExec(context.Background(), dir, []string{sh, "-c", "sleep 300 & echo $! > " + pidFile + "; exit 0"}, nil, &buf)
	elapsed := time.Since(start)
	t.Logf("runHookExec returned after %s: err=%v output=%q", elapsed, err, buf.String())
	if err != nil {
		t.Fatalf("expected the hook to succeed, got %v", err)
	}
	if elapsed > hookWaitDelay+5*time.Second {
		t.Fatalf("runHookExec took %s, want well under hookWaitDelay+margin (%s)", elapsed, hookWaitDelay+5*time.Second)
	}

	raw, err := os.ReadFile(pidFile)
	if err != nil {
		t.Fatalf("read grandchild pidfile: %v", err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(raw)))
	if err != nil {
		t.Fatalf("parse grandchild pid %q: %v", raw, err)
	}
	t.Logf("grandchild pid: %d", pid)

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) && pidAlive(pid) {
		time.Sleep(50 * time.Millisecond)
	}
	if pidAlive(pid) {
		t.Fatalf("grandchild pid %d is still alive after runHookExec returned; it was orphaned instead of swept", pid)
	}
}

// TestRunHookExec_OrphanSweptAfterCleanExit is the failing-first regression
// test for the "no upper bound" half of the defect: a hook leader that
// backgrounds a grandchild and then exits cleanly on its own, well before
// any timeout, must not leave that grandchild running on the host. Against
// the pre-fix runHookExec (plain cmd.Run, no process-group cleanup of any
// kind) the grandchild survives indefinitely, so the helper above fails on
// the final liveness check; a naive post-fix implementation that reaps the
// leader before killing -pgid could instead hit a recycled PGID, which this
// drives through a real exec so that failure mode would show up as the
// grandchild surviving too.
func TestRunHookExec_OrphanSweptAfterCleanExit(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("sh not available")
	}
	output, finished, err := runHelperSubprocess(t, "TestHelperHookExec_OrphanSweptAfterCleanExit", "orphan")
	if !finished {
		t.Fatalf("helper subprocess did not return within %s\n%s", helperSubprocessTimeout, output)
	}
	if err != nil {
		t.Fatalf("helper subprocess failed:\n%s", output)
	}
}
