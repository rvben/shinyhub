//go:build linux

package process

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// TestNativeSuspend_InsufficientReclaimRestores drives the real Suspend code path
// (SIGSTOP -> measure -> reclaim -> threshold) against a faked app cgroup dir:
// reclaim "succeeds" (the request is written to a plain file) but memory.current
// does not drop, so Suspend must report not-freed AND leave the process running
// (SIGCONT'd by the restore path). Root-free: no real cgroup is touched, so it
// runs in normal CI, complementing the gated moxie integration test that proves
// the success path.
func TestNativeSuspend_InsufficientReclaimRestores(t *testing.T) {
	dir := t.TempDir()
	// 200MB "current"; a plain memory.reclaim the helper can open + write.
	if err := os.WriteFile(filepath.Join(dir, "memory.current"), []byte("209715200\n"), 0o600); err != nil {
		t.Fatalf("write memory.current: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "memory.reclaim"), nil, 0o600); err != nil {
		t.Fatalf("write memory.reclaim: %v", err)
	}

	cmd := exec.Command("sleep", "60")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		t.Fatalf("start sleeper: %v", err)
	}
	pid := cmd.Process.Pid
	t.Cleanup(func() { _ = cmd.Process.Kill(); _, _ = cmd.Process.Wait() })

	r := NewNativeRuntime()
	r.snapshotEnabled = true
	r.reclaimMinFraction = 0.8
	r.cgroupBaseReady = true
	r.appCgroups[pid] = dir

	freed, err := r.Suspend(context.Background(), RunHandle{PID: pid})
	if freed || err != nil {
		t.Fatalf("Suspend = (%v, %v), want (false, nil) for insufficient reclaim", freed, err)
	}
	// The restore path must have SIGCONT'd the process: it must not be stopped.
	if st := settledProcState(pid); st == 'T' {
		t.Fatalf("process left stopped after insufficient-reclaim suspend (state=%c)", st)
	}
}

// TestNativeTeardownAppCgroupFor removes the cgroup dir and forgets the mapping.
func TestNativeTeardownAppCgroupFor(t *testing.T) {
	dir := t.TempDir()
	appDir := filepath.Join(dir, "app-x-0")
	if err := os.Mkdir(appDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	r := NewNativeRuntime()
	r.appCgroups[4242] = appDir

	r.teardownAppCgroupFor(4242)

	if _, ok := r.appCgroups[4242]; ok {
		t.Error("mapping not cleared after teardown")
	}
	if _, err := os.Stat(appDir); !os.IsNotExist(err) {
		t.Errorf("app cgroup dir still present after teardown: %v", err)
	}
	// Untracked pid is a no-op (no panic).
	r.teardownAppCgroupFor(7777)
}

// settledProcState returns the /proc/<pid>/stat state char once it is not 'T'
// (stopped), polling briefly to absorb the SIGCONT->runnable transition. Returns
// 'T' if it never leaves the stopped state.
func settledProcState(pid int) byte {
	for i := 0; i < 50; i++ {
		b, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
		if err == nil {
			s := string(b)
			// comm (field 2) is parenthesized and may contain spaces/parens, so
			// the state char is the first token after the last ')'.
			if idx := strings.LastIndexByte(s, ')'); idx >= 0 && idx+2 < len(s) {
				if state := s[idx+2]; state != 'T' {
					return state
				}
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	return 'T'
}
