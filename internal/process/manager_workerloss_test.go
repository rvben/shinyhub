package process_test

import (
	"context"
	"fmt"
	"io"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/rvben/shinyhub/internal/process"
)

// waitErrRuntime is a Runtime whose Wait blocks until the test releases it with
// a chosen error, so a test can drive the exit monitor down a specific outcome:
// a real exit (nil), or a worker-loss verdict (ErrNoLiveWorker) that a remote
// runtime returns once the registry no longer lists the replica's worker as up.
type waitErrRuntime struct {
	mu      sync.Mutex
	nextPID int
	release map[int]chan error
}

func newWaitErrRuntime() *waitErrRuntime {
	return &waitErrRuntime{nextPID: 9000, release: map[int]chan error{}}
}

func (r *waitErrRuntime) Start(_ context.Context, _ process.StartParams, _ io.Writer) (process.ReplicaEndpoint, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	pid := r.nextPID
	r.nextPID++
	r.release[pid] = make(chan error, 1)
	return process.ReplicaEndpoint{
		URL:      "http://127.0.0.1:0",
		Provider: "remote_docker",
		WorkerID: "node-a",
		Handle:   process.RunHandle{PID: pid},
	}, nil
}

func (r *waitErrRuntime) Wait(_ context.Context, h process.RunHandle) error {
	r.mu.Lock()
	ch := r.release[h.PID]
	r.mu.Unlock()
	if ch == nil {
		return nil
	}
	return <-ch
}

// finish ends the blocked Wait for pid with err.
func (r *waitErrRuntime) finish(pid int, err error) {
	r.mu.Lock()
	ch := r.release[pid]
	r.mu.Unlock()
	if ch != nil {
		ch <- err
	}
}

func (r *waitErrRuntime) Signal(h process.RunHandle, _ syscall.Signal) error {
	r.finish(h.PID, nil)
	return nil
}

func (r *waitErrRuntime) Stats(_ context.Context, _ process.RunHandle) (*float64, uint64, error) {
	return nil, 0, nil
}
func (r *waitErrRuntime) RunOnce(_ context.Context, _ process.StartParams, _ io.Writer) (process.ExitInfo, error) {
	return process.ExitInfo{}, nil
}
func (r *waitErrRuntime) HostPreparesDeps() bool    { return false }
func (r *waitErrRuntime) AppBindHost() string       { return "0.0.0.0" }
func (r *waitErrRuntime) HostProvidesAppData() bool { return false }

// awaitStatus polls the manager until the replica reaches want, or fails.
func awaitStatus(t *testing.T, m *process.Manager, slug string, index int, want process.Status) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	var got process.Status
	for time.Now().Before(deadline) {
		if info, ok := m.GetReplica(slug, index); ok {
			got = info.Status
			if got == want {
				return
			}
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("replica status = %q, want %q", got, want)
}

// TestExitMonitor_WorkerLossIsNotACrash asserts the exit monitor distinguishes
// "the worker holding this replica is gone" from "this replica exited". The two
// are different facts: only the second has an exit cause, and only the second is
// the app's fault. Recording a crash for a lost worker fabricates an exit reason
// for a container that never exited, spends the app's restart budget re-placing
// onto a dead worker, and - because the persisted row flips to 'crashed' - hides
// the replica from the worker-down sweep that owns the real recovery path.
func TestExitMonitor_WorkerLossIsNotACrash(t *testing.T) {
	rt := newWaitErrRuntime()
	m := process.NewManager(t.TempDir(), rt)

	info, err := m.Start(process.StartParams{Slug: "app", Index: 0, Command: []string{"x"}})
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	rt.finish(info.PID, fmt.Errorf("handle node %q: %w", "node-a", process.ErrNoLiveWorker))

	awaitStatus(t, m, "app", 0, process.StatusLost)
	if verdict, ok := m.LastExit("app", 0); ok {
		t.Fatalf("worker loss recorded an exit verdict %+v; there was no exit to explain", verdict)
	}
}

// TestExitMonitor_RealExitIsStillACrash is the negative control for the test
// above: an exit the worker actually reported must still surface as a crash with
// a recorded verdict, so the worker-loss branch cannot pass by suppressing every
// exit.
func TestExitMonitor_RealExitIsStillACrash(t *testing.T) {
	rt := newWaitErrRuntime()
	m := process.NewManager(t.TempDir(), rt)

	info, err := m.Start(process.StartParams{Slug: "app", Index: 0, Command: []string{"x"}})
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	rt.finish(info.PID, nil)

	awaitStatus(t, m, "app", 0, process.StatusCrashed)
	if _, ok := m.LastExit("app", 0); !ok {
		t.Fatal("a reported exit must record an exit verdict")
	}
}

// TestAdoptExitMonitor_WorkerLossIsNotACrash asserts the same distinction on the
// adopt path. A control-plane restart re-adopts remote replicas from agent
// inventory, so after a restart it is the adopt monitor - not the start monitor -
// that observes a worker dying.
func TestAdoptExitMonitor_WorkerLossIsNotACrash(t *testing.T) {
	rt := newWaitErrRuntime()
	m := process.NewManager(t.TempDir(), rt)

	handle := process.RunHandle{PID: 4242, ContainerID: "node-a/c1"}
	rt.mu.Lock()
	rt.release[handle.PID] = make(chan error, 1)
	rt.mu.Unlock()

	m.Adopt("app", process.ProcessInfo{
		Slug: "app", Index: 0, Status: process.StatusRunning,
		Provider: "remote_docker", WorkerID: "node-a",
	}, handle)
	rt.finish(handle.PID, fmt.Errorf("handle node %q: %w", "node-a", process.ErrNoLiveWorker))

	awaitStatus(t, m, "app", 0, process.StatusLost)
	if verdict, ok := m.LastExit("app", 0); ok {
		t.Fatalf("worker loss recorded an exit verdict %+v; there was no exit to explain", verdict)
	}
}
