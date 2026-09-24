package main

import (
	"context"
	"errors"
	"runtime"
	"testing"
	"time"
)

// TestAwaitShutdown_ReturnsTrueWhenStopFinishesInBudget proves the happy path:
// a stop function that returns promptly is reported as finished, with no
// timeout side effects.
func TestAwaitShutdown_ReturnsTrueWhenStopFinishesInBudget(t *testing.T) {
	if !awaitShutdown(500*time.Millisecond, func() {}, readinessTestLogger()) {
		t.Fatal("expected true when stop returns well within budget")
	}
}

// TestAwaitShutdown_ReturnsFalseWhenStopBlocksPastBudget proves the core
// watchdog behavior: a stop function that never returns (modeling
// stopOwnership hanging on loops.Wait()/InterruptAndDrain/the fence retry
// loop) must not hang the caller - awaitShutdown gives up at the budget and
// reports false promptly, not after some multiple of the budget.
func TestAwaitShutdown_ReturnsFalseWhenStopBlocksPastBudget(t *testing.T) {
	block := make(chan struct{}) // never closed: stop never returns
	start := time.Now()
	if awaitShutdown(20*time.Millisecond, func() { <-block }, readinessTestLogger()) {
		t.Fatal("expected false when stop exceeds the budget")
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("awaitShutdown took %v to report a timeout of 20ms; it must return promptly at the budget, not wait for stop", elapsed)
	}
}

// TestAwaitOwnershipStop_ExitsNonZeroWhenStopHangs proves the bounded exit
// path end to end: when stopOwnership hangs past budget, awaitOwnershipStop
// calls osExit(1) rather than returning and letting the caller proceed into
// cleanup (in production, runServe's deferred store.Close()) while the leaked
// goroutine may still be using the store.
func TestAwaitOwnershipStop_ExitsNonZeroWhenStopHangs(t *testing.T) {
	origExit := osExit
	defer func() { osExit = origExit }()
	var exitCode int
	exited := make(chan struct{})
	osExit = func(code int) {
		exitCode = code
		close(exited)
		runtime.Goexit() // stand-in for os.Exit never returning to its caller
	}

	block := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		awaitOwnershipStop(20*time.Millisecond, func() { <-block }, readinessTestLogger())
	}()

	select {
	case <-exited:
	case <-time.After(2 * time.Second):
		t.Fatal("awaitOwnershipStop did not call osExit after stopOwnership hung past budget")
	}
	if exitCode != 1 {
		t.Fatalf("exit code = %d, want 1", exitCode)
	}
}

// TestAwaitOwnershipStop_DoesNotExitWhenStopFinishesInBudget proves the
// watchdog is silent on the ordinary graceful-shutdown path.
func TestAwaitOwnershipStop_DoesNotExitWhenStopFinishesInBudget(t *testing.T) {
	origExit := osExit
	defer func() { osExit = origExit }()
	called := false
	osExit = func(int) { called = true }

	awaitOwnershipStop(time.Second, func() {}, readinessTestLogger())
	if called {
		t.Fatal("awaitOwnershipStop must not exit when stopOwnership finishes within budget")
	}
}

// TestAcquireFenceUntilDeadline_SucceedsAfterRetries proves the fence-acquire
// retry loop keeps its original retry-until-success behavior when the fence
// becomes available before the deadline.
func TestAcquireFenceUntilDeadline_SucceedsAfterRetries(t *testing.T) {
	calls := 0
	acquire := func() (func(), error) {
		calls++
		if calls < 3 {
			return nil, errors.New("locked")
		}
		return func() {}, nil
	}
	release, ok := acquireFenceUntilDeadline(context.Background(), acquire, time.Millisecond, readinessTestLogger())
	if !ok || release == nil {
		t.Fatalf("expected ok=true and a non-nil release func, got ok=%v release=%v", ok, release != nil)
	}
	if calls != 3 {
		t.Fatalf("acquire calls = %d, want 3 (2 failures then success)", calls)
	}
}

// TestAcquireFenceUntilDeadline_GivesUpAtDeadline is the regression test for
// the actual bug: the original loop had no deadline at all, so a fence that
// never becomes available (e.g. a database outage during shutdown) hung
// forever. It must now give up promptly once drainCtx is done.
func TestAcquireFenceUntilDeadline_GivesUpAtDeadline(t *testing.T) {
	acquire := func() (func(), error) { return nil, errors.New("never available") }
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	start := time.Now()
	release, ok := acquireFenceUntilDeadline(ctx, acquire, time.Millisecond, readinessTestLogger())
	if ok || release != nil {
		t.Fatalf("expected ok=false and a nil release once the deadline passes, got ok=%v release=%v", ok, release != nil)
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("acquireFenceUntilDeadline took %v to give up on a 20ms deadline", elapsed)
	}
}

func TestDrainOwnershipHandoff(t *testing.T) {
	fenceOK := func() (func(), error) { return func() {}, nil }
	fenceBusy := func() (func(), error) { return nil, errors.New("fence busy") }
	drainOK := func(context.Context) error { return nil }
	// A job that never observes cancellation: InterruptAndDrain returns the
	// context error at the deadline while the job goroutine keeps running.
	drainStuck := func(ctx context.Context) error {
		<-ctx.Done()
		return ctx.Err()
	}
	for _, tc := range []struct {
		name  string
		drain func(context.Context) error
		fence func() (func(), error)
		want  bool
	}{
		{"drained and fenced", drainOK, fenceOK, true},
		{"drain deadline exceeded", drainStuck, fenceOK, false},
		{"fence never acquired", drainOK, fenceBusy, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
			defer cancel()
			if got := drainOwnershipHandoff(ctx, tc.drain, tc.fence, 5*time.Millisecond, readinessTestLogger()); got != tc.want {
				t.Fatalf("drainOwnershipHandoff = %v, want %v", got, tc.want)
			}
		})
	}
}
