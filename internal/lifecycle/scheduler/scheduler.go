package scheduler

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/robfig/cron/v3"
	"github.com/rvben/shinyhub/internal/db"
)

// Jobs is the narrow interface scheduler needs from the jobs package, satisfied
// by *jobs.Manager. Kept here to avoid an import cycle in tests.
type Jobs interface {
	Run(scheduleID int64, trigger string, userID *int64) (int64, error)
}

// Store is the narrow interface scheduler needs from db. Satisfied by *db.Store.
type Store interface {
	ListEnabledSchedules() ([]*db.Schedule, error)
	GetSchedule(id int64) (*db.Schedule, error)
	MarkRunningSchedulesInterrupted() (int64, error)
	LastSuccessfulRun(id int64) (*db.ScheduleRun, error)
}

// Scheduler wraps robfig/cron with a per-schedule entry registry that supports
// hot reload (post-CRUD) and missed-run catch-up at startup.
type Scheduler struct {
	jobs  Jobs
	store Store

	mu      sync.Mutex
	cron    *cron.Cron
	entries map[int64]cron.EntryID
	started bool
}

func New(jobs Jobs, store Store) *Scheduler {
	return &Scheduler{
		jobs:    jobs,
		store:   store,
		entries: map[int64]cron.EntryID{},
	}
}

// Start initialises the scheduler. It blocks only briefly during initial load —
// the cron loop runs in a separate goroutine. Stop the scheduler by cancelling
// ctx OR calling Stop(); either is sufficient.
func (s *Scheduler) Start(ctx context.Context) error {
	s.mu.Lock()
	if s.started {
		s.mu.Unlock()
		return errors.New("scheduler already started")
	}
	s.cron = cron.New(cron.WithSeconds())
	s.started = true
	s.mu.Unlock()

	// 1. Mark interrupted runs from a previous server life.
	if _, err := s.store.MarkRunningSchedulesInterrupted(); err != nil {
		slog.Warn("mark interrupted schedules", "err", err)
	}

	// 2. Load enabled schedules.
	enabled, err := s.store.ListEnabledSchedules()
	if err != nil {
		return fmt.Errorf("list enabled schedules: %w", err)
	}
	for _, sched := range enabled {
		if err := s.register(sched); err != nil {
			slog.Warn("register schedule", "schedule", sched.ID, "err", err)
			continue
		}
		if sched.MissedPolicy == "run_once" {
			s.dispatchMissedIfDue(sched)
		}
	}

	// 3. Start cron loop and stop it when ctx is cancelled.
	s.cron.Start()
	go func() {
		<-ctx.Done()
		s.Stop()
	}()
	return nil
}

// Stop cancels the cron loop, waiting for in-flight callbacks to return.
func (s *Scheduler) Stop() {
	s.mu.Lock()
	c := s.cron
	s.cron = nil
	s.started = false
	s.entries = map[int64]cron.EntryID{}
	s.mu.Unlock()
	if c == nil {
		return
	}
	<-c.Stop().Done()
}

// Reload re-registers a schedule (post-create or post-update). If the schedule
// is no longer enabled, the entry is removed.
func (s *Scheduler) Reload(scheduleID int64) error {
	sched, err := s.store.GetSchedule(scheduleID)
	if err != nil {
		return s.Remove(scheduleID)
	}
	s.Remove(scheduleID)
	if !sched.Enabled {
		return nil
	}
	return s.register(sched)
}

// Remove unregisters a schedule's cron entry. No-op if absent.
func (s *Scheduler) Remove(scheduleID int64) error {
	s.mu.Lock()
	id, ok := s.entries[scheduleID]
	delete(s.entries, scheduleID)
	c := s.cron
	s.mu.Unlock()
	if ok && c != nil {
		c.Remove(id)
	}
	return nil
}

// NextFire returns the next scheduled fire time for a schedule.
func (s *Scheduler) NextFire(scheduleID int64) (time.Time, error) {
	s.mu.Lock()
	id, ok := s.entries[scheduleID]
	c := s.cron
	s.mu.Unlock()
	if !ok || c == nil {
		return time.Time{}, fmt.Errorf("schedule %d not registered", scheduleID)
	}
	return c.Entry(id).Next, nil
}

// entryCount is exposed for tests.
func (s *Scheduler) entryCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.entries)
}

func (s *Scheduler) register(sched *db.Schedule) error {
	s.mu.Lock()
	c := s.cron
	s.mu.Unlock()
	if c == nil {
		return errors.New("scheduler not started")
	}
	schedID := sched.ID
	id, err := c.AddFunc(sched.CronExpr, func() {
		if _, err := s.jobs.Run(schedID, "schedule", nil); err != nil {
			slog.Warn("scheduled run failed", "schedule", schedID, "err", err)
		}
	})
	if err != nil {
		return fmt.Errorf("parse cron %q: %w", sched.CronExpr, err)
	}
	s.mu.Lock()
	s.entries[sched.ID] = id
	s.mu.Unlock()
	return nil
}

// dispatchMissedIfDue runs the schedule once immediately if more than one cron
// interval has passed since the last successful run.
func (s *Scheduler) dispatchMissedIfDue(sched *db.Schedule) {
	last, err := s.store.LastSuccessfulRun(sched.ID)
	if err != nil {
		// Never run successfully; treat first registration as the baseline.
		return
	}
	parser := cron.NewParser(cron.SecondOptional | cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow | cron.Descriptor)
	schedule, err := parser.Parse(sched.CronExpr)
	if err != nil {
		slog.Warn("parse cron for missed-run check", "schedule", sched.ID, "err", err)
		return
	}
	next := schedule.Next(last.StartedAt)
	if next.Before(time.Now()) {
		go func(id int64) {
			if _, err := s.jobs.Run(id, "schedule", nil); err != nil {
				slog.Warn("missed-run dispatch failed", "schedule", id, "err", err)
			}
		}(sched.ID)
	}
}
