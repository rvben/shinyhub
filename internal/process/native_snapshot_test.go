package process

import (
	"context"
	"errors"
	"os/exec"
	"strconv"
	"syscall"
	"testing"
)

func TestNativeSetSnapshot(t *testing.T) {
	r := NewNativeRuntime()
	if r.snapshotEnabled {
		t.Fatal("snapshot must default disabled")
	}
	r.SetSnapshot(true, 0.7)
	if !r.snapshotEnabled || r.reclaimMinFraction != 0.7 {
		t.Fatalf("SetSnapshot: enabled=%v frac=%v", r.snapshotEnabled, r.reclaimMinFraction)
	}
}

// TestNativeSuspend_DisabledNotSnapshotter: with warm-wake off, Suspend reports
// ErrRuntimeNotSnapshotter so the watcher hibernates via Stop (docker parity).
func TestNativeSuspend_DisabledNotSnapshotter(t *testing.T) {
	r := NewNativeRuntime()
	freed, err := r.Suspend(context.Background(), RunHandle{PID: 1})
	if freed || !errors.Is(err, ErrRuntimeNotSnapshotter) {
		t.Fatalf("Suspend = (%v, %v), want (false, ErrRuntimeNotSnapshotter)", freed, err)
	}
}

// TestNativeSuspend_NotReadyNotSnapshotter: enabled but the delegated base never
// came up (ensureDelegatedBase failed, e.g. no Delegate=memory) -> not-snapshotter.
func TestNativeSuspend_NotReadyNotSnapshotter(t *testing.T) {
	r := NewNativeRuntime()
	r.snapshotEnabled = true // cgroupBaseReady stays false
	freed, err := r.Suspend(context.Background(), RunHandle{PID: 1})
	if freed || !errors.Is(err, ErrRuntimeNotSnapshotter) {
		t.Fatalf("Suspend = (%v, %v), want (false, ErrRuntimeNotSnapshotter)", freed, err)
	}
}

// TestNativeSuspend_UntrackedNotFreed: base is ready but this replica has no
// per-app cgroup -> (false, nil) so just this app cold-stops, without flagging
// the whole runtime as non-snapshotting. Returns before any signal is sent.
func TestNativeSuspend_UntrackedNotFreed(t *testing.T) {
	r := NewNativeRuntime()
	r.snapshotEnabled = true
	r.cgroupBaseReady = true // no appCgroups entry for this PID
	freed, err := r.Suspend(context.Background(), RunHandle{PID: 999999})
	if freed || err != nil {
		t.Fatalf("Suspend = (%v, %v), want (false, nil) for untracked replica", freed, err)
	}
}

// TestNativeResume_LiveEndpoint: Resume on a running process group is a SIGCONT
// no-op and returns an empty-URL endpoint (Manager preserves the route) carrying
// the same PID/handle.
func TestNativeResume_LiveEndpoint(t *testing.T) {
	cmd := exec.Command("sleep", "30")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		t.Fatalf("start sleeper: %v", err)
	}
	pid := cmd.Process.Pid
	t.Cleanup(func() { _ = cmd.Process.Kill(); _, _ = cmd.Process.Wait() })

	r := NewNativeRuntime()
	ep, err := r.Resume(context.Background(), RunHandle{PID: pid})
	if err != nil {
		t.Fatalf("Resume: %v", err)
	}
	if ep.URL != "" {
		t.Errorf("URL = %q, want empty (route preserved by Manager)", ep.URL)
	}
	if ep.Provider != "native" {
		t.Errorf("Provider = %q, want native", ep.Provider)
	}
	if ep.WorkerID != strconv.Itoa(pid) {
		t.Errorf("WorkerID = %q, want %d", ep.WorkerID, pid)
	}
	if ep.Handle.PID != pid {
		t.Errorf("Handle.PID = %d, want %d", ep.Handle.PID, pid)
	}
}

// TestNativeResume_GoneIsError: a vanished process group yields an error so the
// caller cold-boots instead of routing to a dead replica.
func TestNativeResume_GoneIsError(t *testing.T) {
	cmd := exec.Command("sleep", "30")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		t.Fatalf("start sleeper: %v", err)
	}
	pid := cmd.Process.Pid
	_ = cmd.Process.Kill()
	_, _ = cmd.Process.Wait() // reap: the process group is now gone

	r := NewNativeRuntime()
	if _, err := r.Resume(context.Background(), RunHandle{PID: pid}); err == nil {
		t.Fatal("Resume of a gone process group: want error, got nil")
	}
}
