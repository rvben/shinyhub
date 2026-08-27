package activation

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rvben/shinyhub/internal/db"
)

type fakeStore struct {
	queue                []*db.ScheduleActivation
	finished             []string
	finishedErrors       []string
	finishedCountAttempt []bool
	deferred             []string
	deferredDueAt        []time.Time
}

func (f *fakeStore) ClaimNextScheduleActivation(time.Time) (*db.ScheduleActivation, error) {
	if len(f.queue) == 0 {
		return nil, db.ErrNotFound
	}
	a := f.queue[0]
	f.queue = f.queue[1:]
	return a, nil
}
func (f *fakeStore) FinishScheduleActivation(_ int64, status, lastError string, _ time.Time, countAttempt bool) error {
	f.finished = append(f.finished, status)
	f.finishedErrors = append(f.finishedErrors, lastError)
	f.finishedCountAttempt = append(f.finishedCountAttempt, countAttempt)
	return nil
}
func (f *fakeStore) DeferScheduleActivation(_ int64, status, _ string, dueAt, _ time.Time) error {
	f.deferred = append(f.deferred, status)
	f.deferredDueAt = append(f.deferredDueAt, dueAt)
	return nil
}

type fakeRunner struct{ err error }

func (r fakeRunner) Roll(context.Context, *db.ScheduleActivation) error { return r.err }

type panicRunner struct{}

func (panicRunner) Roll(context.Context, *db.ScheduleActivation) error { panic("runtime fault") }

type failingClaimStore struct{ calls atomic.Int64 }

func (s *failingClaimStore) ClaimNextScheduleActivation(time.Time) (*db.ScheduleActivation, error) {
	s.calls.Add(1)
	return nil, errors.New("database unavailable")
}
func (*failingClaimStore) FinishScheduleActivation(int64, string, string, time.Time, bool) error {
	return nil
}
func (*failingClaimStore) DeferScheduleActivation(int64, string, string, time.Time, time.Time) error {
	return nil
}

func TestCoordinatorMapsRuntimeOutcomesToDurableActivationStates(t *testing.T) {
	tests := []struct {
		name       string
		runErr     error
		wantFinish string
		wantDefer  string
		wantErr    bool
	}{
		{name: "success", wantFinish: "succeeded"},
		{name: "already current", runErr: ErrNotNeeded, wantFinish: "not_needed"},
		{name: "unsupported", runErr: ErrUnsupported, wantFinish: "blocked_unsupported"},
		{name: "deleted", runErr: ErrTargetDeleted, wantFinish: "target_deleted"},
		{name: "capacity", runErr: &CapacityError{Reason: "memory floor", RetryAfter: time.Minute}, wantDefer: "deferred_capacity"},
		{name: "transient", runErr: &RetryableError{Reason: "temporary boot failure"}, wantDefer: "pending"},
		{name: "repair", runErr: &RepairRequiredError{Reason: "surge owns the safety route"}, wantDefer: "repairing"},
		{name: "runtime failure", runErr: errors.New("health timeout"), wantFinish: "failed", wantErr: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			store := &fakeStore{queue: []*db.ScheduleActivation{{ID: 7, AppSlug: "demo", ScheduleRunID: int64ptr(41)}}}
			coordinator := New(store, fakeRunner{err: tc.runErr}, time.Second)
			worked, err := coordinator.ProcessNext(context.Background())
			if !worked {
				t.Fatal("ProcessNext worked=false, want true")
			}
			if (err != nil) != tc.wantErr {
				t.Fatalf("ProcessNext err=%v, wantErr=%v", err, tc.wantErr)
			}
			if tc.wantFinish != "" && (len(store.finished) != 1 || store.finished[0] != tc.wantFinish) {
				t.Fatalf("finished=%v, want %q", store.finished, tc.wantFinish)
			}
			if tc.wantDefer != "" && (len(store.deferred) != 1 || store.deferred[0] != tc.wantDefer) {
				t.Fatalf("deferred=%v, want %q", store.deferred, tc.wantDefer)
			}
		})
	}
}

func TestCoordinatorRepairIgnoresOrdinaryAttemptBudget(t *testing.T) {
	store := &fakeStore{queue: []*db.ScheduleActivation{{ID: 7, AppSlug: "demo", Attempts: 99}}}
	coordinator := New(store, fakeRunner{err: &RepairRequiredError{Reason: "canonical slot detached"}}, time.Second)
	worked, err := coordinator.ProcessNext(context.Background())
	if !worked || err != nil {
		t.Fatalf("ProcessNext = %v, %v; want true, nil", worked, err)
	}
	if len(store.finished) != 0 || len(store.deferred) != 1 || store.deferred[0] != "repairing" {
		t.Fatalf("finished=%v deferred=%v, want durable repairing defer", store.finished, store.deferred)
	}
}

func TestCoordinatorCapacityDeferralUsesDurableExponentialBackoff(t *testing.T) {
	now := time.Date(2026, 8, 27, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		deferrals int
		wantDelay time.Duration
	}{
		{deferrals: 0, wantDelay: time.Minute},
		{deferrals: 1, wantDelay: 2 * time.Minute},
		{deferrals: 2, wantDelay: 4 * time.Minute},
		{deferrals: 3, wantDelay: 8 * time.Minute},
		{deferrals: 4, wantDelay: 15 * time.Minute},
		{deferrals: 20, wantDelay: 15 * time.Minute},
	}
	for _, tc := range tests {
		store := &fakeStore{queue: []*db.ScheduleActivation{{
			ID: 7, AppSlug: "demo", CreatedAt: now.Add(-time.Hour), CapacityDeferrals: tc.deferrals,
		}}}
		coordinator := New(store, fakeRunner{err: &CapacityError{Reason: "host pressure"}}, time.Second)
		coordinator.now = func() time.Time { return now }
		worked, err := coordinator.ProcessNext(context.Background())
		if !worked || err != nil {
			t.Fatalf("deferrals=%d ProcessNext = %v, %v; want true, nil", tc.deferrals, worked, err)
		}
		if len(store.deferredDueAt) != 1 || !store.deferredDueAt[0].Equal(now.Add(tc.wantDelay)) {
			t.Fatalf("deferrals=%d due_at=%v, want %s", tc.deferrals, store.deferredDueAt, now.Add(tc.wantDelay))
		}
	}
}

func TestCoordinatorCapacityDeferralExpiresWithoutChargingRollAttempt(t *testing.T) {
	now := time.Date(2026, 8, 27, 12, 0, 0, 0, time.UTC)
	store := &fakeStore{queue: []*db.ScheduleActivation{{
		ID: 7, AppSlug: "demo", CreatedAt: now.Add(-24 * time.Hour),
		CapacityDeferredAt: timePtr(now.Add(-2 * time.Hour)), MaxDeferAgeSeconds: 3600,
	}}}
	coordinator := New(store, fakeRunner{err: &CapacityError{Reason: "host pressure"}}, time.Second)
	coordinator.now = func() time.Time { return now }
	worked, err := coordinator.ProcessNext(context.Background())
	if !worked || err == nil {
		t.Fatalf("ProcessNext = %v, %v; want terminal failure", worked, err)
	}
	if len(store.finished) != 1 || store.finished[0] != "failed" || store.finishedCountAttempt[0] {
		t.Fatalf("finished=%v count_attempt=%v, want failed without rollout attempt", store.finished, store.finishedCountAttempt)
	}
	if !strings.Contains(store.finishedErrors[0], "capacity deferral expired after 1h0m0s") {
		t.Fatalf("last_error=%q, want expiry reason", store.finishedErrors[0])
	}
}

func TestCoordinatorCapacityDeadlineStartsAtFirstCapacityDeferral(t *testing.T) {
	now := time.Date(2026, 8, 27, 12, 0, 0, 0, time.UTC)
	store := &fakeStore{queue: []*db.ScheduleActivation{{
		ID: 7, AppSlug: "demo", CreatedAt: now.Add(-24 * time.Hour), MaxDeferAgeSeconds: 3600,
	}}}
	coordinator := New(store, fakeRunner{err: &CapacityError{Reason: "host pressure"}}, time.Second)
	coordinator.now = func() time.Time { return now }
	worked, err := coordinator.ProcessNext(context.Background())
	if !worked || err != nil {
		t.Fatalf("ProcessNext = %v, %v; want first capacity defer", worked, err)
	}
	if len(store.finished) != 0 || len(store.deferredDueAt) != 1 || !store.deferredDueAt[0].Equal(now.Add(time.Minute)) {
		t.Fatalf("finished=%v due_at=%v, want a fresh one-hour capacity window", store.finished, store.deferredDueAt)
	}
}

func TestCoordinatorNoDueWorkIsBenign(t *testing.T) {
	coordinator := New(&fakeStore{}, fakeRunner{}, time.Second)
	worked, err := coordinator.ProcessNext(context.Background())
	if worked || err != nil {
		t.Fatalf("ProcessNext = %v, %v; want false, nil", worked, err)
	}
}

func TestCoordinatorBacksOffAfterStoreErrors(t *testing.T) {
	store := &failingClaimStore{}
	coordinator := New(store, fakeRunner{}, 20*time.Millisecond)
	ctx, cancel := context.WithTimeout(context.Background(), 85*time.Millisecond)
	defer cancel()
	coordinator.Start(ctx)
	if calls := store.calls.Load(); calls < 2 || calls > 6 {
		t.Fatalf("claim calls=%d during 85ms outage, want poll-paced retries without a hot loop", calls)
	}
}

func TestCoordinatorContainsRunnerPanicAndDefersRepair(t *testing.T) {
	store := &fakeStore{queue: []*db.ScheduleActivation{{ID: 7, AppSlug: "demo", Attempts: 99}}}
	coordinator := New(store, panicRunner{}, time.Second)
	worked, err := coordinator.ProcessNext(context.Background())
	if !worked || err != nil {
		t.Fatalf("ProcessNext = %v, %v; want true, nil", worked, err)
	}
	if len(store.finished) != 0 || len(store.deferred) != 1 || store.deferred[0] != "repairing" {
		t.Fatalf("finished=%v deferred=%v, want panic converted to nonterminal repair", store.finished, store.deferred)
	}
}

func int64ptr(v int64) *int64        { return &v }
func timePtr(v time.Time) *time.Time { return &v }
