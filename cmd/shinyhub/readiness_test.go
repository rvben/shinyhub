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
}

func (f *readinessFakeRefresher) Refresh() error {
	f.calls++
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
	r := &readinessFakeRefresher{failuresLeft: 1 << 30} // never succeeds
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan bool, 1)
	go func() { done <- refreshUntilReady(ctx, r, 5*time.Millisecond, readinessTestLogger()) }()

	// While refresh keeps failing and ctx is live, it must NOT return - the gate
	// stays closed rather than opening on a stale index or giving up.
	select {
	case <-done:
		t.Fatal("refreshUntilReady returned before ownership was lost")
	case <-time.After(40 * time.Millisecond):
	}

	// Only once ownership is lost (ctx cancelled) does it return false. This proves
	// the cancellation is what caused the return, not an early give-up.
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
