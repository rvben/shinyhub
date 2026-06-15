//go:build linux

package process

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// TestCgroupHasField checks the controller-list parser used by
// ensureDelegatedBase to detect a delegated/enabled memory controller. Pure file
// parsing: runs on any linux without root.
func TestCgroupHasField(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "cgroup.controllers")
	if err := os.WriteFile(path, []byte("cpuset cpu io memory pids\n"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	if !cgroupHasField(path, "memory") {
		t.Error("expected memory present")
	}
	if cgroupHasField(path, "hugetlb") {
		t.Error("hugetlb must be absent")
	}
	if cgroupHasField(filepath.Join(dir, "missing"), "memory") {
		t.Error("missing file must report absent, not panic")
	}
}

// TestReadCgroupProcs checks the cgroup.procs PID parser used by drainCgroupProcs.
// Pure file parsing: runs on any linux without root.
func TestReadCgroupProcs(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "cgroup.procs")
	if err := os.WriteFile(path, []byte("101\n202\n303\n"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	pids, err := readCgroupProcs(path)
	if err != nil {
		t.Fatalf("readCgroupProcs: %v", err)
	}
	want := []int{101, 202, 303}
	if len(pids) != len(want) {
		t.Fatalf("pids = %v, want %v", pids, want)
	}
	for i := range want {
		if pids[i] != want[i] {
			t.Fatalf("pids = %v, want %v", pids, want)
		}
	}

	// Empty file: an empty cgroup yields no pids and no error.
	empty := filepath.Join(dir, "empty.procs")
	if err := os.WriteFile(empty, []byte(""), 0o600); err != nil {
		t.Fatalf("write empty: %v", err)
	}
	if pids, err := readCgroupProcs(empty); err != nil || len(pids) != 0 {
		t.Fatalf("empty: pids=%v err=%v", pids, err)
	}
}

// TestNativeAppCgroup_Integration exercises the per-app cgroup helpers
// (setupAppCgroup, appCgroupCurrentMemory, reclaimAppCgroup, teardownAppCgroup)
// end to end against a hand-built delegated subtree with a 200MB anon hog,
// asserting reclaim actually frees the RAM (under freeze, mirroring Suspend) and
// teardown removes the cgroup. It deliberately does NOT call ensureDelegatedBase
// (which would move the test runner's own cgroup); the self-move is verified in a
// systemd-service context (N5).
//
// Gated: needs root, cgroup v2, swap, python3, and WWC_CGROUP_IT=1, so it never
// runs in normal CI. This is the moxie-verified counterpart of the macOS unit
// tests, which cannot exercise memory.reclaim.
func TestNativeAppCgroup_Integration(t *testing.T) {
	if os.Getenv("WWC_CGROUP_IT") == "" {
		t.Skip("set WWC_CGROUP_IT=1 to run (needs root, cgroup v2, swap, python3)")
	}
	if os.Geteuid() != 0 {
		t.Skip("needs root to write a test cgroup")
	}

	// Build a delegated base by hand: a child of the v2 root with +memory in
	// subtree_control so its app children expose memory.current / memory.reclaim.
	base := filepath.Join(cgroupV2Mount, "wwc-native-gotest")
	if err := os.Mkdir(base, 0o755); err != nil && !os.IsExist(err) {
		t.Fatalf("mkdir base: %v", err)
	}
	t.Cleanup(func() { _ = os.Remove(base) })
	if err := writeCgroupFile(filepath.Join(base, "cgroup.subtree_control"), "+memory"); err != nil {
		t.Fatalf("enable +memory on base: %v (root cgroup may not delegate memory)", err)
	}

	// A python3 hog that parks on stdin, then allocates + touches 200MB. Parking
	// first lets us move it into the app cgroup BEFORE it allocates, so the RAM is
	// charged to the app cgroup (not wherever the test runner lives).
	const hogPy = `
import sys, time
sys.stdin.readline()
b = bytearray(200*1024*1024)
for i in range(0, len(b), 4096):
    b[i] = 1
sys.stdout.write('ready\n'); sys.stdout.flush()
time.sleep(60)
`
	cmd := exec.Command("python3", "-u", "-c", hogPy)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true} // isolate the signal group
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatalf("stdin pipe: %v", err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatalf("start hog: %v", err)
	}
	pid := cmd.Process.Pid
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
	})

	dir, err := setupAppCgroup(base, "app-test-0", pid)
	if err != nil {
		t.Fatalf("setupAppCgroup: %v", err)
	}
	// Release the hog now that it is charged to the app cgroup.
	if _, err := stdin.Write([]byte("go\n")); err != nil {
		t.Fatalf("release hog: %v", err)
	}
	time.Sleep(4 * time.Second) // let it allocate + touch

	pre, err := appCgroupCurrentMemory(dir)
	if err != nil {
		t.Fatalf("appCgroupCurrentMemory (pre): %v", err)
	}
	if pre < 150*1024*1024 {
		t.Fatalf("pre memory = %d MB, expected ~200MB (hog under-allocated or mischarged)", pre/1024/1024)
	}

	// Freeze before reclaim, exactly as NativeRuntime.Suspend does.
	if err := syscall.Kill(-pid, syscall.SIGSTOP); err != nil {
		t.Fatalf("SIGSTOP: %v", err)
	}
	if err := reclaimAppCgroup(dir, pre); err != nil {
		_ = syscall.Kill(-pid, syscall.SIGCONT)
		t.Fatalf("reclaimAppCgroup: %v", err)
	}
	time.Sleep(1 * time.Second)
	post, err := appCgroupCurrentMemory(dir)
	_ = syscall.Kill(-pid, syscall.SIGCONT)
	if err != nil {
		t.Fatalf("appCgroupCurrentMemory (post): %v", err)
	}
	t.Logf("native reclaim: pre=%dMB post=%dMB", pre/1024/1024, post/1024/1024)
	if !reclaimFreed(pre, post, 0.8) {
		t.Fatalf("reclaim freed < 80%%: pre=%d post=%d", pre, post)
	}

	// Teardown must remove the cgroup once the process is gone.
	_ = cmd.Process.Kill()
	_, _ = cmd.Process.Wait()
	if err := teardownAppCgroup(dir); err != nil {
		t.Fatalf("teardownAppCgroup: %v", err)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("app cgroup dir still present after teardown: %v", err)
	}
}

// TestEnsureDelegatedBase_Integration verifies the delicate self-move that only
// a real systemd-service context exercises: the process moves itself out of its
// service cgroup so that cgroup can delegate the memory controller to per-app
// children. Run it as a transient service with Delegate=memory, e.g.:
//
//	sudo systemd-run -p Delegate=memory -p Environment=WWC_DELEGATE_IT=1 \
//	    --wait --pipe ./process.test \
//	    -test.run TestEnsureDelegatedBase_Integration -test.v
//
// It asserts ensureDelegatedBase moves this process into base/_supervisor,
// enables +memory on the base, is idempotent, and that a per-app child cgroup
// under the base exposes memory.current. Gated: needs WWC_DELEGATE_IT=1 and a
// memory-delegated cgroup (systemd Delegate=memory or equivalent).
func TestEnsureDelegatedBase_Integration(t *testing.T) {
	if os.Getenv("WWC_DELEGATE_IT") == "" {
		t.Skip("set WWC_DELEGATE_IT=1 and run under systemd Delegate=memory")
	}
	base, err := ensureDelegatedBase()
	if err != nil {
		t.Fatalf("ensureDelegatedBase: %v", err)
	}
	// This process must now live in base/_supervisor.
	rel, err := cgroupV2RelPath(os.Getpid())
	if err != nil {
		t.Fatalf("cgroupV2RelPath(self): %v", err)
	}
	if !strings.HasSuffix(rel, "/_supervisor") {
		t.Fatalf("self cgroup = %q, want it moved into .../_supervisor", rel)
	}
	// The base must delegate the memory controller to its children.
	if !cgroupHasField(filepath.Join(base, "cgroup.subtree_control"), "memory") {
		t.Fatalf("base %s does not have +memory in subtree_control", base)
	}
	// Idempotent: a second call returns the same base without error.
	base2, err := ensureDelegatedBase()
	if err != nil || base2 != base {
		t.Fatalf("ensureDelegatedBase not idempotent: base2=%q err=%v", base2, err)
	}
	// A per-app child under the prepared base exposes memory.current.
	dir, err := setupAppCgroup(base, "app-selfmove-it-0", os.Getpid())
	if err != nil {
		t.Fatalf("setupAppCgroup: %v", err)
	}
	if _, err := appCgroupCurrentMemory(dir); err != nil {
		t.Fatalf("appCgroupCurrentMemory: %v", err)
	}
	// setupAppCgroup moved this process into the app cgroup; move it back to
	// _supervisor so the now-empty app cgroup can be torn down.
	if err := writeCgroupProc(filepath.Join(base, "_supervisor"), os.Getpid()); err != nil {
		t.Fatalf("move self back to _supervisor: %v", err)
	}
	if err := teardownAppCgroup(dir); err != nil {
		t.Fatalf("teardownAppCgroup: %v", err)
	}
}
