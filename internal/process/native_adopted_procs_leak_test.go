package process

import (
	"context"
	"os/exec"
	"testing"
	"time"
)

// TestNativeWaitCleansProcsCacheForAdoptedExit is the RT-10 reproduction: the
// adopted-process branch of Wait (elastic-agent recovery, where this runtime
// instance never started the PID itself) never deleted the PID from r.procs,
// the gopsutil handle cache Stats() primes for CPU-delta computation. Once
// Stats has been called for an adopted handle at least once (exactly what the
// metrics poller does on every tick for every running replica, adopted or
// not), the cached *gops.Process for that PID survives the process's exit
// forever. Because gopsutil's Process.Percent/MemoryInfo re-read /proc by PID
// number with no creation-time identity check, a later PID reuse (any new
// process, native replica or otherwise, landing on the same OS PID) would
// silently inherit the dead entry's stale CPU baseline and be Stat()'d as if
// it were the old, already-exited process.
//
// The spawned-process branch already deletes from r.procs on exit (see the
// `delete(r.procs, handle.PID)` right after cmd.Wait()); this test asserts the
// adopted branch does the same, so both exit paths clean up identically.
func TestNativeWaitCleansProcsCacheForAdoptedExit(t *testing.T) {
	cmd := exec.Command("sleep", "30")
	if err := cmd.Start(); err != nil {
		t.Fatalf("start sleep: %v", err)
	}
	pid := cmd.Process.Pid
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	})

	rt := NewNativeRuntime()
	handle := RunHandle{PID: pid}

	// Prime r.procs exactly the way the metrics poller does: one Stats() call
	// against the adopted handle before it exits.
	if _, _, err := rt.Stats(context.Background(), handle); err != nil {
		t.Fatalf("Stats on live adopted PID: %v", err)
	}
	rt.mu.Lock()
	_, primed := rt.procs[pid]
	rt.mu.Unlock()
	if !primed {
		t.Fatal("test setup broken: Stats did not cache a gops.Process for the adopted PID")
	}

	waitDone := make(chan error, 1)
	go func() { waitDone <- rt.Wait(context.Background(), handle) }()

	if err := cmd.Process.Kill(); err != nil {
		t.Fatalf("kill: %v", err)
	}
	if _, err := cmd.Process.Wait(); err != nil {
		t.Fatalf("reap: %v", err)
	}

	select {
	case err := <-waitDone:
		if err != nil {
			t.Fatalf("Wait returned error after adopted process exit: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Wait did not return after adopted process exited")
	}

	rt.mu.Lock()
	_, stillCached := rt.procs[pid]
	rt.mu.Unlock()
	if stillCached {
		t.Fatalf("r.procs still holds pid %d after its adopted process exited; a future PID reuse would silently reuse this stale gopsutil handle and CPU baseline for an unrelated process", pid)
	}
}
