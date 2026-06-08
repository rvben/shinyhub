package main

import (
	"context"
	"errors"
	"io"
	"log/slog"
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
	go func() { time.Sleep(5 * time.Millisecond); cancel() }()
	if refreshUntilReady(ctx, r, time.Millisecond, readinessTestLogger()) {
		t.Fatal("expected ready=false when ownership is lost before the index is fresh")
	}
}
