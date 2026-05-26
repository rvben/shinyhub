package proxy

import (
	"testing"
	"time"
)

// fakeClock lets tests drive the counter's minute epochs deterministically.
type fakeClock struct{ t time.Time }

func (f *fakeClock) now() time.Time      { return f.t }
func (f *fakeClock) add(d time.Duration) { f.t = f.t.Add(d) }

func newTestCounter() (*rejectCounter, *fakeClock) {
	clk := &fakeClock{t: time.Date(2026, 5, 26, 12, 0, 0, 0, time.UTC)}
	c := newRejectCounter()
	c.nowFn = clk.now
	return c, clk
}

func TestRejectCounter_RecordAndWindow(t *testing.T) {
	c, _ := newTestCounter()
	c.record("demo", ReasonPoolSaturated)
	c.record("demo", ReasonPoolSaturated)
	c.record("demo", ReasonAppNotReady)

	got := c.window("demo", 10*time.Minute)
	if got[ReasonPoolSaturated] != 2 {
		t.Errorf("pool-saturated = %d, want 2", got[ReasonPoolSaturated])
	}
	if got[ReasonAppNotReady] != 1 {
		t.Errorf("app-not-ready = %d, want 1", got[ReasonAppNotReady])
	}
}

func TestRejectCounter_WindowEdgeBoundary(t *testing.T) {
	c, clk := newTestCounter()
	c.record("demo", ReasonPoolSaturated) // at minute E

	// 9 minutes later the original event is still inside a 10-minute window.
	clk.add(9 * time.Minute)
	if got := c.window("demo", 10*time.Minute); got[ReasonPoolSaturated] != 1 {
		t.Errorf("at +9m: count = %d, want 1", got[ReasonPoolSaturated])
	}

	// 10 minutes later it has fallen out of the 10-minute window.
	clk.add(1 * time.Minute) // now +10m
	if got := c.window("demo", 10*time.Minute); got[ReasonPoolSaturated] != 0 {
		t.Errorf("at +10m: count = %d, want 0", got[ReasonPoolSaturated])
	}
}

func TestRejectCounter_StaleSweepEvictsKey(t *testing.T) {
	c, clk := newTestCounter()
	c.record("gone", ReasonPoolSaturated)

	// Advance past the whole ring so every bucket for "gone" is stale, then
	// touch a different key to trigger the once-per-minute sweep.
	clk.add(rejectRingBuckets * time.Minute)
	c.record("other", ReasonAppNotReady)

	c.mu.Lock()
	_, present := c.keys["gone"]
	c.mu.Unlock()
	if present {
		t.Error("expected stale key 'gone' to be swept")
	}
}

func TestRejectCounter_Forget(t *testing.T) {
	c, _ := newTestCounter()
	c.record("demo", ReasonPoolSaturated)
	c.forget("demo")
	if got := c.window("demo", 10*time.Minute); got != nil {
		t.Errorf("after forget, window = %v, want nil", got)
	}
}

func TestRejectCounter_SentinelStaysSingleKey(t *testing.T) {
	c, _ := newTestCounter()
	// Many distinct junk slugs all collapse to the sentinel at the call site,
	// so the counter only ever sees the one key.
	for i := 0; i < 1000; i++ {
		c.record(rejectSentinel, ReasonUnknownSlug)
	}
	c.mu.Lock()
	n := len(c.keys)
	c.mu.Unlock()
	if n != 1 {
		t.Errorf("key count = %d, want 1 (sentinel only)", n)
	}
}
