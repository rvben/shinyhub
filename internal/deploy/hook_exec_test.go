package deploy

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// helperSubprocessTimeout bounds how long a driver test waits for a re-exec'd
// helper before concluding the underlying hook exec has hung (the pre-fix
// behaviour: cmd.Run blocks on a pipe an inherited grandchild still holds
// open, and an in-process context deadline cannot unblock a blocking Run
// call). It must exceed hookWaitDelay with a comfortable margin.
const helperSubprocessTimeout = 30 * time.Second

// runHelperSubprocess re-execs the current test binary restricted to the
// named test (expected to be one of the TestHelperHookExec_* functions
// below), in its own process group so the driver can clean up every process
// it spawns even when the helper itself hangs. It returns the combined
// output and whether the helper finished within helperSubprocessTimeout.
func runHelperSubprocess(t *testing.T, testName, mode string) (output string, finished bool, exitErr error) {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^"+testName+"$", "-test.v")
	cmd.Env = append(os.Environ(), "HOOKEXEC_HELPER_MODE="+mode)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	if err := cmd.Start(); err != nil {
		t.Fatalf("start helper subprocess: %v", err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		return out.String(), true, err
	case <-time.After(helperSubprocessTimeout):
		// The helper (and anything it spawned) must not survive a failed
		// test: kill its whole process group before reporting the hang.
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		<-done
		return out.String(), false, nil
	}
}

// TestHelperHookExec_TimeoutKillsBackgroundedGrandchild is not a normal test:
// it only runs its assertion when re-exec'd by
// TestRunHookExec_TimeoutKillsBackgroundedGrandchild with
// HOOKEXEC_HELPER_MODE=timeout set, so that a hang in runHookExec shows up
// to the driver as a subprocess that must be killed rather than as a test
// binary that never returns.
func TestHelperHookExec_TimeoutKillsBackgroundedGrandchild(t *testing.T) {
	if os.Getenv("HOOKEXEC_HELPER_MODE") != "timeout" {
		t.Skip("helper test; invoked only via HOOKEXEC_HELPER_MODE=timeout")
	}
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Fatalf("sh not found: %v", err)
	}

	var buf bytes.Buffer
	dir := t.TempDir()
	pidFile := filepath.Join(dir, "grandchild.pid")
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	start := time.Now()
	err = runHookExec(ctx, dir, []string{sh, "-c", `sleep 300 & echo $! > "$1"; sleep 300`, "hook", pidFile}, nil, &buf)
	elapsed := time.Since(start)

	t.Logf("runHookExec returned after %s: err=%v output=%q", elapsed, err, buf.String())
	raw, readErr := os.ReadFile(pidFile)
	if readErr != nil {
		t.Fatalf("read grandchild pid: %v", readErr)
	}
	pid, convErr := strconv.Atoi(strings.TrimSpace(string(raw)))
	if convErr != nil || pid <= 0 {
		t.Fatalf("parse grandchild pid %q: %v", raw, convErr)
	}
	// Whatever the outcome, nothing the hook started may outlive the test:
	// the hook runs in its own process group, which the grandchild shares.
	if pgid, pgErr := syscall.Getpgid(pid); pgErr == nil {
		t.Cleanup(func() { _ = syscall.Kill(-pgid, syscall.SIGKILL) })
	}
	// Killing only the leader still returns, but only once hookWaitDelay gives
	// up on the pipe the surviving grandchild holds open, so the bound sits
	// below hookWaitDelay.
	if elapsed >= hookWaitDelay/2 {
		t.Fatalf("runHookExec took %s, want under %s (hookWaitDelay=%s): the timeout did not kill the whole process group", elapsed, hookWaitDelay/2, hookWaitDelay)
	}
	// The orphaned grandchild is reparented and reaped by init, so poll until
	// it is gone rather than expecting ESRCH on the first probe.
	deadline := time.Now().Add(3 * time.Second)
	for syscall.Kill(pid, 0) == nil {
		if time.Now().After(deadline) {
			_ = syscall.Kill(pid, syscall.SIGKILL)
			t.Fatalf("backgrounded grandchild %d is still alive after the hook timed out", pid)
		}
		time.Sleep(20 * time.Millisecond)
	}
	if err == nil {
		t.Fatal("expected an error, got nil")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected errors.Is(err, context.DeadlineExceeded), got %v", err)
	}
}

// TestRunHookExec_TimeoutKillsBackgroundedGrandchild is the failing-first
// regression test for the hang described in the plan: a hook whose leader
// backgrounds a grandchild (`sleep 300 &`) and then itself keeps running past
// its timeout must still return once the timeout fires, because the whole
// process group is killed, not just the leader. Against the pre-fix
// runHookExec (no Setpgid, no WaitDelay, no Cancel override) cmd.Run blocks
// forever on the pipe the grandchild still holds open, so this test hangs
// until the 30s watchdog below kills it and reports the hang as a failure.
func TestRunHookExec_TimeoutKillsBackgroundedGrandchild(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("sh not available")
	}
	output, finished, err := runHelperSubprocess(t, "TestHelperHookExec_TimeoutKillsBackgroundedGrandchild", "timeout")
	if !finished {
		t.Fatalf("hook exec hung: helper subprocess did not return within %s (pre-fix cmd.Run blocks on a pipe a backgrounded grandchild holds open)\n%s", helperSubprocessTimeout, output)
	}
	if err != nil {
		t.Fatalf("helper subprocess failed:\n%s", output)
	}
}

// TestValidateHook_RejectsTimeoutOverOneHour is the failing-first regression
// test for the missing upper bound on a manifest hook's timeout: today only
// negative values are rejected (validateHook, hooks.go), so an operator typo
// of "20h" instead of "20m" pins the deploy lock for most of a day.
func TestValidateHook_RejectsTimeoutOverOneHour(t *testing.T) {
	h := Hook{On: HookPostDeploy, Command: []string{"true"}, Timeout: 2 * time.Hour}
	if err := validateHook(h); err == nil {
		t.Fatal("expected an error for a 2h hook timeout, got nil")
	}
}

func TestValidateHook_AcceptsTimeoutAtOneHour(t *testing.T) {
	h := Hook{On: HookPostDeploy, Command: []string{"true"}, Timeout: time.Hour}
	if err := validateHook(h); err != nil {
		t.Fatalf("expected 1h hook timeout to be accepted, got %v", err)
	}
}
