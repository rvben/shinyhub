package admission

import (
	"testing"
	"time"
)

func TestAppLimiterRejectsCapacityBelowDivisor(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("NewAppLimiter must panic when lruCapacity < divisor")
		}
	}()
	NewAppLimiter(10, 10, 20, 3, 8) // capacity 8 < divisor 20
}

func TestAppLimiterOneFloodDoesNotStarveAnother(t *testing.T) {
	clk := &fakeClock{t: time.Unix(1000, 0)}
	// Shared rate 10/s burst 10, divisor 20 so each principal gets 0.5/s, burst 3.
	a := NewAppLimiter(10, 10, 20, 3, 4096)
	a.setClock(clk.now)

	// Attacker burns its own share: 3 burst admits, then blocked (no refill).
	got := 0
	for i := 0; i < 10; i++ {
		if a.TryAdmit("attacker") {
			got++
		}
	}
	if got != 3 {
		t.Fatalf("attacker admitted %d, want 3 (its own burst)", got)
	}
	// A different principal is unaffected: its own bucket is full, shared bucket
	// still has capacity because the attacker never reached it after its share.
	if !a.TryAdmit("victim") {
		t.Fatal("a different principal must still be admitted; attacker starved only itself")
	}
}

func TestAppLimiterOverShareDoesNotDebitShared(t *testing.T) {
	clk := &fakeClock{t: time.Unix(1000, 0)}
	a := NewAppLimiter(10, 10, 20, 3, 4096)
	a.setClock(clk.now)

	// Exhaust one principal's share (3 admits consume 3 shared tokens).
	for i := 0; i < 3; i++ {
		a.TryAdmit("p1")
	}
	sharedBefore := a.sharedTokens()
	// Further attempts by p1 are over-share: they must NOT touch the shared bucket.
	for i := 0; i < 5; i++ {
		if a.TryAdmit("p1") {
			t.Fatal("over-share admit should be refused")
		}
	}
	if a.sharedTokens() != sharedBefore {
		t.Fatalf("over-share refusals debited the shared bucket: %v -> %v", sharedBefore, a.sharedTokens())
	}
}

func TestAppLimiterEvictionDoesNotResetShare(t *testing.T) {
	clk := &fakeClock{t: time.Unix(1000, 0)}
	// Tiny capacity equal to divisor to force eviction quickly. divisor 2,
	// capacity 2, principal burst 1, shared rate high so shared never binds.
	a := NewAppLimiter(1000, 1000, 2, 1, 2)
	a.setClock(clk.now)

	// p1 spends its single-burst share.
	if !a.TryAdmit("p1") {
		t.Fatal("p1 first admit should succeed")
	}
	if a.TryAdmit("p1") {
		t.Fatal("p1 second admit should fail: share spent")
	}
	// Churn other principals to force LRU eviction. An evicted-then-returning p1
	// must NOT get a fresh burst; eviction prefers full (unspent) buckets, and
	// p1's bucket is empty so it is not the eviction victim while others are full.
	for _, name := range []string{"q1", "q2", "q3", "q4"} {
		a.TryAdmit(name)
	}
	if a.TryAdmit("p1") {
		t.Fatal("p1 must not regain a fresh share via eviction churn")
	}
}
