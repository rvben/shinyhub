package db

import (
	"errors"
	"testing"
)

// ERR-2: a self-mount returns a typed sentinel so the API can map it to 400.
func TestSharedData_SelfMountReturnsSentinel(t *testing.T) {
	store := newScheduleStore(t)
	appID := newScheduleAppFixture(t, store, "report")
	err := store.GrantSharedData(appID, appID)
	if !errors.Is(err, ErrSelfMount) {
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
	if !errors.Is(err, ErrDuplicateMount) {
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
	if !errors.Is(err, ErrSharedDataCycle) {
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
	if !errors.Is(err, ErrSharedDataCycle) {
		t.Fatalf("expected ErrSharedDataCycle for c->a, got %v", err)
	}
}
