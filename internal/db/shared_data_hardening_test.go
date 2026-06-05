package db_test

import (
	"errors"
	"fmt"
	"sync"
	"testing"

	"github.com/rvben/shinyhub/internal/db"
)

// ERR-2: a self-mount returns a typed sentinel so the API can map it to 400.
func TestSharedData_SelfMountReturnsSentinel(t *testing.T) {
	store := newScheduleStore(t)
	appID := newScheduleAppFixture(t, store, "report")
	err := store.GrantSharedData(appID, appID)
	if !errors.Is(err, db.ErrSelfMount) {
		t.Fatalf("expected ErrSelfMount, got %v", err)
	}
}

// ERR-3: granting the same mount twice returns a typed sentinel so the API can
// map it to 409 instead of leaking the raw SQLite UNIQUE-constraint text as a 500.
func TestSharedData_DuplicateReturnsSentinel(t *testing.T) {
	store := newScheduleStore(t)
	consumer := newScheduleAppFixture(t, store, "report")
	source := newScheduleAppFixture(t, store, "fetch")
	if err := store.GrantSharedData(consumer, source); err != nil {
		t.Fatalf("first grant: %v", err)
	}
	err := store.GrantSharedData(consumer, source)
	if !errors.Is(err, db.ErrDuplicateMount) {
		t.Fatalf("expected ErrDuplicateMount, got %v", err)
	}
}

// SCH-3: a grant that would close a cycle (A reads B, then B reads A) is rejected
// with a typed sentinel rather than silently allowed.
func TestSharedData_CycleReturnsSentinel(t *testing.T) {
	store := newScheduleStore(t)
	a := newScheduleAppFixture(t, store, "report")
	b := newScheduleAppFixture(t, store, "fetch")
	if err := store.GrantSharedData(a, b); err != nil {
		t.Fatalf("grant a->b: %v", err)
	}
	err := store.GrantSharedData(b, a)
	if !errors.Is(err, db.ErrSharedDataCycle) {
		t.Fatalf("expected ErrSharedDataCycle for b->a, got %v", err)
	}
}

// SCH-3: a transitive cycle (A reads B, B reads C, then C reads A) is rejected.
func TestSharedData_TransitiveCycleReturnsSentinel(t *testing.T) {
	store := newScheduleStore(t)
	a := newScheduleAppFixture(t, store, "report")
	b := newScheduleAppFixture(t, store, "fetch")
	c := newScheduleAppFixture(t, store, "warm")
	if err := store.GrantSharedData(a, b); err != nil {
		t.Fatalf("grant a->b: %v", err)
	}
	if err := store.GrantSharedData(b, c); err != nil {
		t.Fatalf("grant b->c: %v", err)
	}
	err := store.GrantSharedData(c, a)
	if !errors.Is(err, db.ErrSharedDataCycle) {
		t.Fatalf("expected ErrSharedDataCycle for c->a, got %v", err)
	}
}

// SCH-3: the cycle check and the insert must be atomic. Two opposing grants
// (a->b and b->a) submitted concurrently must never both succeed; otherwise a
// cycle slips past the check-then-insert window. Many independent app pairs are
// raced to make the interleaving likely to surface a non-atomic implementation.
func TestSharedData_ConcurrentOpposingGrantsNeverCycle(t *testing.T) {
	store := newScheduleStore(t)
	const pairs = 60

	type pair struct{ a, b int64 }
	ps := make([]pair, pairs)
	for i := range ps {
		ps[i] = pair{
			a: newScheduleAppFixture(t, store, fmt.Sprintf("a%d", i)),
			b: newScheduleAppFixture(t, store, fmt.Sprintf("b%d", i)),
		}
	}

	var wg sync.WaitGroup
	start := make(chan struct{})
	results := make([]struct{ ab, ba error }, pairs)
	for i, p := range ps {
		wg.Add(2)
		go func(i int, p pair) {
			defer wg.Done()
			<-start
			results[i].ab = store.GrantSharedData(p.a, p.b)
		}(i, p)
		go func(i int, p pair) {
			defer wg.Done()
			<-start
			results[i].ba = store.GrantSharedData(p.b, p.a)
		}(i, p)
	}
	close(start)
	wg.Wait()

	for i := range ps {
		ab, ba := results[i].ab, results[i].ba
		if ab == nil && ba == nil {
			t.Errorf("pair %d: both a->b and b->a succeeded, forming a cycle", i)
			continue
		}
		// Exactly one must succeed; the loser must be rejected as a cycle (not
		// some other error), which is the public contract of GrantSharedData.
		if ab != nil && !errors.Is(ab, db.ErrSharedDataCycle) {
			t.Errorf("pair %d: a->b failed with unexpected error: %v", i, ab)
		}
		if ba != nil && !errors.Is(ba, db.ErrSharedDataCycle) {
			t.Errorf("pair %d: b->a failed with unexpected error: %v", i, ba)
		}
	}
}
