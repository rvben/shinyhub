//go:build linux

package process

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

// requireWritableCgroupV2 skips the test unless it can actually create and
// delegate a cgroup v2 subtree: root (cgroup.procs/subtree_control writes need
// CAP_SYS_ADMIN-equivalent privilege on most hosts) and a real cgroup v2
// mount. Unlike TestNativeAppCgroup_Integration and
// TestEnsureDelegatedBase_Integration, the tests using this helper need no
// python3/swap and mutate nothing outside a throwaway subtree, so they run
// automatically wherever they can rather than requiring an opt-in env var -
// they simply no-op (skip) in an unprivileged CI job.
func requireWritableCgroupV2(t *testing.T) {
	t.Helper()
	if os.Geteuid() != 0 {
		t.Skip("needs root to create/move cgroups")
	}
	if _, err := os.Stat(filepath.Join(cgroupV2Mount, "cgroup.controllers")); err != nil {
		t.Skipf("cgroup v2 not mounted at %s: %v", cgroupV2Mount, err)
	}
	probe, err := os.MkdirTemp(cgroupV2Mount, "shinyhub-cgroup-probe-")
	if err != nil {
		t.Skipf("cgroup v2 not writable: %v", err)
	}
	_ = os.Remove(probe)
}

// buildDelegatedTestBase creates a throwaway child of the cgroup v2 root
// (removed on test cleanup) and enables requiredControllers (e.g. "+memory")
// in its subtree_control, mirroring what ensureDelegatedBase does for the
// real service cgroup but without moving this test process anywhere. App
// cgroups created under it therefore expose the matching controller
// interface files (memory.max, ...). optionalControllers is attempted on a
// best-effort basis and its failure is ignored: not every host delegates
// every controller to the top of the hierarchy (this sandbox's root only
// enables "memory", not "cpu"), so callers that want to also assert on an
// optional controller must check for its interface file's presence first.
func buildDelegatedTestBase(t *testing.T, requiredControllers, optionalControllers string) string {
	t.Helper()
	base, err := os.MkdirTemp(cgroupV2Mount, "shinyhub-cgroup-test-")
	if err != nil {
		t.Fatalf("mkdir base: %v", err)
	}
	t.Cleanup(func() { _ = os.Remove(base) })
	if err := writeCgroupFile(filepath.Join(base, "cgroup.subtree_control"), requiredControllers); err != nil {
		t.Fatalf("enable %q on %s: %v (root cgroup may not delegate these controllers)", requiredControllers, base, err)
	}
	if optionalControllers != "" {
		_ = writeCgroupFile(filepath.Join(base, "cgroup.subtree_control"), optionalControllers)
	}
	return base
}

// readCgroupFileTrimmed reads a cgroup interface file and returns its
// trimmed content, failing the test on error.
func readCgroupFileTrimmed(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return strings.TrimSpace(string(b))
}

// fileExists reports whether path exists, treating any stat error as absence.
func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// TestPlaceInAppCgroup_ResetsLimitToMaxWhenLowered reproduces RT-8: when an
// app's memory/CPU limit is lowered to 0 ("no limit") and the next replica's
// setup reuses the SAME cgroup directory a still-warm-wake-enabled prior
// replica left behind (exactly what happens when teardown gave up after an
// EBUSY rmdir, RT-9's failure mode), the reused directory must have its
// memory.max/cpu.max reset to "max" rather than keeping the old replica's
// limit. Fails on the pre-fix code (placeInAppCgroup only wrote the limit
// file when the new value was > 0) and passes once the write is
// unconditional.
func TestPlaceInAppCgroup_ResetsLimitToMaxWhenLowered(t *testing.T) {
	requireWritableCgroupV2(t)
	// cpu is requested on a best-effort basis: some hosts (including this
	// sandbox) only delegate memory to the top of the hierarchy. The memory
	// assertions below are unconditional; the cpu ones are gated on
	// cpuDelegated so the test still proves the RT-8 claim on such a host.
	base := buildDelegatedTestBase(t, "+memory", "+cpu")

	r := &NativeRuntime{
		appCgroups:      make(map[int]string),
		oomBaseline:     make(map[int]uint64),
		oomVerdict:      make(map[int]bool),
		cgroupBaseReady: true,
		cgroupBase:      base,
		// Warm-wake keeps placing every replica in its own cgroup regardless of
		// whether a memory/CPU limit is set, which is the realistic trigger for
		// RT-8: the cgroup exists and is reused independent of the limit value.
		snapshotEnabled: true,
	}

	dir := appCgroupDir(base, "rt8app", 0)

	// Replica 1: memory limit 64 MiB, CPU quota 50%.
	cmd1 := exec.Command("sleep", "300")
	cmd1.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd1.Start(); err != nil {
		t.Fatalf("start replica 1: %v", err)
	}
	pid1 := cmd1.Process.Pid

	r.placeInAppCgroup(StartParams{Slug: "rt8app", Index: 0, MemoryLimitMB: 64, CPUQuotaPercent: 50}, pid1)

	if got, want := readCgroupFileTrimmed(t, filepath.Join(dir, "memory.max")), cgroupMemoryMaxValue(64); got != want {
		t.Fatalf("memory.max after initial placement = %q, want %q", got, want)
	}
	cpuDelegated := fileExists(filepath.Join(dir, "cpu.max"))
	if !cpuDelegated {
		t.Logf("cpu controller not delegated in this environment; skipping cpu.max assertions")
	} else if got, want := readCgroupFileTrimmed(t, filepath.Join(dir, "cpu.max")), cgroupCPUMaxValue(50); got != want {
		t.Fatalf("cpu.max after initial placement = %q, want %q", got, want)
	}

	// Kill replica 1 WITHOUT going through the real teardown path: this is
	// deliberately the reproduction of "setup reuses the existing dir" - the
	// directory survives with replica 1's limits still on disk, exactly as it
	// would after a real EBUSY-timeout teardown failure (RT-9's mechanism).
	_ = cmd1.Process.Kill()
	_, _ = cmd1.Process.Wait()

	// Replica 2: the operator lowered both limits to 0 (no limit) before the
	// next deploy/restart.
	cmd2 := exec.Command("sleep", "300")
	cmd2.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd2.Start(); err != nil {
		t.Fatalf("start replica 2: %v", err)
	}
	pid2 := cmd2.Process.Pid
	t.Cleanup(func() {
		_ = cmd2.Process.Kill()
		_, _ = cmd2.Process.Wait()
		killAppCgroupProcs(dir)
		_ = teardownAppCgroup(dir)
	})

	r.placeInAppCgroup(StartParams{Slug: "rt8app", Index: 0, MemoryLimitMB: 0, CPUQuotaPercent: 0}, pid2)

	if _, ok := r.appCgroups[pid2]; !ok {
		t.Fatalf("placeInAppCgroup did not register replica 2 (pid %d) - setup must have failed", pid2)
	}

	if got := readCgroupFileTrimmed(t, filepath.Join(dir, "memory.max")); got != "max" {
		t.Fatalf("memory.max after lowering the app's limit to 0 = %q, want %q (replica 2 kept replica 1's stale limit from the reused cgroup)", got, "max")
	}
	if cpuDelegated {
		if got, want := readCgroupFileTrimmed(t, filepath.Join(dir, "cpu.max")), cgroupCPUMaxValue(0); got != want {
			t.Fatalf("cpu.max after lowering the app's limit to 0 = %q, want %q (replica 2 kept replica 1's stale limit from the reused cgroup)", got, want)
		}
	}
}
