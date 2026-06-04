package leader

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
)

func TestOwnerScope_AcquireRunsWork_LoseCancels(t *testing.T) {
	started := make(chan struct{})
	var ctxErr error
	scope := NewOwnerScope(func(ctx context.Context, epoch int64) {
		close(started)
		<-ctx.Done()
		ctxErr = ctx.Err()
	})
	scope.Acquire(1)
	<-started
	scope.Lose() // joins the work goroutine, so ctxErr is visible below
	if ctxErr == nil {
		t.Fatal("work context must be cancelled on Lose")
	}
}

func TestOwnerScope_PropagatesEpoch(t *testing.T) {
	got := make(chan int64, 1)
	scope := NewOwnerScope(func(ctx context.Context, epoch int64) {
		got <- epoch
		<-ctx.Done()
	})
	scope.Acquire(42)
	defer scope.Stop()
	if e := <-got; e != 42 {
		t.Fatalf("epoch = %d, want 42", e)
	}
}

func TestOwnerScope_ReacquireRestartsWork(t *testing.T) {
	var runs int32
	scope := NewOwnerScope(func(ctx context.Context, epoch int64) {
		atomic.AddInt32(&runs, 1)
		<-ctx.Done()
	})
	scope.Acquire(1)
	scope.Lose()
	scope.Acquire(2)
	scope.Stop()
	if n := atomic.LoadInt32(&runs); n != 2 {
		t.Fatalf("work ran %d times, want 2", n)
	}
}

func TestOwnerScope_LoseWhenIdleIsNoop(t *testing.T) {
	scope := NewOwnerScope(func(ctx context.Context, epoch int64) { <-ctx.Done() })
	scope.Lose() // must neither panic nor block
}

func TestOwnerScope_ConcurrentStopIsSafe(t *testing.T) {
	running := make(chan struct{})
	scope := NewOwnerScope(func(ctx context.Context, epoch int64) {
		close(running)
		<-ctx.Done()
	})
	scope.Acquire(1)
	<-running
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); scope.Stop() }()
	}
	wg.Wait() // all concurrent Stop() calls must return without panic or deadlock
}
