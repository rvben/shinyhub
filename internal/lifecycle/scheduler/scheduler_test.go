package scheduler

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rvben/shinyhub/internal/db"
)

// fakeJobs records calls to Run.
type fakeJobs struct {
	mu    sync.Mutex
	calls []int64
}

func (f *fakeJobs) Run(id int64, trigger string, userID *int64) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, id)
	return int64(len(f.calls)), nil
}

func (f *fakeJobs) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

// fakeStore implements scheduler.Store.
type fakeStore struct {
	enabled     []*db.Schedule
	interrupted int64
	lastSucc    map[int64]*db.ScheduleRun
}

func (s *fakeStore) ListEnabledSchedules() ([]*db.Schedule, error) { return s.enabled, nil }
func (s *fakeStore) GetSchedule(id int64) (*db.Schedule, error) {
	for _, sched := range s.enabled {
		if sched.ID == id {
			return sched, nil
		}
	}
	return nil, db.ErrNotFound
}
func (s *fakeStore) MarkRunningSchedulesInterrupted() (int64, error) {
	atomic.AddInt64(&s.interrupted, 1)
	return 1, nil
}
func (s *fakeStore) LastSuccessfulRun(id int64) (*db.ScheduleRun, error) {
	if r, ok := s.lastSucc[id]; ok {
		return r, nil
	}
	return nil, db.ErrNotFound
}

func TestScheduler_Start_MarksInterruptedAndLoadsEnabled(t *testing.T) {
	store := &fakeStore{
		enabled: []*db.Schedule{
			{ID: 1, CronExpr: "@every 24h", Enabled: true, MissedPolicy: "skip"},
		},
	}
	jobs := &fakeJobs{}
	s := New(jobs, store)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := s.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if atomic.LoadInt64(&store.interrupted) != 1 {
		t.Fatal("expected MarkRunningSchedulesInterrupted call")
	}
	if got := s.entryCount(); got != 1 {
		t.Fatalf("expected 1 cron entry, got %d", got)
	}
	s.Stop()
}

func TestScheduler_MissedRun_DispatchesOnceForRunOnceSchedule(t *testing.T) {
	pastRun := time.Now().Add(-25 * time.Hour).UTC()
	store := &fakeStore{
		enabled: []*db.Schedule{
			{ID: 1, CronExpr: "@every 24h", Enabled: true, MissedPolicy: "run_once"},
		},
		lastSucc: map[int64]*db.ScheduleRun{
			1: {ID: 99, ScheduleID: 1, StartedAt: pastRun, Status: "succeeded"},
		},
	}
	jobs := &fakeJobs{}
	s := New(jobs, store)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := s.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	waitForCallCount(t, jobs, 1, time.Second)
	s.Stop()
}

func TestScheduler_MissedRun_SkipsWhenPolicyIsSkip(t *testing.T) {
	pastRun := time.Now().Add(-25 * time.Hour).UTC()
	store := &fakeStore{
		enabled: []*db.Schedule{
			{ID: 1, CronExpr: "@every 24h", Enabled: true, MissedPolicy: "skip"},
		},
		lastSucc: map[int64]*db.ScheduleRun{
			1: {ID: 99, ScheduleID: 1, StartedAt: pastRun, Status: "succeeded"},
		},
	}
	jobs := &fakeJobs{}
	s := New(jobs, store)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	_ = s.Start(ctx)
	time.Sleep(150 * time.Millisecond)
	if jobs.callCount() != 0 {
		t.Fatalf("expected no missed-run dispatch with policy=skip, got %d", jobs.callCount())
	}
	s.Stop()
}

func TestScheduler_Reload_RegistersNewEntry(t *testing.T) {
	store := &fakeStore{enabled: []*db.Schedule{}}
	jobs := &fakeJobs{}
	s := New(jobs, store)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := s.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}

	// Pretend a new schedule was just created in the store.
	store.enabled = append(store.enabled, &db.Schedule{
		ID: 7, CronExpr: "@every 24h", Enabled: true, MissedPolicy: "skip",
	})
	if err := s.Reload(7); err != nil {
		t.Fatalf("Reload: %v", err)
	}
	if got := s.entryCount(); got != 1 {
		t.Fatalf("expected 1 entry after reload, got %d", got)
	}
	s.Stop()
}

func waitForCallCount(t *testing.T, j *fakeJobs, want int, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if j.callCount() >= want {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %d job calls (got %d)", want, j.callCount())
}
