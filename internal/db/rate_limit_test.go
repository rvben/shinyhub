package db_test

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rvben/shinyhub/internal/db"
	"github.com/rvben/shinyhub/internal/dbtest"
)

// TestRateLimitAllow_EnforcesLimit verifies the windowed counter denies the
// attempt that would exceed the limit and that distinct keys are independent.
func TestRateLimitAllow_EnforcesLimit(t *testing.T) {
	store := dbtest.New(t)
	const bucket, key, limit = "login", "203.0.113.7", 3
	// Fixed window bucketed on floor(now/window): a burst straddling a boundary
	// starts a fresh count and the over-limit attempt is allowed. A window far
	// longer than the burst makes that impossible without weakening the assertion.
	window := 24 * time.Hour

	for i := 0; i < limit; i++ {
		ok, err := store.RateLimitAllow(bucket, key, limit, window)
		if err != nil {
			t.Fatalf("allow %d: %v", i, err)
		}
		if !ok {
			t.Fatalf("attempt %d within limit should be allowed", i)
		}
	}
	ok, err := store.RateLimitAllow(bucket, key, limit, window)
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Fatal("attempt over the limit should be denied")
	}

	// A different key has its own budget.
	ok, err = store.RateLimitAllow(bucket, "198.51.100.2", limit, window)
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("a different key should be allowed")
	}
}

// TestRateLimitAllow_ConcurrentBurstEnforcesLimit is the security guarantee: a
// parallel burst on one key allows exactly limit attempts, never more. This is
// the case a non-atomic check-then-insert would fail (every concurrent reader
// sees the pre-burst count and all insert).
func TestRateLimitAllow_ConcurrentBurstEnforcesLimit(t *testing.T) {
	store := dbtest.New(t)
	const bucket, key, limit = "login", "203.0.113.21", 5
	const attempts = 40
	window := time.Minute

	var wg sync.WaitGroup
	var allowed int64
	errs := make(chan error, attempts)
	for i := 0; i < attempts; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ok, err := store.RateLimitAllow(bucket, key, limit, window)
			if err != nil {
				errs <- err
				return
			}
			if ok {
				atomic.AddInt64(&allowed, 1)
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatalf("concurrent allow: %v", err)
	}
	if allowed != limit {
		t.Fatalf("concurrent burst allowed %d attempts, want exactly %d", allowed, limit)
	}
}

// TestRateLimitAllow_SharedAcrossInstances is the HA guarantee: two stores
// against the same Postgres database share one combined counter, so attempts
// spread across instances cannot exceed the global limit.
func TestRateLimitAllow_SharedAcrossInstances(t *testing.T) {
	store1, dsn := dbtest.NewPostgres(t)
	store2, err := db.Open(dsn)
	if err != nil {
		t.Fatalf("open second store: %v", err)
	}
	defer store2.Close()

	const bucket, key, limit = "login", "203.0.113.9", 4
	window := time.Minute

	// Four attempts split across both instances exactly hit the limit.
	for i, s := range []*db.Store{store1, store2, store1, store2} {
		ok, err := s.RateLimitAllow(bucket, key, limit, window)
		if err != nil {
			t.Fatalf("allow %d: %v", i, err)
		}
		if !ok {
			t.Fatalf("attempt %d within combined limit should be allowed", i)
		}
	}
	// The next attempt from either instance is denied: the counter is shared.
	ok, err := store1.RateLimitAllow(bucket, key, limit, window)
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Fatal("combined attempts exceed the limit; should be denied by the shared limiter")
	}
}

// TestPruneRateLimitCounters removes only windows that started before retention.
func TestPruneRateLimitCounters(t *testing.T) {
	store := dbtest.New(t)
	oldWindow := time.Now().Add(-2 * time.Hour).UnixMilli()
	freshWindow := time.Now().UnixMilli()
	if _, err := store.DB().Exec(
		`INSERT INTO rate_limit_counters (bucket, rl_key, window_start_ms, count) VALUES (?, ?, ?, ?), (?, ?, ?, ?)`,
		"login", "203.0.113.5", oldWindow, 3, "login", "203.0.113.5", freshWindow, 1); err != nil {
		t.Fatalf("seed rows: %v", err)
	}

	n, err := store.PruneRateLimitCounters(time.Hour)
	if err != nil {
		t.Fatalf("prune: %v", err)
	}
	if n != 1 {
		t.Fatalf("prune removed %d rows, want 1 (the 2h-old window)", n)
	}

	var remaining int
	if err := store.DB().QueryRow(`SELECT COUNT(*) FROM rate_limit_counters`).Scan(&remaining); err != nil {
		t.Fatalf("count: %v", err)
	}
	if remaining != 1 {
		t.Fatalf("remaining rows = %d, want 1 (the fresh window survives)", remaining)
	}
}
