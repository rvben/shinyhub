package admission

import (
	"sync"
	"testing"
	"time"
)

type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func (f *fakeClock) now() time.Time {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.t
}

func (f *fakeClock) add(d time.Duration) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.t = f.t.Add(d)
}

func TestRate(t *testing.T) {
	// (cores * headroom) / render_seconds. 2 cores, 0.75 headroom, 1.3s render.
	got := Rate(2, 0.75, 1.3)
	want := (2.0 * 0.75) / 1.3
	if got != want {
		t.Fatalf("Rate = %v, want %v", got, want)
	}
	// render_seconds <= 0 means pacing disabled: rate 0.
	if r := Rate(2, 0.75, 0); r != 0 {
		t.Fatalf("Rate with 0 render_seconds = %v, want 0", r)
	}
}

func TestBurst(t *testing.T) {
	if b := Burst(2); b != 2 {
		t.Fatalf("Burst(2) = %v, want 2", b)
	}
	// Never below 1, even for a fractional sub-core quota.
	if b := Burst(0.5); b != 1 {
		t.Fatalf("Burst(0.5) = %v, want 1", b)
	}
}

func TestPacerBurstThenPace(t *testing.T) {
	clk := &fakeClock{t: time.Unix(1000, 0)}
	// rate 1/sec, burst 2.
	p := NewPacer(1, 2)
	p.nowFn = clk.now

	// Burst: two immediate takes succeed, the third fails (no time elapsed).
	if !p.TryTake() {
		t.Fatal("first take should succeed (burst)")
	}
	if !p.TryTake() {
		t.Fatal("second take should succeed (burst)")
	}
	if p.TryTake() {
		t.Fatal("third take should fail: bucket empty, no refill yet")
	}

	// After 1 second, exactly one token refilled.
	clk.add(1 * time.Second)
	if !p.TryTake() {
		t.Fatal("take after 1s refill should succeed")
	}
	if p.TryTake() {
		t.Fatal("second take right after should fail: only one token refilled")
	}
}

func TestPacerRefillCapsAtBurst(t *testing.T) {
	clk := &fakeClock{t: time.Unix(1000, 0)}
	p := NewPacer(1, 2)
	p.nowFn = clk.now
	// Drain the burst.
	p.TryTake()
	p.TryTake()
	// Idle far longer than burst/rate. Refill must cap at burst, not accumulate.
	clk.add(1 * time.Hour)
	if !p.TryTake() {
		t.Fatal("take 1 after long idle should succeed")
	}
	if !p.TryTake() {
		t.Fatal("take 2 after long idle should succeed (burst=2)")
	}
	if p.TryTake() {
		t.Fatal("take 3 must fail: refill capped at burst 2, not accumulated")
	}
}

func TestPacerZeroRateNeverRefills(t *testing.T) {
	clk := &fakeClock{t: time.Unix(1000, 0)}
	// rate 0 (pacing disabled sizing), burst 1: one initial token, then never refills.
	p := NewPacer(0, 1)
	p.nowFn = clk.now
	if !p.TryTake() {
		t.Fatal("initial token should be available")
	}
	clk.add(1 * time.Hour)
	if p.TryTake() {
		t.Fatal("rate 0 must never refill")
	}
}

func TestPacerConcurrentNeverExceedsBurst(t *testing.T) {
	clk := &fakeClock{t: time.Unix(1000, 0)}
	p := NewPacer(0, 100) // 100 tokens, no refill during the test
	p.nowFn = clk.now

	var granted int64
	var mu sync.Mutex
	var wg sync.WaitGroup
	for i := 0; i < 500; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if p.TryTake() {
				mu.Lock()
				granted++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if granted != 100 {
		t.Fatalf("concurrent takes granted %d, want exactly 100 (burst)", granted)
	}
}
