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
	failuresLeft int
	calls        int
	called       chan struct{} // optional: non-blocking signal on each Refresh call
}

func (f *readinessFakeRefresher) Refresh() error {
	f.calls++
	if f.called != nil {
		select {
		case f.called <- struct{}{}:
		default:
		}
	}
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
	if r.calls != 3 {
		t.Fatalf("Refresh calls = %d, want 3 (2 failures then success)", r.calls)
	}
}

func TestRefreshUntilReady_FailsClosedOnOwnershipLoss(t *testing.T) {
	called := make(chan struct{}, 1)
	r := &readinessFakeRefresher{failuresLeft: 1 << 30, called: called} // never succeeds
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan bool, 1)
	go func() { done <- refreshUntilReady(ctx, r, 5*time.Millisecond, readinessTestLogger()) }()

	// Synchronize on an actually-observed failing refresh (no arbitrary sleep).
	select {
	case <-called:
	case <-time.After(2 * time.Second):
		t.Fatal("refreshUntilReady never called Refresh")
	}

	// A refresh has failed and ctx is still live: the function must not have
	// returned. refreshUntilReady only returns true on success (impossible here)
	// or false on ctx cancel (not yet), so done must be empty - the gate stays
	// closed rather than opening on a stale index or giving up early.
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
