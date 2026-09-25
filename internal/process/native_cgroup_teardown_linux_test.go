//go:build linux

package process

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// waitForPIDFile polls for a file written by a spawned shell containing a
// PID, returning the parsed value. Used instead of a fixed sleep so the test
// is not a timing gamble on a loaded CI host.
func waitForPIDFile(t *testing.T, path string, timeout time.Duration) int {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		b, err := os.ReadFile(path)
		if err == nil {
			if s := strings.TrimSpace(string(b)); s != "" {
				pid, perr := strconv.Atoi(s)
				if perr != nil {
					t.Fatalf("parse pid from %s (%q): %v", path, s, perr)
				}
				return pid
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for pid file %s", path)
	return 0
}

// TestTeardownAppCgroupFor_ReapsDetachedChild reproduces RT-9: a replica whose
// child calls setsid() (starting a new session and process group) survives
// the SIGKILL that Stop sends to the replica's OWN process group, because
// that signal targets a pgid the detached child no longer belongs to. cgroup
// membership, unlike process-group membership, is inherited from the forking
// parent regardless of session, so the child is still charged to the app
// cgroup and must be reaped before rmdir can succeed. Fails on the pre-fix
// teardownAppCgroupFor (which never calls killAppCgroupProcs, unlike the job
// teardown path) because the surviving child leaves the cgroup non-empty, so
// rmdir EBUSY-loops for a second and then gives up, leaking both the process
// and the directory (which is RT-8's stale-limit vector for the next
// replica). Passes once teardownAppCgroupFor reaps the cgroup's members
// first, exactly as placeJobInCgroup's teardown closure already does.
func TestTeardownAppCgroupFor_ReapsDetachedChild(t *testing.T) {
	requireWritableCgroupV2(t)
	base := buildDelegatedTestBase(t, "+memory", "")

	r := &NativeRuntime{
		appCgroups:      make(map[int]string),
		oomBaseline:     make(map[int]uint64),
		oomVerdict:      make(map[int]bool),
		cgroupBaseReady: true,
		cgroupBase:      base,
	}

	tmp := t.TempDir()
	triggerFile := filepath.Join(tmp, "go")
	pidFile := filepath.Join(tmp, "detached-child.pid")
	// Leader: busy-waits for triggerFile (so the test can move it into the app
	// cgroup BEFORE it forks anything - cgroup migration only moves the PID
	// written to cgroup.procs, not processes a lineage forks later, so forking
	// too early would leave the grandchild in the root cgroup and this test
	// would not reproduce RT-9 at all), then forks a grandchild that setsid()s
	// (escaping the leader's process group) and parks on sleep, then execs
	// into sleep itself (keeping its PID, now inside the app cgroup) so it
	// stays alive until this test SIGKILLs its process group below.
	script := `while [ ! -e "$1" ]; do sleep 0.05; done
setsid sh -c 'echo $$ > '"$2"'; exec sleep 300' >/dev/null 2>&1 &
exec sleep 300`
	cmd := exec.Command("sh", "-c", script, "_", triggerFile, pidFile)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		t.Fatalf("start leader: %v", err)
	}
	leaderPid := cmd.Process.Pid

	r.placeInAppCgroup(StartParams{Slug: "rt9app", Index: 0, MemoryLimitMB: 32}, leaderPid)
	dir, ok := r.appCgroups[leaderPid]
	if !ok {
		t.Fatalf("placeInAppCgroup did not register leader pid %d - setup must have failed", leaderPid)
	}

	if err := os.WriteFile(triggerFile, nil, 0o644); err != nil {
		t.Fatalf("write trigger file: %v", err)
	}

	childPid := waitForPIDFile(t, pidFile, 5*time.Second)
	t.Cleanup(func() {
		_ = syscall.Kill(childPid, syscall.SIGKILL)
		_ = syscall.Kill(-leaderPid, syscall.SIGKILL)
		_, _ = cmd.Process.Wait()
		killAppCgroupProcs(dir)
		_ = teardownAppCgroup(dir)
	})

	// The detached child forked from a process already inside the app cgroup,
	// so it must have inherited membership even though it left the process
	// group.
	inCgroup, err := cgroupContainsPID(dir, childPid)
	if err != nil {
		t.Fatalf("cgroupContainsPID: %v", err)
	}
	if !inCgroup {
		t.Fatalf("detached child %d never joined app cgroup %s (test setup invalid)", childPid, dir)
	}

	// Kill only the leader's process group, exactly as NativeRuntime.Signal
	// does on Stop.
	if err := syscall.Kill(-leaderPid, syscall.SIGKILL); err != nil {
		t.Fatalf("SIGKILL leader pgid %d: %v", leaderPid, err)
	}
	_, _ = cmd.Process.Wait()

	if err := syscall.Kill(childPid, 0); err != nil {
		t.Fatalf("detached child %d did not survive the leader's group SIGKILL (test setup invalid): %v", childPid, err)
	}

	// This is the code path under test.
	r.teardownAppCgroupFor(leaderPid)

	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("app cgroup dir %s still present after teardown (detached child pinned it): %v", dir, err)
	}
	if err := syscall.Kill(childPid, 0); err != syscall.ESRCH {
		t.Fatalf("detached child %d still alive after teardown (err=%v), want ESRCH", childPid, err)
	}
}
