package api

import (
	"fmt"
	"testing"
	"time"
)

// The keyed rate limiter must not retain a map entry per distinct key
// forever: once a key's timestamps age past the window, a periodic global
// sweep has to drop it. Without the sweep the map grows unbounded under
// many distinct source IPs over long uptime.
func TestKeyedRateLimiter_SweepsStaleKeys(t *testing.T) {
	window := 20 * time.Millisecond
	rl := newKeyedRateLimiter(5, window)

	for i := 0; i < 500; i++ {
		rl.allow(fmt.Sprintf("ip-%d", i))
	}

	rl.mu.Lock()
	grown := len(rl.windows)
	rl.mu.Unlock()
	if grown != 500 {
		t.Fatalf("expected 500 tracked keys, got %d", grown)
	}

	// Let every recorded timestamp age out, then a single new request on a
	// fresh key triggers the once-per-window global sweep.
	time.Sleep(window + 5*time.Millisecond)
	rl.allow("trigger")

	rl.mu.Lock()
	remaining := len(rl.windows)
	rl.mu.Unlock()

	// Only the just-touched "trigger" key should survive; the 500 stale
	// keys must be gone.
	if remaining != 1 {
		t.Fatalf("after sweep: %d keys retained, want 1 (stale keys leaked)", remaining)
	}
}
