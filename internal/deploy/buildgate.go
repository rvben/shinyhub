package deploy

import (
	"context"
	"sync/atomic"
	"time"
)

// buildGate bounds how many dependency builds (uv sync, renv::restore) run on
// this host at once. A build is the most expensive thing a deploy does, and
// without the gate a fleet apply of N apps starts N resolver processes that
// contend for every core and page the running apps out. Builds past the limit
// wait in FIFO order; the wait is reported, not silent.
type buildGate struct {
	// slots is nil when the gate has no limit; otherwise a buffered channel
	// whose capacity is the limit and whose length is the number of builds
	// running. Go wakes blocked senders in FIFO order, so waiting builds start
	// in the order they arrived.
	slots   chan struct{}
	waiters atomic.Int32
}

// newBuildGate returns a gate admitting limit concurrent builds. A limit of 0
// or less removes the cap.
func newBuildGate(limit int) *buildGate {
	g := &buildGate{}
	if limit > 0 {
		g.slots = make(chan struct{}, limit)
	}
	return g
}

// limit reports the number of builds the gate admits at once, 0 for no cap.
func (g *buildGate) limit() int { return cap(g.slots) }

// running reports how many builds currently hold a slot.
func (g *buildGate) running() int { return len(g.slots) }

// waiting reports how many builds are queued for a slot.
func (g *buildGate) waiting() int { return int(g.waiters.Load()) }

// acquire takes a build slot, blocking while the gate is full. While blocked
// it calls onWait once immediately and again every tick of every, each time
// with the gate's current occupancy and how long this caller has waited, so
// the caller can narrate the queue. It returns the context's error if the
// context ends first. The returned release must be called exactly once.
func (g *buildGate) acquire(ctx context.Context, every time.Duration, onWait func(running, waiting int, waited time.Duration)) (release func(), err error) {
	if g.slots == nil {
		return func() {}, nil
	}
	select {
	case g.slots <- struct{}{}:
		return g.release, nil
	default:
	}

	start := time.Now()
	g.waiters.Add(1)
	defer g.waiters.Add(-1)
	if onWait != nil {
		onWait(g.running(), g.waiting(), 0)
	}
	ticker := time.NewTicker(every)
	defer ticker.Stop()
	for {
		select {
		case g.slots <- struct{}{}:
			return g.release, nil
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-ticker.C:
			if onWait != nil {
				onWait(g.running(), g.waiting(), time.Since(start))
			}
		}
	}
}

func (g *buildGate) release() { <-g.slots }

// activeBuildGate is the gate every deploy on this process builds through.
// It starts uncapped and is sized once from configuration at startup.
var activeBuildGate atomic.Pointer[buildGate]

func init() { activeBuildGate.Store(newBuildGate(0)) }

// SetMaxConcurrentBuilds sizes the host-wide build gate: at most limit
// dependency builds run at once, 0 removes the cap. Builds already running or
// queued at the previous gate finish under it; only new builds see the new
// limit.
func SetMaxConcurrentBuilds(limit int) { activeBuildGate.Store(newBuildGate(limit)) }
