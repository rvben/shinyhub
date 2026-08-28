package leader

import (
	"context"
	"log/slog"
	"runtime/debug"
	"sync"
)

// OwnerScope runs a unit of owner-only work in a fresh context each time
// ownership is acquired and cancels it when ownership is lost. Wire Acquire to
// an Elector's OnAcquire and Lose to its OnLose. The work function is
// responsible for running its one-shot startup, launching its loops bound to
// the passed context, blocking until that context is cancelled, and tearing the
// loops down before it returns.
//
// The Elector fires OnAcquire/OnLose serially from a single goroutine, so
// Acquire and Lose never overlap; the mutex only guards against a concurrent
// Stop() at process shutdown. Work must return promptly after its context is
// cancelled; Lose and Stop block until it does.
type OwnerScope struct {
	work func(ctx context.Context, epoch int64)

	mu      sync.Mutex
	cancel  context.CancelFunc
	done    chan struct{}
	stopped bool
}

// NewOwnerScope constructs a scope around work.
func NewOwnerScope(work func(ctx context.Context, epoch int64)) *OwnerScope {
	return &OwnerScope{work: work}
}

// Acquire starts work in a new goroutine under a fresh cancelable context. Any
// prior span is stopped first, so at most one span runs at a time.
func (o *OwnerScope) Acquire(epoch int64) {
	o.Lose()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	o.mu.Lock()
	if o.stopped {
		o.mu.Unlock()
		cancel()
		return
	}
	o.cancel, o.done = cancel, done
	o.mu.Unlock()
	go func() {
		defer close(done)
		// Recover so a panic in owner-only work does not crash the whole process
		// (an unrecovered goroutine panic is fatal). The span ends; the elector
		// still holds the lease, so this instance stays owner and the next
		// acquisition re-runs work rather than the fleet losing its control plane.
		defer func() {
			if r := recover(); r != nil {
				slog.Error("leader: owner work panicked",
					"epoch", epoch, "panic", r, "stack", string(debug.Stack()))
			}
		}()
		o.work(ctx, epoch)
	}()
}

// Lose cancels the current span (if any) and blocks until work returns.
func (o *OwnerScope) Lose() {
	o.stop(false)
}

// Stop cancels and waits for the current span, then permanently prevents new
// spans from starting. Unlike Lose, Stop is terminal: this matters at process
// shutdown, where the Elector may already be finishing an in-flight database
// acquire while another goroutine begins teardown.
func (o *OwnerScope) Stop() {
	o.stop(true)
}

func (o *OwnerScope) stop(final bool) {
	o.mu.Lock()
	if final {
		o.stopped = true
	}
	cancel, done := o.cancel, o.done
	o.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	if done != nil {
		<-done
	}
	o.mu.Lock()
	// Retain the active span while waiting so concurrent Stop/Lose callers join
	// the same work instead of returning early. Do not erase a newer span if a
	// caller violates the documented serial Acquire/Lose callback contract.
	if o.done == done {
		o.cancel, o.done = nil, nil
	}
	o.mu.Unlock()
}
