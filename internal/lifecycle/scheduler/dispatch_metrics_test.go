package scheduler

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/rvben/shinyhub/internal/db"
	jobspkg "github.com/rvben/shinyhub/internal/jobs"
)

type dispatchFailure struct{ Slug, Schedule, Reason string }

type fakeMetrics struct {
	mu       sync.Mutex
	seeded   []string
	failures []dispatchFailure
}

func (f *fakeMetrics) SeedSchedule(slug, schedule string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.seeded = append(f.seeded, slug+"/"+schedule)
}

func (f *fakeMetrics) RecordScheduleDispatchFailure(slug, schedule, reason string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.failures = append(f.failures, dispatchFailure{slug, schedule, reason})
}

func (f *fakeMetrics) snapshot() ([]string, []dispatchFailure) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.seeded...), append([]dispatchFailure(nil), f.failures...)
}

func TestScheduler_Dispatch_CountsRejectionsByReason(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want []dispatchFailure
	}{
		{"deploying", fmt.Errorf("run: %w", jobspkg.ErrAppDeploying), []dispatchFailure{{"sales", "nightly", DispatchRejectAppDeploying}}},
		{"quarantined", fmt.Errorf("run: %w", jobspkg.ErrAppCompatibilityQuarantined), []dispatchFailure{{"sales", "nightly", DispatchRejectQuarantined}}},
		{"other", errors.New("disk full"), []dispatchFailure{{"sales", "nightly", DispatchRejectError}}},
		// A stopping manager is shutdown, not a rejection.
		{"stopped", fmt.Errorf("run: %w", jobspkg.ErrManagerStopped), nil},
		{"accepted", nil, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fj := &fakeJobs{err: tc.err}
			m := &fakeMetrics{}
			s := New(fj, &fakeStore{}, time.UTC)
			s.SetMetrics(m)

			s.dispatch(dispatchTarget{scheduleID: 3, name: "nightly", slug: "sales"}, TriggerSchedule)

			if fj.callCount() != 1 {
				t.Fatalf("Run calls = %d, want 1", fj.callCount())
			}
			_, got := m.snapshot()
			if fmt.Sprint(got) != fmt.Sprint(tc.want) {
				t.Fatalf("dispatch failures = %v, want %v", got, tc.want)
			}
		})
	}
}

// A catch-up dispatch rejected at startup is reported under the schedule's
// name and slug, the same identity its runs carry.
func TestScheduler_MissedRunRejection_IsNamed(t *testing.T) {
	store := &fakeStore{
		enabled: []*db.Schedule{
			{ID: 1, AppID: 4, Name: "refresh", CronExpr: "@every 24h", Enabled: true, MissedPolicy: "run_once"},
		},
		lastSucc: map[int64]*db.ScheduleRun{
			1: {ID: 99, ScheduleID: 1, StartedAt: time.Now().Add(-25 * time.Hour).UTC(), Status: "succeeded"},
		},
	}
	fj := &fakeJobs{err: jobspkg.ErrAppDeploying}
	m := &fakeMetrics{}
	s := New(fj, store, time.UTC)
	s.SetMetrics(m)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := s.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer s.Stop()

	deadline := time.Now().Add(2 * time.Second)
	for {
		if _, got := m.snapshot(); len(got) > 0 {
			want := dispatchFailure{"app-4", "refresh", DispatchRejectAppDeploying}
			if len(got) != 1 || got[0] != want {
				t.Fatalf("dispatch failures = %v, want [%v]", got, want)
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("missed-run rejection was never recorded")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// Registering a schedule seeds its series so increase() sees the first
// failure; a reload after a rename seeds the new name.
func TestScheduler_Register_SeedsMetrics(t *testing.T) {
	sched := &db.Schedule{ID: 2, AppID: 9, Name: "before", CronExpr: "@every 24h", Enabled: true, MissedPolicy: "skip"}
	store := &fakeStore{enabled: []*db.Schedule{sched}}
	m := &fakeMetrics{}
	s := New(&fakeJobs{}, store, time.UTC)
	s.SetMetrics(m)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := s.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer s.Stop()

	sched.Name = "after"
	if err := s.Reload(2); err != nil {
		t.Fatalf("Reload: %v", err)
	}
	seeded, _ := m.snapshot()
	if fmt.Sprint(seeded) != fmt.Sprint([]string{"app-9/before", "app-9/after"}) {
		t.Fatalf("seeded = %v", seeded)
	}
}
