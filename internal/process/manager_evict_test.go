package process_test

import (
	"context"
	"io"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/rvben/shinyhub/internal/process"
)

// evictRuntime is a Runtime whose Wait blocks per-handle on a channel the test
// controls, so a test can hold a replica's exit-monitor goroutine open and then
// release it on demand. It records whether Signal was ever called (eviction
// must be signal-free) and captures the log writer handed to each Start so the
// test can write to a replacement's log and detect a stale Wait closing it.
type evictRuntime struct {
	mu       sync.Mutex
	nextPID  int
	waitCh   map[int]chan struct{}
	writers  map[int]io.Writer
	signaled bool
}

func newEvictRuntime() *evictRuntime {
	return &evictRuntime{nextPID: 7000, waitCh: map[int]chan struct{}{}, writers: map[int]io.Writer{}}
}

func (r *evictRuntime) Start(_ context.Context, p process.StartParams, w io.Writer) (process.ReplicaEndpoint, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	pid := r.nextPID
	r.nextPID++
	r.waitCh[pid] = make(chan struct{})
	r.writers[pid] = w
	return process.ReplicaEndpoint{
		URL:      "http://127.0.0.1:0",
		Provider: "native",
		Handle:   process.RunHandle{PID: pid},
	}, nil
}

func (r *evictRuntime) Signal(h process.RunHandle, _ syscall.Signal) error {
	r.mu.Lock()
	r.signaled = true
	ch := r.waitCh[h.PID]
	r.mu.Unlock()
	if ch != nil {
		select {
		case <-ch:
		default:
			close(ch)
		}
	}
	return nil
}

func (r *evictRuntime) Wait(_ context.Context, h process.RunHandle) error {
	r.mu.Lock()
	ch := r.waitCh[h.PID]
	r.mu.Unlock()
	if ch != nil {
		<-ch
	}
	return nil
}

// release lets a blocked Wait for pid return without recording a Signal,
// simulating a worker connection that drops on its own (a stale exit).
func (r *evictRuntime) release(pid int) {
	r.mu.Lock()
	ch := r.waitCh[pid]
	r.mu.Unlock()
	if ch != nil {
		select {
		case <-ch:
		default:
			close(ch)
		}
	}
}

func (r *evictRuntime) writerFor(pid int) io.Writer {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.writers[pid]
}

func (r *evictRuntime) didSignal() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.signaled
}

func (r *evictRuntime) Stats(_ context.Context, _ process.RunHandle) (float64, uint64, error) {
	return 0, 0, nil
}
func (r *evictRuntime) RunOnce(_ context.Context, _ process.StartParams, _ io.Writer) (process.ExitInfo, error) {
	return process.ExitInfo{}, nil
}
func (r *evictRuntime) HostPreparesDeps() bool    { return false }
func (r *evictRuntime) AppBindHost() string       { return "127.0.0.1" }
func (r *evictRuntime) HostProvidesAppData() bool { return false }

// TestEvictReplica_DropsEntryWithoutSignaling verifies that EvictReplica removes
// a replica from the manager's view without signaling the runtime (the worker is
// presumed dead, so dialing it would hang) and that the freed slot can be
// started again immediately without an "already running" rejection.
func TestEvictReplica_DropsEntryWithoutSignaling(t *testing.T) {
	rt := newEvictRuntime()
	m := process.NewManager(t.TempDir(), rt)

	first, err := m.Start(process.StartParams{Slug: "app", Index: 0, Command: []string{"x"}})
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() { rt.release(first.PID) })

	m.EvictReplicaIfWorker("app", 0, "")

	if got := m.All(); len(got) != 0 {
		t.Fatalf("expected no entries after eviction, got %d: %+v", len(got), got)
	}
	if rt.didSignal() {
		t.Error("EvictReplica must not signal the runtime (worker is dead)")
	}

	// The slot is free: a re-placement Start must succeed, not be rejected as
	// already running.
	second, err := m.Start(process.StartParams{Slug: "app", Index: 0, Command: []string{"x"}})
	if err != nil {
		t.Fatalf("re-start after eviction: %v", err)
	}
	t.Cleanup(func() { rt.release(second.PID) })
}

// TestEvictReplicaIfWorker_OnlyEvictsMatchingWorker verifies eviction is gated on
// the entry still being owned by the lost worker. A worker-loss pass that races a
// redeploy (which already re-placed the slot onto a healthy worker and started a
// new manager entry, but not yet persisted its row) must not drop the live
// replacement; only the entry actually owned by the lost worker is evicted.
func TestEvictReplicaIfWorker_OnlyEvictsMatchingWorker(t *testing.T) {
	m := process.NewManager(t.TempDir(), newEvictRuntime())

	// A replacement replica owned by a healthy worker now occupies slug+index.
	m.ForceEntry("app", process.ProcessInfo{Slug: "app", Index: 0, Status: process.StatusRunning, WorkerID: "w-new"})

	// A stale worker-loss pass for the OLD worker must not evict the replacement.
	m.EvictReplicaIfWorker("app", 0, "w-old")
	if _, ok := m.GetReplica("app", 0); !ok {
		t.Fatal("replacement owned by w-new must not be evicted by a loss pass for w-old")
	}

	// The loss pass for the entry's actual owner evicts it.
	m.EvictReplicaIfWorker("app", 0, "w-new")
	if _, ok := m.GetReplica("app", 0); ok {
		t.Fatal("entry owned by w-new must be evicted by a loss pass for w-new")
	}
}

// TestEvictReplica_StaleWaitDoesNotCloseReplacementLog verifies the log-cleanup
// guard: after a replica is evicted and a replacement is started at the same
// slug+index, releasing the original (now stale) exit-monitor goroutine must not
// touch the replacement's state or close its log file. Run under -race to catch
// the concurrent close.
func TestEvictReplica_StaleWaitDoesNotCloseReplacementLog(t *testing.T) {
	rt := newEvictRuntime()
	m := process.NewManager(t.TempDir(), rt)

	first, err := m.Start(process.StartParams{Slug: "app", Index: 0, Command: []string{"x"}})
	if err != nil {
		t.Fatalf("start first: %v", err)
	}

	// Evict the first replica: its exit-monitor goroutine is still blocked in
	// rt.Wait (the worker never reported an exit).
	m.EvictReplicaIfWorker("app", 0, "")

	second, err := m.Start(process.StartParams{Slug: "app", Index: 0, Command: []string{"x"}})
	if err != nil {
		t.Fatalf("start replacement: %v", err)
	}
	t.Cleanup(func() { rt.release(second.PID) })
	lfSecond := rt.writerFor(second.PID)
	if lfSecond == nil {
		t.Fatal("no log writer captured for replacement")
	}

	// Release the stale Wait for the evicted replica. With the guard fix it must
	// see a handle mismatch and touch neither the replacement's status nor its
	// log file.
	rt.release(first.PID)
	time.Sleep(30 * time.Millisecond) // let the (lock-protected) stale Wait body run

	// The replacement is untouched: still running.
	info, ok := m.GetReplica("app", 0)
	if !ok || info.Status != process.StatusRunning {
		t.Fatalf("replacement status changed by stale Wait: ok=%v info=%+v", ok, info)
	}
	// The replacement's log file is still open and writable.
	if _, err := lfSecond.Write([]byte("alive\n")); err != nil {
		t.Fatalf("replacement log closed by stale Wait: %v", err)
	}
}
