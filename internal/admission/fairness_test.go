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

func TestAppLimiterEvictionTakesFullestNotSpent(t *testing.T) {
	clk := &fakeClock{t: time.Unix(1000, 0)}
	// Shared rate high (never binds). divisor 2, principal burst 2, capacity 2.
	// principal rate = 4/2 = 2 per second, but no time advances so no refill:
	// token counts are set purely by how many times each principal admits.
	a := NewAppLimiter(4, 1000, 2, 2, 2)
	a.setClock(clk.now)

	// p1 fully spends its burst of 2: p1 bucket ends at 0 tokens. Two separate
	// admit calls, not one folded into a single boolean, since each has the side
	// effect of consuming a token.
	if !a.TryAdmit("p1") {
		t.Fatal("p1 first admit should succeed (burst 2)")
	}
	if !a.TryAdmit("p1") {
		t.Fatal("p1 second admit should succeed (burst 2)")
	}
	if a.TryAdmit("p1") {
		t.Fatal("p1 third admit should fail: burst spent")
	}
	// q spends one of its burst of 2: q bucket ends at 1 token, strictly fuller
	// than the spent p1. Capacity is now full (p1, q).
	if !a.TryAdmit("q") {
		t.Fatal("q first admit should succeed")
	}
	// A new principal r forces one eviction. The victim must be the FULLER bucket
	// (q at 1), never the spent one (p1 at 0). If eviction took p1, a returning
	// p1 would get a fresh full burst, which is the reset-by-churn attack.
	a.TryAdmit("r")
	if a.TryAdmit("p1") {
		t.Fatal("spent p1 was reset by eviction; eviction must take the fuller bucket, not the spent one")
	}
}
