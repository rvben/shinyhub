package process_test

import (
	"errors"
	"sync"
	"testing"

	"github.com/rvben/shinyhub/internal/process"
)

// finishCapture records every Finish call the manager emits, so a test can
// assert how many times a run's terminal record was written and with which
// status. The run-history table keys on run_id with an unguarded UPDATE, so a
// second Finish for the same run silently rewrites the recorded verdict.
// While fail is set, every Finish is still recorded but reports a transient
// persistence error.
type finishCapture struct {
	mu       sync.Mutex
	fail     bool
	finishes []process.LogRun
}

func (c *finishCapture) recorder() process.LogRunRecorder {
	return process.LogRunRecorder{Finish: func(run process.LogRun) error {
		c.mu.Lock()
		defer c.mu.Unlock()
		c.finishes = append(c.finishes, run)
		if c.fail {
			return errors.New("database unavailable")
		}
		return nil
	}}
}

func (c *finishCapture) setFail(v bool) {
	c.mu.Lock()
	c.fail = v
	c.mu.Unlock()
}

func (c *finishCapture) snapshot() []process.LogRun {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]process.LogRun(nil), c.finishes...)
}

// TestEvictReplicaIfWorker_PreservesFinishedRun asserts eviction does not
// rewrite the terminal record of a run the exit monitor already finished. A
// replica can crash on a dying worker moments before the worker-down sweep
// evicts it; the crash verdict (status, exit cause, finish time) is the true
// history of that run, and the eviction must only clear the slot, not restamp
// the run as lost.
func TestEvictReplicaIfWorker_PreservesFinishedRun(t *testing.T) {
	rt := newWaitErrRuntime()
	m := process.NewManager(t.TempDir(), rt)
	cap := &finishCapture{}
	m.SetLogRunRecorder(cap.recorder())

	info, err := m.Start(process.StartParams{Slug: "app", Index: 0, Command: []string{"x"}})
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	rt.finish(info.PID, nil)
	awaitStatus(t, m, "app", 0, process.StatusCrashed)

	m.EvictReplicaIfWorker("app", 0, "node-a")

	finishes := cap.snapshot()
	if len(finishes) != 1 {
		t.Fatalf("run finished %d times, want 1; statuses: %v", len(finishes), runStatuses(finishes))
	}
	if finishes[0].Status != process.StatusCrashed {
		t.Fatalf("run finished as %q, want crashed", finishes[0].Status)
	}
}

// TestAdoptEvict_PreservesFinishedRun is the adopt-path variant: after a
// control-plane restart the adopt monitor - not the start monitor - observes
// the crash, and the subsequent eviction must equally leave the finished run's
// verdict alone.
func TestAdoptEvict_PreservesFinishedRun(t *testing.T) {
	rt := newWaitErrRuntime()
	m := process.NewManager(t.TempDir(), rt)
	cap := &finishCapture{}
	m.SetLogRunRecorder(cap.recorder())

	handle := process.RunHandle{PID: 4242, ContainerID: "node-a/c1"}
	rt.mu.Lock()
	rt.release[handle.PID] = make(chan error, 1)
	rt.mu.Unlock()

	m.Adopt("app", process.ProcessInfo{
		Slug: "app", Index: 0, Status: process.StatusRunning,
		Provider: "remote_docker", WorkerID: "node-a", LogRunID: "run-adopted",
	}, handle)
	rt.finish(handle.PID, nil)
	awaitStatus(t, m, "app", 0, process.StatusCrashed)

	m.EvictReplicaIfWorker("app", 0, "node-a")

	finishes := cap.snapshot()
	if len(finishes) != 1 {
		t.Fatalf("adopted run finished %d times, want 1; statuses: %v", len(finishes), runStatuses(finishes))
	}
	if finishes[0].Status != process.StatusCrashed {
		t.Fatalf("adopted run finished as %q, want crashed", finishes[0].Status)
	}
}

// TestEvictReplicaIfWorker_FinishesLiveRunAsLost is the negative control: a
// replica evicted while its run is still open (the worker vanished without any
// exit ever being observed) has no other writer for its terminal record, so
// eviction itself must finish the run as lost.
func TestEvictReplicaIfWorker_FinishesLiveRunAsLost(t *testing.T) {
	rt := newWaitErrRuntime()
	m := process.NewManager(t.TempDir(), rt)
	cap := &finishCapture{}
	m.SetLogRunRecorder(cap.recorder())

	info, err := m.Start(process.StartParams{Slug: "app", Index: 0, Command: []string{"x"}})
	if err != nil {
		t.Fatalf("start: %v", err)
	}

	m.EvictReplicaIfWorker("app", 0, "node-a")

	finishes := cap.snapshot()
	if len(finishes) != 1 {
		t.Fatalf("run finished %d times, want 1; statuses: %v", len(finishes), runStatuses(finishes))
	}
	if finishes[0].Status != process.StatusLost {
		t.Fatalf("run finished as %q, want lost", finishes[0].Status)
	}
	if finishes[0].FinishedAt.IsZero() {
		t.Fatal("lost finish carries no FinishedAt")
	}

	// Unblock the orphaned exit monitor; it sees the emptied slot and no-ops.
	rt.finish(info.PID, nil)
}

func runStatuses(runs []process.LogRun) []process.Status {
	out := make([]process.Status, len(runs))
	for i, r := range runs {
		out[i] = r.Status
	}
	return out
}

// TestEvictReplicaIfWorker_RetriesUnpersistedFinish: the exit monitor can
// record a crash verdict in memory and then fail to persist it (transient
// database error). Eviction is the last actor to see that entry, so it must
// retry the existing terminal payload - same run, same crashed verdict -
// rather than trusting the in-memory FinishedAt as proof of a durable record
// and dropping the only copy of the verdict.
func TestEvictReplicaIfWorker_RetriesUnpersistedFinish(t *testing.T) {
	rt := newWaitErrRuntime()
	m := process.NewManager(t.TempDir(), rt)
	cap := &finishCapture{}
	m.SetLogRunRecorder(cap.recorder())

	info, err := m.Start(process.StartParams{Slug: "app", Index: 0, Command: []string{"x"}})
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	cap.setFail(true)
	rt.finish(info.PID, errors.New("exit status 1"))
	awaitStatus(t, m, "app", 0, process.StatusCrashed)
	if got := len(cap.snapshot()); got != 1 {
		t.Fatalf("finish attempts before eviction = %d, want 1", got)
	}

	// The store recovers before the worker-loss sweep evicts the slot.
	cap.setFail(false)
	m.EvictReplicaIfWorker("app", 0, "node-a")

	finishes := cap.snapshot()
	if len(finishes) != 2 {
		t.Fatalf("finish attempts = %d, want 2 (retry of the failed persist); statuses: %v", len(finishes), runStatuses(finishes))
	}
	if finishes[1].Status != process.StatusCrashed {
		t.Fatalf("retried finish status = %q, want crashed (the original verdict, not lost)", finishes[1].Status)
	}
	if finishes[1].RunID != finishes[0].RunID {
		t.Fatalf("retried finish run = %q, want %q (the same run)", finishes[1].RunID, finishes[0].RunID)
	}
	if finishes[1].FinishedAt.IsZero() {
		t.Fatal("retried finish carries no FinishedAt")
	}
}

// TestAdoptEvict_RetriesUnpersistedFinish is the adopt-path variant: the adopt
// monitor's failed terminal persist must equally be retried at eviction.
func TestAdoptEvict_RetriesUnpersistedFinish(t *testing.T) {
	rt := newWaitErrRuntime()
	m := process.NewManager(t.TempDir(), rt)
	cap := &finishCapture{}
	m.SetLogRunRecorder(cap.recorder())

	handle := process.RunHandle{PID: 4243, ContainerID: "node-a/c2"}
	rt.mu.Lock()
	rt.release[handle.PID] = make(chan error, 1)
	rt.mu.Unlock()

	m.Adopt("app", process.ProcessInfo{
		Slug: "app", Index: 0, Status: process.StatusRunning,
		Provider: "remote_docker", WorkerID: "node-a", LogRunID: "run-adopted",
	}, handle)
	cap.setFail(true)
	rt.finish(handle.PID, errors.New("exit status 1"))
	awaitStatus(t, m, "app", 0, process.StatusCrashed)
	if got := len(cap.snapshot()); got != 1 {
		t.Fatalf("finish attempts before eviction = %d, want 1", got)
	}

	cap.setFail(false)
	m.EvictReplicaIfWorker("app", 0, "node-a")

	finishes := cap.snapshot()
	if len(finishes) != 2 {
		t.Fatalf("finish attempts = %d, want 2 (retry of the failed persist); statuses: %v", len(finishes), runStatuses(finishes))
	}
	if finishes[1].Status != process.StatusCrashed {
		t.Fatalf("retried finish status = %q, want crashed (the original verdict, not lost)", finishes[1].Status)
	}
	if finishes[1].RunID != "run-adopted" {
		t.Fatalf("retried finish run = %q, want run-adopted", finishes[1].RunID)
	}
}
