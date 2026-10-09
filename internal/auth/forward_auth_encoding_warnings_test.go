package auth

import (
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestEncodingWarnings_RecurWithoutFlooding(t *testing.T) {
	var limiter encodingWarnings
	now := time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)
	check := func(peer, header string, at time.Time, want bool) {
		t.Helper()
		allowed, overflow := limiter.allow(peer, header, at)
		if allowed != want || overflow {
			t.Fatalf("peer=%s header=%s at=%s: allowed=%v overflow=%v, want allowed=%v", peer, header, at, allowed, overflow, want)
		}
	}
	check("127.0.0.1", "Remote-Name", now, true)
	check("127.0.0.1", "Remote-Name", now, false)
	check("127.0.0.1", "Remote-Name", now.Add(forwardAuthEncodingWarningInterval-time.Nanosecond), false)
	check("127.0.0.1", "Remote-Email", now, true)
	check("127.0.0.2", "Remote-Name", now, true)
	check("127.0.0.1", "Remote-Name", now.Add(forwardAuthEncodingWarningInterval), true)
	check("127.0.0.1", "Remote-Name", now.Add(forwardAuthEncodingWarningInterval+time.Second), false)
	check("127.0.0.1", "Remote-Name", now.Add(2*forwardAuthEncodingWarningInterval), true)
}

func TestEncodingWarnings_BoundedPeersAndOverflowBudget(t *testing.T) {
	var limiter encodingWarnings
	now := time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)
	for i := 0; i < forwardAuthEncodingWarningEntries; i++ {
		allowed, overflow := limiter.allow(fmt.Sprintf("peer-%d", i), "Remote-Name", now)
		if !allowed || overflow {
			t.Fatalf("tracked peer %d: allowed=%v overflow=%v", i, allowed, overflow)
		}
	}
	for i := 0; i < 100; i++ {
		allowed, overflow := limiter.allow(fmt.Sprintf("overflow-%d", i), "Remote-Name", now)
		if allowed != (i == 0) || !overflow {
			t.Fatalf("overflow peer %d: allowed=%v overflow=%v", i, allowed, overflow)
		}
	}
	if len(limiter.last) != forwardAuthEncodingWarningEntries {
		t.Fatalf("unbounded tracking: %d entries", len(limiter.last))
	}
	// Renew tracked budgets before expiry to keep the table full while the
	// shared overflow budget becomes eligible for another diagnostic.
	for i := 0; i < forwardAuthEncodingWarningEntries; i++ {
		limiter.allow(fmt.Sprintf("peer-%d", i), "Remote-Name", now.Add(forwardAuthEncodingWarningInterval))
	}
	allowed, overflow := limiter.allow("overflow-again", "Remote-Name", now.Add(forwardAuthEncodingWarningInterval))
	if !allowed || !overflow {
		t.Fatalf("overflow did not recur: allowed=%v overflow=%v", allowed, overflow)
	}
	// Once tracked budgets expire, a new peer receives its own budget again.
	allowed, overflow = limiter.allow("new-peer", "Remote-Name", now.Add(2*forwardAuthEncodingWarningInterval))
	if !allowed || overflow {
		t.Fatalf("expired capacity was not reclaimed: allowed=%v overflow=%v", allowed, overflow)
	}
}

func TestEncodingWarnings_ConcurrentRequestsEmitOneWarning(t *testing.T) {
	var limiter encodingWarnings
	var allowed atomic.Int64
	var workers sync.WaitGroup
	now := time.Now()
	for i := 0; i < 100; i++ {
		workers.Go(func() {
			if ok, _ := limiter.allow("127.0.0.1", "Remote-Name", now); ok {
				allowed.Add(1)
			}
		})
	}
	workers.Wait()
	if got := allowed.Load(); got != 1 {
		t.Fatalf("concurrent diagnostics=%d, want 1", got)
	}
}

func TestEncodingWarnings_ReclaimStaggeredExpiries(t *testing.T) {
	var limiter encodingWarnings
	now := time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)
	limiter.allow("first", "Remote-Name", now)
	limiter.allow("second", "Remote-Name", now.Add(time.Minute))
	for i := 2; i < forwardAuthEncodingWarningEntries; i++ {
		limiter.allow(fmt.Sprintf("peer-%d", i), "Remote-Name", now.Add(2*time.Minute))
	}
	check := func(peer string, at time.Time, wantAllowed, wantOverflow bool) {
		t.Helper()
		allowed, overflow := limiter.allow(peer, "Remote-Name", at)
		if allowed != wantAllowed || overflow != wantOverflow {
			t.Fatalf("at=%s allowed=%v overflow=%v, want %v/%v", at, allowed, overflow, wantAllowed, wantOverflow)
		}
	}
	check("overflow", now.Add(2*time.Minute), true, true)
	check("too-soon", now.Add(forwardAuthEncodingWarningInterval-time.Nanosecond), false, true)
	// Capacity is reclaimed at the earliest expiry even though the overflow
	// warning budget remains exhausted. Later entries must stay throttled.
	check("replacement-first", now.Add(forwardAuthEncodingWarningInterval), true, false)
	check("second", now.Add(forwardAuthEncodingWarningInterval), false, false)
	check("still-full", now.Add(forwardAuthEncodingWarningInterval+time.Minute-time.Nanosecond), false, true)
	check("replacement-second", now.Add(forwardAuthEncodingWarningInterval+time.Minute), true, false)
	if len(limiter.last) != forwardAuthEncodingWarningEntries {
		t.Fatalf("tracking count=%d", len(limiter.last))
	}
}
