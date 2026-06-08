package main

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync/atomic"
	"testing"
	"time"
)

type readinessFakeRefresher struct {
	failuresLeft int          // only the refresh goroutine touches this
	calls        atomic.Int64 // total Refresh calls; readable from the test goroutine
}

func (f *readinessFakeRefresher) Refresh() error {
	f.calls.Add(1)
	if f.failuresLeft > 0 {
		f.failuresLeft--
		return errors.New("refresh boom")
	}
	return nil
}

func readinessTestLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func TestRefreshUntilReady_SucceedsAfterRetries(t *testing.T) {
	r := &readinessFakeRefresher{failuresLeft: 2}
	if !refreshUntilReady(context.Background(), r, time.Millisecond, readinessTestLogger()) {
		t.Fatal("expected ready=true after the refresh eventually succeeds")
	}
	if got := r.calls.Load(); got != 3 {
		t.Fatalf("Refresh calls = %d, want 3 (2 failures then success)", got)
	}
}

func TestRefreshUntilReady_FailsClosedOnOwnershipLoss(t *testing.T) {
	r := &readinessFakeRefresher{failuresLeft: 1 << 30} // never succeeds
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan bool, 1)
	go func() { done <- refreshUntilReady(ctx, r, time.Millisecond, readinessTestLogger()) }()

	// Synchronize on an observed SECOND Refresh call: that proves the first call
	// returned an error AND the loop backed off and retried, so the function is
	// definitively still running (not an early give-up) - and it has not returned,
	// because it only returns on success (impossible here) or ctx cancel (not yet).
	// A broken immediate-return implementation never makes a second call, so it
	// trips the done-check below or the deadline.
	deadline := time.Now().Add(2 * time.Second)
	for r.calls.Load() < 2 {
		select {
		case ready := <-done:
			t.Fatalf("refreshUntilReady returned (ready=%v) before retrying / before ownership loss", ready)
		default:
		}
		if time.Now().After(deadline) {
			t.Fatal("refreshUntilReady did not retry a failing refresh within the timeout")
		}
		time.Sleep(time.Millisecond)
	}

	// Still running and ctx live: it must not have returned.
	select {
	case <-done:
		t.Fatal("refreshUntilReady returned before ownership was lost")
	default:
	}

	// Cancelling ownership is what makes it return, and it returns false.
	cancel()
	select {
	case ready := <-done:
		if ready {
			t.Fatal("expected ready=false after ownership loss")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("refreshUntilReady did not return after ctx cancel")
	}
}

func TestOwnerAndReadyPredicate(t *testing.T) {
	var ready atomic.Bool
	owner := true
	pred := ownerAndReadyPredicate(func() bool { return owner }, &ready)

	// owner but not ready -> false (the window the gate must reject).
	if pred() {
		t.Fatal("owner=true ready=false must gate closed (503)")
	}
	ready.Store(true)
	if !pred() {
		t.Fatal("owner=true ready=true must gate open")
	}
	// not owner -> false even when ready.
	owner = false
	if pred() {
		t.Fatal("owner=false ready=true must gate closed")
	}
}
