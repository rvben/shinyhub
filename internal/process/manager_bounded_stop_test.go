package process_test

import (
	"context"
	"errors"
	"slices"
	"syscall"
	"testing"
	"time"

	"github.com/rvben/shinyhub/internal/process"
)

// startIgnoringSignals starts one replica whose process ignores every signal
// and returns its incarnation and PID.
func startIgnoringSignals(t *testing.T, rt *perPIDRuntime, grace time.Duration) (*process.Manager, uint64, int) {
	t.Helper()
	mgr := process.NewManager(t.TempDir(), rt)
	mgr.SetStopGrace(grace)
	rt.mu.Lock()
	rt.ignoreNext = true
	rt.mu.Unlock()
	info, err := mgr.Start(process.StartParams{Slug: "slow", Index: 0, Port: 20001, Command: []string{"app"}})
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	gen, _, ok := mgr.ReplicaIncarnation("slow", 0)
	if !ok {
		t.Fatal("no incarnation for the started replica")
	}
	return mgr, gen, info.PID
}

// TestStopReplicaIncarnationWithin_ReturnsWithinBudget pins the bound a retry
// queue relies on: a process that ignores signals must not hold the caller for
// the stop grace windows, only for the budget it was given.
func TestStopReplicaIncarnationWithin_ReturnsWithinBudget(t *testing.T) {
	rt := newPerPIDRuntime()
	mgr, gen, _ := startIgnoringSignals(t, rt, 5*time.Second)

	start := time.Now()
	err := mgr.StopReplicaIncarnationWithin("slow", 0, gen, 100*time.Millisecond)
	elapsed := time.Since(start)
	if !errors.Is(err, process.ErrStopUnconfirmed) {
		t.Fatalf("bounded stop of a live process = %v, want ErrStopUnconfirmed", err)
	}
	if elapsed > 2*time.Second {
		t.Fatalf("bounded stop took %v, want it bounded well below the 5s grace", elapsed)
	}
	if _, err := mgr.Start(process.StartParams{Slug: "slow", Index: 0, Port: 20002, Command: []string{"app"}}); !errors.Is(err, process.ErrReplicaStopPending) && !errors.Is(err, process.ErrReplicaAlreadyRunning) {
		t.Fatalf("Start after an unconfirmed bounded stop = %v, want the slot refused", err)
	}
}

// TestStopReplicaIncarnationWithin_BoundsAHungSignal covers a runtime whose
// signal call itself hangs, as a remote runtime can: the budget bounds the
// whole step, not only the wait for exit proof.
func TestStopReplicaIncarnationWithin_BoundsAHungSignal(t *testing.T) {
	rt := newPerPIDRuntime()
	mgr, gen, _ := startIgnoringSignals(t, rt, 5*time.Second)
	rt.mu.Lock()
	rt.signalDelay = 3 * time.Second
	rt.mu.Unlock()

	start := time.Now()
	err := mgr.StopReplicaIncarnationWithin("slow", 0, gen, 100*time.Millisecond)
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("bounded stop with a hung signal took %v", elapsed)
	}
	if err == nil {
		t.Fatal("bounded stop with a hung signal reported the process stopped")
	}
}

// TestStopReplicaIncarnationWithin_EscalatesAcrossCalls pins that bounded
// retries still reach SIGKILL: each call advances the stop by one step, TERM
// first, KILL only once the grace window since TERM has passed, and nothing
// is signalled again after KILL.
func TestStopReplicaIncarnationWithin_EscalatesAcrossCalls(t *testing.T) {
	rt := newPerPIDRuntime()
	grace := 300 * time.Millisecond
	mgr, gen, pid := startIgnoringSignals(t, rt, grace)
	step := func() error { return mgr.StopReplicaIncarnationWithin("slow", 0, gen, 20*time.Millisecond) }

	if err := step(); !errors.Is(err, process.ErrStopUnconfirmed) {
		t.Fatalf("first step = %v, want ErrStopUnconfirmed", err)
	}
	if got := rt.signalsTo(pid); !slices.Equal(got, []syscall.Signal{syscall.SIGTERM}) {
		t.Fatalf("after the first step signals = %v, want [SIGTERM]", got)
	}
	if err := step(); !errors.Is(err, process.ErrStopUnconfirmed) {
		t.Fatalf("second step = %v, want ErrStopUnconfirmed", err)
	}
	if got := rt.signalsTo(pid); !slices.Equal(got, []syscall.Signal{syscall.SIGTERM}) {
		t.Fatalf("a step inside the grace window signalled again: %v", got)
	}

	time.Sleep(grace)
	if err := step(); !errors.Is(err, process.ErrStopUnconfirmed) {
		t.Fatalf("step after grace = %v, want ErrStopUnconfirmed", err)
	}
	if got := rt.signalsTo(pid); !slices.Equal(got, []syscall.Signal{syscall.SIGTERM, syscall.SIGKILL}) {
		t.Fatalf("after the grace window signals = %v, want [SIGTERM SIGKILL]", got)
	}
	time.Sleep(grace)
	if err := step(); !errors.Is(err, process.ErrStopUnconfirmed) {
		t.Fatalf("step after KILL = %v, want ErrStopUnconfirmed", err)
	}
	if got := rt.signalsTo(pid); len(got) != 2 {
		t.Fatalf("a step after KILL signalled again: %v", got)
	}

	rt.exit(pid)
	deadline := time.Now().Add(2 * time.Second)
	for {
		err := step()
		if err == nil {
			break
		}
		if !errors.Is(err, process.ErrStopUnconfirmed) || time.Now().After(deadline) {
			t.Fatalf("step once the process exited = %v, want nil", err)
		}
	}
	if _, ok := mgr.GetReplica("slow", 0); ok {
		t.Fatal("slot still occupied after the bounded stop confirmed")
	}
}

// TestStopReplicaIncarnationWithin_ContinuesAFullAttempt pins that a retry
// picks up where an unbounded first attempt left off: that attempt already
// sent TERM and KILL, so the retry only checks for proof.
func TestStopReplicaIncarnationWithin_ContinuesAFullAttempt(t *testing.T) {
	rt := newPerPIDRuntime()
	mgr, gen, pid := startIgnoringSignals(t, rt, 30*time.Millisecond)
	if err := mgr.StopReplicaIncarnation("slow", 0, gen); !errors.Is(err, process.ErrStopUnconfirmed) {
		t.Fatalf("full attempt = %v, want ErrStopUnconfirmed", err)
	}
	if err := mgr.StopReplicaIncarnationWithin("slow", 0, gen, 20*time.Millisecond); !errors.Is(err, process.ErrStopUnconfirmed) {
		t.Fatalf("bounded retry = %v, want ErrStopUnconfirmed", err)
	}
	if got := rt.signalsTo(pid); !slices.Equal(got, []syscall.Signal{syscall.SIGTERM, syscall.SIGKILL}) {
		t.Fatalf("signals = %v, want only the full attempt's [SIGTERM SIGKILL]", got)
	}
}

// TestStopReplicaIncarnationWithin_RecordsALateSignal covers a signal call
// that outlives the step's budget and lands afterwards. The landed signal
// must count as delivered, and a retry while it is still in flight must not
// send a second one, so the stop escalates from the real delivery instead of
// re-sending TERM on every retry.
func TestStopReplicaIncarnationWithin_RecordsALateSignal(t *testing.T) {
	rt := newPerPIDRuntime()
	mgr, gen, pid := startIgnoringSignals(t, rt, 5*time.Second)
	rt.mu.Lock()
	rt.signalDelay = 400 * time.Millisecond
	rt.mu.Unlock()
	step := func() error { return mgr.StopReplicaIncarnationWithin("slow", 0, gen, 20*time.Millisecond) }

	if err := step(); !errors.Is(err, process.ErrStopUnconfirmed) {
		t.Fatalf("first step = %v, want ErrStopUnconfirmed", err)
	}
	if err := step(); !errors.Is(err, process.ErrStopUnconfirmed) {
		t.Fatalf("step while the signal is in flight = %v, want ErrStopUnconfirmed", err)
	}
	time.Sleep(800 * time.Millisecond)
	if got := rt.signalsTo(pid); !slices.Equal(got, []syscall.Signal{syscall.SIGTERM}) {
		t.Fatalf("signals after the in-flight retry = %v, want one SIGTERM", got)
	}
	if err := step(); !errors.Is(err, process.ErrStopUnconfirmed) {
		t.Fatalf("step after the late signal landed = %v, want ErrStopUnconfirmed", err)
	}
	time.Sleep(800 * time.Millisecond)
	if got := rt.signalsTo(pid); !slices.Equal(got, []syscall.Signal{syscall.SIGTERM}) {
		t.Fatalf("a step inside the grace window re-sent a late-landed signal: %v", got)
	}
}

// hungProofRuntime's first Wait panics, so the exit monitor finishes without
// proof, and every later Wait ignores its context and blocks, as a runtime
// stuck on a dead connection or a slow orphan sweep can.
type hungProofRuntime struct {
	*perPIDRuntime
	waits chan struct{}
}

func (r hungProofRuntime) Wait(ctx context.Context, h process.RunHandle) error {
	select {
	case r.waits <- struct{}{}:
		panic("first wait fails")
	default:
	}
	time.Sleep(3 * time.Second)
	return nil
}

// TestStopReplicaIncarnationWithin_BoundsTheDirectExitProbe pins that the
// direct Wait a bounded stop falls back to, once the exit monitor is gone, is
// held to the stop's budget like every other runtime call.
func TestStopReplicaIncarnationWithin_BoundsTheDirectExitProbe(t *testing.T) {
	rt := hungProofRuntime{perPIDRuntime: newPerPIDRuntime(), waits: make(chan struct{}, 1)}
	mgr := process.NewManager(t.TempDir(), rt)
	mgr.SetStopGrace(5 * time.Second)
	rt.mu.Lock()
	rt.ignoreNext = true
	rt.mu.Unlock()
	if _, err := mgr.Start(process.StartParams{Slug: "slow", Index: 0, Port: 20001, Command: []string{"app"}}); err != nil {
		t.Fatalf("start: %v", err)
	}
	gen, _, ok := mgr.ReplicaIncarnation("slow", 0)
	if !ok {
		t.Fatal("no incarnation for the started replica")
	}
	deadline := time.Now().Add(2 * time.Second)
	for len(rt.waits) == 0 {
		if time.Now().After(deadline) {
			t.Fatal("exit monitor never called Wait")
		}
		time.Sleep(5 * time.Millisecond)
	}

	start := time.Now()
	err := mgr.StopReplicaIncarnationWithin("slow", 0, gen, 100*time.Millisecond)
	if elapsed := time.Since(start); elapsed > 1500*time.Millisecond {
		t.Fatalf("bounded stop blocked %v on a hung direct Wait", elapsed)
	}
	if !errors.Is(err, process.ErrStopUnconfirmed) {
		t.Fatalf("bounded stop with a hung Wait = %v, want ErrStopUnconfirmed", err)
	}
}

// TestClaimStopPending_RestoresAFenceTheExitCleared covers an exit that lands
// between an unconfirmed stop and the queue's claim. The exit monitor clears
// the then-unclaimed fence, so a claim that reports success must put the
// fence back: the queue has not yet recorded the exit durably, and until it
// releases the slot no replacement may start there.
func TestClaimStopPending_RestoresAFenceTheExitCleared(t *testing.T) {
	rt := newPerPIDRuntime()
	mgr, gen, pid := startIgnoringSignals(t, rt, 20*time.Millisecond)
	if err := mgr.StopReplicaIncarnation("slow", 0, gen); !errors.Is(err, process.ErrStopUnconfirmed) {
		t.Fatalf("stop = %v, want ErrStopUnconfirmed", err)
	}
	rt.exit(pid)
	deadline := time.Now().Add(2 * time.Second)
	for {
		info, ok := mgr.GetReplica("slow", 0)
		if !ok {
			t.Fatal("entry left its slot before the claim; the interleaving under test needs it in place")
		}
		if info.Status != process.StatusRunning {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("exit monitor never observed the exit")
		}
		time.Sleep(5 * time.Millisecond)
	}

	if !mgr.ClaimStopPending("slow", 0, gen) {
		t.Fatal("claim of the exited incarnation failed")
	}
	if _, err := mgr.Start(process.StartParams{Slug: "slow", Index: 0, Port: 20002, Command: []string{"app"}}); !errors.Is(err, process.ErrReplicaStopPending) {
		t.Fatalf("Start on a claimed slot = %v, want ErrReplicaStopPending", err)
	}
	mgr.ReleaseStopPending("slow", 0, gen)
	if _, err := mgr.Start(process.StartParams{Slug: "slow", Index: 0, Port: 20003, Command: []string{"app"}}); err != nil {
		t.Fatalf("Start after the release = %v, want the slot free", err)
	}
}
