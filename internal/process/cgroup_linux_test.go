//go:build linux

package process

import (
	"os"
	"os/exec"
	"testing"
	"time"
)

// TestCgroupReclaim_Integration drives the real cgroupCurrentMemory /
// reclaimPIDMemory / reclaimFreed helpers against an isolated cgroup v2 subtree
// with a 200MB anon-memory hog, asserting the reclaim actually frees the RAM.
// Gated: needs root, cgroup v2, swap, python3, and WWC_CGROUP_IT=1 (so it never
// runs in normal CI). This is the moxie-verified counterpart of the macOS unit
// tests, which cannot exercise memory.reclaim.
func TestCgroupReclaim_Integration(t *testing.T) {
	if os.Getenv("WWC_CGROUP_IT") == "" {
		t.Skip("set WWC_CGROUP_IT=1 to run (needs root, cgroup v2, swap, python3)")
	}
	if os.Geteuid() != 0 {
		t.Skip("needs root to write a test cgroup")
	}

	const cgDir = "/sys/fs/cgroup/wwc-gotest"
	if err := os.Mkdir(cgDir, 0o755); err != nil && !os.IsExist(err) {
		t.Fatalf("mkdir %s: %v", cgDir, err)
	}

	// Child moves itself into the cgroup, then allocates + touches 200MB anon.
	hog := `echo $$ > ` + cgDir + `/cgroup.procs; exec python3 -c "import time; b=bytearray(200*1024*1024); [b.__setitem__(i,1) for i in range(0,len(b),4096)]; print('ready',flush=True); time.sleep(60)"`
	cmd := exec.Command("sh", "-c", hog)
	if err := cmd.Start(); err != nil {
		t.Fatalf("start hog: %v", err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
		for i := 0; i < 30; i++ {
			if err := os.Remove(cgDir); err == nil {
				return
			}
			time.Sleep(100 * time.Millisecond)
		}
	})
	pid := cmd.Process.Pid
	time.Sleep(4 * time.Second) // let it allocate + touch

	pre, err := cgroupCurrentMemory(pid)
	if err != nil {
		t.Fatalf("cgroupCurrentMemory (pre): %v", err)
	}
	if pre < 150*1024*1024 {
		t.Fatalf("pre memory = %d MB, expected ~200MB (hog under-allocated)", pre/1024/1024)
	}

	if err := reclaimPIDMemory(pid, pre); err != nil {
		t.Fatalf("reclaimPIDMemory: %v", err)
	}
	time.Sleep(1 * time.Second)

	post, err := cgroupCurrentMemory(pid)
	if err != nil {
		t.Fatalf("cgroupCurrentMemory (post): %v", err)
	}
	t.Logf("reclaim: pre=%dMB post=%dMB", pre/1024/1024, post/1024/1024)
	if !reclaimFreed(pre, post, 0.8) {
		t.Fatalf("reclaim freed < 80%%: pre=%d post=%d", pre, post)
	}
}
