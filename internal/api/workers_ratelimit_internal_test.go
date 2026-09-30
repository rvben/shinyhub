package api

import (
	"fmt"
	"testing"
	"time"
)

// TestWorkerAPI_RegisterLimiterThrottles verifies the register limiter still
// throttles a burst from one source: the sixth request inside the window is
// rejected, matching the small-burst-then-throttle policy that dampens
// join-token guessing.
func TestWorkerAPI_RegisterLimiterThrottles(t *testing.T) {
	a := NewWorkerAPI(nil, nil, nil, "")

	allowed := 0
	for i := 0; i < 6; i++ {
		if a.registerRL.allow("198.51.100.9") {
			allowed++
		}
	}
	if allowed != 5 {
		t.Fatalf("allowed %d of 6 burst requests, want 5 then throttle", allowed)
	}
}

// TestWorkerAPI_RegisterLimiterEvictsIdleSources verifies the register limiter
// does not retain a map entry per source host forever. A long-lived control
// plane facing many distinct worker source IPs must not leak memory: once a
// source's window ages out, the periodic sweep drops it.
func TestWorkerAPI_RegisterLimiterEvictsIdleSources(t *testing.T) {
	a := NewWorkerAPI(nil, nil, nil, "")
	// Keep entries live while building the burst, even on a busy test host.
	a.registerRL = newKeyedRateLimiter(5, time.Hour)

	for i := 0; i < 500; i++ {
		a.registerRL.allow(fmt.Sprintf("ip-%d", i))
	}
	a.registerRL.mu.Lock()
	grown := len(a.registerRL.windows)
	a.registerRL.mu.Unlock()
	if grown != 500 {
		t.Fatalf("expected 500 tracked source keys, got %d", grown)
	}

	// Age recorded timestamps and the sweep boundary without requiring the
	// host to insert 500 entries within a 20ms scheduling window.
	a.registerRL.mu.Lock()
	for key, timestamps := range a.registerRL.windows {
		for i := range timestamps {
			timestamps[i] = timestamps[i].Add(-2 * time.Hour)
		}
		a.registerRL.windows[key] = timestamps
	}
	a.registerRL.lastSweep = a.registerRL.lastSweep.Add(-2 * time.Hour)
	a.registerRL.mu.Unlock()
	// A fresh request still exercises the actual periodic cleanup path.
	a.registerRL.allow("trigger")

	a.registerRL.mu.Lock()
	remaining := len(a.registerRL.windows)
	a.registerRL.mu.Unlock()
	if remaining != 1 {
		t.Fatalf("after sweep: %d source keys retained, want 1 (idle keys leaked)", remaining)
	}
}
