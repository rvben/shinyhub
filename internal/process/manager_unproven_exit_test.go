package process_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strconv"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/rvben/shinyhub/internal/process"
)

// unprovenExitRuntime is a Runtime whose first Wait (the exit monitor's)
// fails with a transient error that proves nothing, so the monitor finishes
// without ever observing an exit. Later Waits report the truth: nil once the
// process is gone, otherwise they block until their context ends.
type unprovenExitRuntime struct {
	mu         sync.Mutex
	waitCalls  int
	gone       bool // the process has exited
	ignoreTerm bool // Signal is delivered but the process keeps running
	nextPID    int
}

func (r *unprovenExitRuntime) Start(_ context.Context, p process.StartParams, _ io.Writer) (process.ReplicaEndpoint, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.nextPID++
	return process.ReplicaEndpoint{
		URL:      fmt.Sprintf("http://127.0.0.1:%d", p.Port),
		Provider: "native",
		WorkerID: strconv.Itoa(r.nextPID),
		Handle:   process.RunHandle{PID: 70000 + r.nextPID},
	}, nil
}

func (r *unprovenExitRuntime) Signal(process.RunHandle, syscall.Signal) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.gone {
		return syscall.ESRCH
	}
	if !r.ignoreTerm {
		r.gone = true
	}
	return nil
}

func (r *unprovenExitRuntime) Wait(ctx context.Context, _ process.RunHandle) error {
	r.mu.Lock()
	r.waitCalls++
	first := r.waitCalls == 1
	r.mu.Unlock()
	if first {
		return errors.New("wait: connection reset by peer")
	}
	for {
		r.mu.Lock()
		gone := r.gone
		r.mu.Unlock()
		if gone {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(2 * time.Millisecond):
		}
	}
}

func (r *unprovenExitRuntime) Stats(context.Context, process.RunHandle) (*float64, uint64, error) {
	return nil, 0, nil
}
func (r *unprovenExitRuntime) RunOnce(context.Context, process.StartParams, io.Writer) (process.ExitInfo, error) {
	return process.ExitInfo{}, nil
}
func (r *unprovenExitRuntime) HostPreparesDeps() bool    { return false }
func (r *unprovenExitRuntime) AppBindHost() string       { return "127.0.0.1" }
func (r *unprovenExitRuntime) HostProvidesAppData() bool { return false }

// startWithUnprovenMonitor starts one replica and waits until its exit
// monitor has finished on the transient Wait error.
func startWithUnprovenMonitor(t *testing.T, rt *unprovenExitRuntime) (*process.Manager, process.StartParams) {
	t.Helper()
	mgr := process.NewManager(t.TempDir(), rt)
	mgr.SetStopGrace(50 * time.Millisecond)
	params := process.StartParams{Slug: "unproven", Index: 0, Command: []string{"x"}, Port: 1}
	if _, err := mgr.Start(params); err != nil {
		t.Fatalf("start: %v", err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for {
		info, ok := mgr.GetReplica("unproven", 0)
		if !ok {
			t.Fatal("replica entry vanished before the stop")
		}
		if info.Status != process.StatusRunning {
			return mgr, params
		}
		if time.Now().After(deadline) {
			t.Fatal("exit monitor never finished")
		}
		time.Sleep(2 * time.Millisecond)
	}
}

// TestStopReplicaConfirmed_ProvesExitAfterMonitorWaitFailed covers an exit
// monitor whose Wait failed transiently: it closed done without proof and
// will never run again, so a confirmed stop must ask the runtime itself
// rather than read the closed done as "unconfirmed" forever.
func TestStopReplicaConfirmed_ProvesExitAfterMonitorWaitFailed(t *testing.T) {
	rt := &unprovenExitRuntime{}
	mgr, params := startWithUnprovenMonitor(t, rt)

	if err := mgr.StopReplicaConfirmed("unproven", 0); err != nil {
		t.Fatalf("StopReplicaConfirmed after an unproven monitor exit: %v", err)
	}
	if _, _, ok := mgr.ReplicaIncarnation("unproven", 0); ok {
		t.Fatal("slot still occupied after a confirmed stop")
	}
	if _, err := mgr.Start(params); err != nil {
		t.Fatalf("Start after the confirmed stop: %v", err)
	}
}

// TestStopReplicaConfirmed_ProvesExitWhenSignalFindsProcessGone covers the
// same monitor failure where the process has meanwhile exited on its own:
// the signal fails because nothing is left to receive it, and only a fresh
// Wait can prove that.
func TestStopReplicaConfirmed_ProvesExitWhenSignalFindsProcessGone(t *testing.T) {
	rt := &unprovenExitRuntime{}
	mgr, params := startWithUnprovenMonitor(t, rt)
	rt.mu.Lock()
	rt.gone = true
	rt.mu.Unlock()

	if err := mgr.StopReplicaConfirmed("unproven", 0); err != nil {
		t.Fatalf("StopReplicaConfirmed for a process that already exited: %v", err)
	}
	if _, err := mgr.Start(params); err != nil {
		t.Fatalf("Start after the confirmed stop: %v", err)
	}
}

// TestStopReplicaConfirmed_UnprovenMonitorStillFencesLiveProcess is the
// negative control: a process that ignores both signals is never proven
// gone, so the stop stays unconfirmed and the slot stays fenced.
func TestStopReplicaConfirmed_UnprovenMonitorStillFencesLiveProcess(t *testing.T) {
	rt := &unprovenExitRuntime{ignoreTerm: true}
	mgr, params := startWithUnprovenMonitor(t, rt)

	if err := mgr.StopReplicaConfirmed("unproven", 0); !errors.Is(err, process.ErrStopUnconfirmed) {
		t.Fatalf("StopReplicaConfirmed = %v, want ErrStopUnconfirmed", err)
	}
	if _, err := mgr.Start(params); !errors.Is(err, process.ErrReplicaStopPending) {
		t.Fatalf("Start = %v, want ErrReplicaStopPending while the process may be alive", err)
	}

	// Once the process does exit, a retry proves it and frees the slot.
	rt.mu.Lock()
	rt.gone = true
	rt.mu.Unlock()
	gen, _, ok := mgr.ReplicaIncarnation("unproven", 0)
	if !ok {
		t.Fatal("fenced entry vanished")
	}
	if err := mgr.StopReplicaIncarnation("unproven", 0, gen); err != nil {
		t.Fatalf("StopReplicaIncarnation once the process exited: %v", err)
	}
	if _, err := mgr.Start(params); err != nil {
		t.Fatalf("Start after the retry confirmed: %v", err)
	}
}
