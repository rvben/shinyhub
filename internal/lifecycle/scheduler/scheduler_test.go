package scheduler

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rvben/shinyhub/internal/db"
	"github.com/rvben/shinyhub/internal/schedulespec"
)

// fakeJobs records calls to Run.
type fakeJobs struct {
	mu       sync.Mutex
	calls    []int64
	triggers map[int64]string
}

func (f *fakeJobs) Run(id int64, trigger string, userID *int64) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, id)
	if f.triggers == nil {
		f.triggers = map[int64]string{}
	}
	f.triggers[id] = trigger
	return int64(len(f.calls)), nil
}

func (f *fakeJobs) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

func (f *fakeJobs) triggerFor(id int64) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.triggers[id]
}

// fakeStore implements scheduler.Store.
type fakeStore struct {
	enabled        []*db.Schedule
	interrupted    int64
	lastSucc       map[int64]*db.ScheduleRun
	firstFireRetry []int64
}

func (s *fakeStore) ListEnabledSchedules() ([]*db.Schedule, error) { return s.enabled, nil }
func (s *fakeStore) SchedulesNeedingFirstFireRetry() ([]int64, error) {
	return s.firstFireRetry, nil
}
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
	s := New(jobs, store, time.UTC)
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
	// With nothing needing a first-fire retry, Start must not dispatch any run.
	if got := jobs.callCount(); got != 0 {
		t.Fatalf("expected no dispatched runs, got %d", got)
	}
	s.Stop()
}

// TestScheduler_Start_RefiresInterruptedFirstFire verifies that a
// run_on_register first-fire interrupted by a prior restart is re-fired on
// startup, using the "register" trigger so a later restart can recognise the
// new first-fire.
func TestScheduler_Start_RefiresInterruptedFirstFire(t *testing.T) {
	store := &fakeStore{
		enabled: []*db.Schedule{
			{ID: 7, CronExpr: "0 5 * * *", Enabled: true, MissedPolicy: "skip"},
		},
		firstFireRetry: []int64{7},
	}
	jobs := &fakeJobs{}
	s := New(jobs, store, time.UTC)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := s.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	waitForCallCount(t, jobs, 1, time.Second)
	if got := jobs.triggerFor(7); got != "register" {
		t.Fatalf("re-fire trigger = %q, want register", got)
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
	s := New(jobs, store, time.UTC)
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
	s := New(jobs, store, time.UTC)
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
	s := New(jobs, store, time.UTC)
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

// TestScheduler_Register_AcceptsFiveFieldCron guards against accidentally
// re-introducing cron.WithSeconds(), which would reject the 5-field
// expressions the API/UI accepts.
func TestScheduler_Register_AcceptsFiveFieldCron(t *testing.T) {
	store := &fakeStore{
		enabled: []*db.Schedule{
			{ID: 1, CronExpr: "*/2 * * * *", Enabled: true, MissedPolicy: "skip"},
		},
	}
	jobs := &fakeJobs{}
	s := New(jobs, store, time.UTC)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := s.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if got := s.entryCount(); got != 1 {
		t.Fatalf("expected 1 cron entry for 5-field expr, got %d", got)
	}
	s.Stop()
}

// TestScheduler_Timezone_AmsterdamJanFiresAt04UTC asserts that a "0 5 * * *"
// schedule in Europe/Amsterdam fires at 04:00 UTC in January (UTC+1 = -1h
// offset). The test uses the real production registration path via
// prefixedSpec + cron entry Next to ensure no hand-rolled math drift.
func TestScheduler_Timezone_AmsterdamJanFiresAt04UTC(t *testing.T) {
	amsterdam, err := time.LoadLocation("Europe/Amsterdam")
	if err != nil {
		t.Fatalf("LoadLocation: %v", err)
	}
	tz := "Europe/Amsterdam"
	store := &fakeStore{
		enabled: []*db.Schedule{
			{ID: 10, CronExpr: "0 5 * * *", Enabled: true, MissedPolicy: "skip", Timezone: &tz},
		},
	}
	jobs := &fakeJobs{}
	s := New(jobs, store, time.UTC)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := s.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer s.Stop()

	// January: UTC+1 so 05:00 Amsterdam = 04:00 UTC.
	jan := time.Date(2025, time.January, 15, 10, 0, 0, 0, time.UTC)
	spec := s.prefixedSpec(store.enabled[0])
	next := nextAfter(t, spec, jan)

	// next must be 05:00 Amsterdam local in January, which is 04:00 UTC.
	nextInAmsterdam := next.In(amsterdam)
	if nextInAmsterdam.Hour() != 5 || nextInAmsterdam.Minute() != 0 {
		t.Errorf("Jan next fire in Amsterdam = %v, want 05:00", nextInAmsterdam)
	}
	if next.UTC().Hour() != 4 {
		t.Errorf("Jan next fire UTC hour = %d, want 4 (UTC+1 in January)", next.UTC().Hour())
	}
}

// TestScheduler_Timezone_AmsterdamJulFiresAt03UTC asserts that the same daily
// schedule fires at 03:00 UTC in July (UTC+2 during summer time).
func TestScheduler_Timezone_AmsterdamJulFiresAt03UTC(t *testing.T) {
	amsterdam, err := time.LoadLocation("Europe/Amsterdam")
	if err != nil {
		t.Fatalf("LoadLocation: %v", err)
	}
	tz := "Europe/Amsterdam"
	sched := &db.Schedule{ID: 11, CronExpr: "0 5 * * *", Enabled: true, MissedPolicy: "skip", Timezone: &tz}
	store := &fakeStore{enabled: []*db.Schedule{sched}}
	jobs := &fakeJobs{}
	s := New(jobs, store, time.UTC)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := s.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer s.Stop()

	// July: UTC+2 so 05:00 Amsterdam = 03:00 UTC.
	jul := time.Date(2025, time.July, 15, 10, 0, 0, 0, time.UTC)
	spec := s.prefixedSpec(sched)
	next := nextAfter(t, spec, jul)

	nextInAmsterdam := next.In(amsterdam)
	if nextInAmsterdam.Hour() != 5 || nextInAmsterdam.Minute() != 0 {
		t.Errorf("Jul next fire in Amsterdam = %v, want 05:00", nextInAmsterdam)
	}
	if next.UTC().Hour() != 3 {
		t.Errorf("Jul next fire UTC hour = %d, want 3 (UTC+2 in July)", next.UTC().Hour())
	}
}

// TestScheduler_Timezone_SpringForwardGapSkipped asserts that a "30 2 * * *"
// schedule in Europe/Amsterdam skips the spring-forward gap (2025-03-30 at
// 02:00 clocks jump to 03:00, making 02:30 non-existent). The robfig/cron
// library skips non-existent times and fires at the next valid occurrence.
func TestScheduler_Timezone_SpringForwardGapSkipped(t *testing.T) {
	tz := "Europe/Amsterdam"
	sched := &db.Schedule{ID: 12, CronExpr: "30 2 * * *", Enabled: true, MissedPolicy: "skip", Timezone: &tz}
	spec := prefixedSpecFor(t, sched, time.UTC)

	// The clock-change date (2025-03-30): 02:30 does not exist.
	// Start just before what would have been 02:30 on the gap day.
	gapDay := time.Date(2025, time.March, 29, 23, 0, 0, 0, time.UTC)
	next := nextAfter(t, spec, gapDay)

	amsterdam, _ := time.LoadLocation("Europe/Amsterdam")
	nextAms := next.In(amsterdam)

	// Should skip to 02:30 on 2025-03-31 (not the gap day 2025-03-30).
	if nextAms.Day() == 30 {
		t.Errorf("spring-forward: schedule fired at %v, but 02:30 on 2025-03-30 does not exist — should skip to the next valid day", nextAms)
	}
	// Should still be 02:30 local on the next valid day.
	if nextAms.Hour() != 2 || nextAms.Minute() != 30 {
		t.Errorf("spring-forward: next fire = %v, want 02:30 local", nextAms)
	}
}

// TestScheduler_Timezone_FallBackOverlap pins the fall-back repeated-hour
// behaviour for robfig/cron v3. Amsterdam clocks fall back 2025-10-26:
// at 03:00 CEST (01:00 UTC) clocks revert to 02:00 CET, so 02:30 local
// wall-clock time occurs twice — once as 02:30 CEST (00:30 UTC) and again
// as 02:30 CET (01:30 UTC).
//
// robfig/cron orders time internally by UTC instants and fires once per UTC
// instant, so a "30 2 * * *" Europe/Amsterdam schedule fires TWICE on the
// fall-back day: at 00:30 UTC and again at 01:30 UTC, each corresponding to
// a distinct "02:30 local" reading.
func TestScheduler_Timezone_FallBackOverlap(t *testing.T) {
	tz := "Europe/Amsterdam"
	sched := &db.Schedule{ID: 13, CronExpr: "30 2 * * *", Enabled: true, MissedPolicy: "skip", Timezone: &tz}
	spec := prefixedSpecFor(t, sched, time.UTC)

	amsterdam, _ := time.LoadLocation("Europe/Amsterdam")

	// First firing: start just before the fall-back date (00:00 UTC = 02:00 CEST).
	justBefore := time.Date(2025, time.October, 26, 0, 0, 0, 0, time.UTC)
	first := nextAfter(t, spec, justBefore)
	firstAms := first.In(amsterdam)

	// First firing must be 00:30 UTC = 02:30 CEST (summer time, before fall-back).
	if first.UTC().Hour() != 0 || first.UTC().Minute() != 30 {
		t.Errorf("fall-back first fire: expected 00:30 UTC, got %v", first.UTC())
	}
	if firstAms.Day() != 26 || firstAms.Month() != time.October {
		t.Errorf("fall-back first fire: expected 2025-10-26, got %v", firstAms)
	}
	if firstAms.Hour() != 2 || firstAms.Minute() != 30 {
		t.Errorf("fall-back first fire: expected 02:30 local, got %v", firstAms)
	}

	// Second firing: advance one tick past the first and call Next again.
	second := nextAfter(t, spec, first)
	secondAms := second.In(amsterdam)

	// Second firing must be 01:30 UTC = 02:30 CET (winter time, after fall-back).
	if second.UTC().Hour() != 1 || second.UTC().Minute() != 30 {
		t.Errorf("fall-back second fire: expected 01:30 UTC, got %v", second.UTC())
	}
	if secondAms.Day() != 26 || secondAms.Month() != time.October {
		t.Errorf("fall-back second fire: expected 2025-10-26, got %v", secondAms)
	}
	if secondAms.Hour() != 2 || secondAms.Minute() != 30 {
		t.Errorf("fall-back second fire: expected 02:30 local, got %v", secondAms)
	}

	// The two firings are at distinct UTC instants.
	if !second.After(first) {
		t.Errorf("fall-back: expected second fire (%v) to be after first fire (%v)", second.UTC(), first.UTC())
	}
}

// TestScheduler_Timezone_InheritServerDefault asserts that a schedule with nil
// Timezone uses the scheduler's defaultLoc.
func TestScheduler_Timezone_InheritServerDefault(t *testing.T) {
	amsterdam, err := time.LoadLocation("Europe/Amsterdam")
	if err != nil {
		t.Fatalf("LoadLocation: %v", err)
	}

	sched := &db.Schedule{ID: 20, CronExpr: "0 5 * * *", Enabled: true, MissedPolicy: "skip", Timezone: nil}
	store := &fakeStore{enabled: []*db.Schedule{sched}}
	jobs := &fakeJobs{}
	// Server default = Amsterdam.
	s := New(jobs, store, amsterdam)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := s.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer s.Stop()

	// The prefixedSpec should use Amsterdam (not UTC).
	spec := s.prefixedSpec(sched)
	jan := time.Date(2025, time.January, 15, 10, 0, 0, 0, time.UTC)
	next := nextAfter(t, spec, jan)

	nextAms := next.In(amsterdam)
	if nextAms.Hour() != 5 || nextAms.Minute() != 0 {
		t.Errorf("inherit: next fire in Amsterdam = %v, want 05:00", nextAms)
	}
	// UTC+1 in January.
	if next.UTC().Hour() != 4 {
		t.Errorf("inherit: expected 04:00 UTC (Amsterdam default), got %d", next.UTC().Hour())
	}
}

// TestScheduler_Timezone_InheritUTCDefault asserts that nil Timezone + UTC
// default fires at the UTC time literally.
func TestScheduler_Timezone_InheritUTCDefault(t *testing.T) {
	sched := &db.Schedule{ID: 21, CronExpr: "0 5 * * *", Enabled: true, MissedPolicy: "skip", Timezone: nil}
	store := &fakeStore{enabled: []*db.Schedule{sched}}
	jobs := &fakeJobs{}
	s := New(jobs, store, time.UTC)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := s.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer s.Stop()

	spec := s.prefixedSpec(sched)
	jan := time.Date(2025, time.January, 15, 10, 0, 0, 0, time.UTC)
	next := nextAfter(t, spec, jan)

	if next.UTC().Hour() != 5 || next.UTC().Minute() != 0 {
		t.Errorf("UTC default: expected 05:00 UTC, got %v", next.UTC())
	}
}

// TestScheduler_Timezone_PerScheduleOverridesServerDefault asserts that an
// explicit schedule timezone overrides the server default.
func TestScheduler_Timezone_PerScheduleOverridesServerDefault(t *testing.T) {
	tz := "America/New_York"
	sched := &db.Schedule{ID: 22, CronExpr: "0 9 * * *", Enabled: true, MissedPolicy: "skip", Timezone: &tz}
	store := &fakeStore{enabled: []*db.Schedule{sched}}
	jobs := &fakeJobs{}
	// Server default = UTC — the per-schedule "America/New_York" must override it.
	s := New(jobs, store, time.UTC)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := s.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer s.Stop()

	spec := s.prefixedSpec(sched)
	jan := time.Date(2025, time.January, 15, 10, 0, 0, 0, time.UTC)
	next := nextAfter(t, spec, jan)

	ny, _ := time.LoadLocation("America/New_York")
	nextNY := next.In(ny)
	if nextNY.Hour() != 9 || nextNY.Minute() != 0 {
		t.Errorf("per-schedule TZ: next fire in NY = %v, want 09:00", nextNY)
	}
	// NYC in January is UTC-5, so 09:00 NY = 14:00 UTC.
	if next.UTC().Hour() != 14 {
		t.Errorf("per-schedule TZ: expected 14:00 UTC (NY winter), got %d", next.UTC().Hour())
	}
}

// TestScheduler_MissedRun_TimezoneAware asserts that dispatchMissedIfDue
// correctly computes the next-fire time in the schedule's effective timezone.
// A daily-at-06:00-Amsterdam schedule whose last run was 26h ago (i.e. more
// than one day back) must be considered missed and trigger a catch-up dispatch.
func TestScheduler_MissedRun_TimezoneAware(t *testing.T) {
	tz := "Europe/Amsterdam"
	// Last run was 26 hours ago.
	lastStart := time.Now().UTC().Add(-26 * time.Hour)
	store := &fakeStore{
		enabled: []*db.Schedule{
			{ID: 30, CronExpr: "0 6 * * *", Enabled: true, MissedPolicy: "run_once", Timezone: &tz},
		},
		lastSucc: map[int64]*db.ScheduleRun{
			30: {ID: 50, ScheduleID: 30, StartedAt: lastStart, Status: "succeeded"},
		},
	}
	jobs := &fakeJobs{}
	s := New(jobs, store, time.UTC)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := s.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	// The missed-run dispatch is async; give it time to land.
	waitForCallCount(t, jobs, 1, 2*time.Second)
	s.Stop()
}

// --- helpers ---

// nextAfter parses a prefixed CRON_TZ=... spec using the same production parser
// and returns the next fire time after `from`.
func nextAfter(t *testing.T, spec string, from time.Time) time.Time {
	t.Helper()
	schedule, err := schedulespec.ProductionParser.Parse(spec)
	if err != nil {
		t.Fatalf("parse spec %q: %v", spec, err)
	}
	return schedule.Next(from)
}

// prefixedSpecFor builds a prefixed spec for sched using the given server default.
func prefixedSpecFor(t *testing.T, sched *db.Schedule, defaultLoc *time.Location) string {
	t.Helper()
	s := New(nil, nil, defaultLoc)
	return s.prefixedSpec(sched)
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
