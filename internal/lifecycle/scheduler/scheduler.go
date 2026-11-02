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
	"github.com/rvben/shinyhub/internal/jobs"
	"github.com/rvben/shinyhub/internal/schedulespec"
)

// Trigger values recorded on every schedule run, naming what caused it. The
// column is free-form TEXT, but these are the only values any writer produces,
// so a run's provenance is answerable from the run record alone.
const (
	// TriggerSchedule is a run started by a cron boundary arriving while the
	// server was up.
	TriggerSchedule = "schedule"
	// TriggerMissed is a catch-up run dispatched at startup by the missed-run
	// policy (missed = "run_once") because one or more boundaries passed while
	// the server was down. It is deliberately distinct from TriggerSchedule: a
	// catch-up corresponds to no cron boundary, and conflating the two makes
	// "did the policy backfill the outage?" unanswerable from run history.
	TriggerMissed = "missed"
	// TriggerDeploy is dispatched by a schedule's deploy_trigger, including
	// restart recovery for an interrupted deploy run.
	TriggerDeploy = "deploy"
	// TriggerManual is a run started by an operator through the API or CLI.
	TriggerManual = "manual"
)

// Jobs is the narrow interface scheduler needs from the jobs package, satisfied
// by *jobs.Manager. Kept here to avoid an import cycle in tests.
type Jobs interface {
	Run(scheduleID int64, trigger string, userID *int64) (int64, error)
	RunDeployObligation(obligation *db.ScheduleDeployObligation) (int64, error)
}

// Store is the narrow interface scheduler needs from db. Satisfied by *db.Store.
type Store interface {
	ListEnabledSchedules() ([]*db.Schedule, error)
	GetSchedule(id int64) (*db.Schedule, error)
	GetAppByID(id int64) (*db.App, error)
	LastSuccessfulRun(id int64) (*db.ScheduleRun, error)
	RecoverDeployObligations() error
	ReconcileAllDeployObligations() error
	ClaimNextDeployObligation() (*db.ScheduleDeployObligation, error)
	ReleaseDeployObligation(id int64, cause error) error
}

// Metrics receives per-schedule series from the scheduler. Satisfied by
// *metrics.Registry; a local interface keeps this package free of a metrics
// import.
type Metrics interface {
	// SeedSchedule publishes a registered schedule's alertable counter series
	// at zero, so the first failure is an increase from 0 rather than a
	// series that appears already at 1.
	SeedSchedule(slug, schedule string)
	// RecordScheduleDispatchFailure counts a cron or catch-up dispatch that
	// was rejected before a run row existed.
	RecordScheduleDispatchFailure(slug, schedule, reason string)
}

// Dispatch rejection reasons, the bounded values of the reason label on
// shinyhub_schedule_dispatch_failures_total.
const (
	DispatchRejectAppDeploying = "app_deploying"
	DispatchRejectQuarantined  = "quarantined"
	DispatchRejectError        = "error"
)

// DispatchRejectReasons lists every reason label value, for seeding.
var DispatchRejectReasons = []string{DispatchRejectAppDeploying, DispatchRejectQuarantined, DispatchRejectError}

// ErrNotStarted is returned by Reload when the scheduler's cron has not
// been started yet (Start has not been called or the scheduler is between
// stop/start cycles). Callers driven by deploy hooks treat this as a
// soft failure: the persisted row will be picked up on the next Start.
var ErrNotStarted = errors.New("scheduler not started")

// Scheduler wraps robfig/cron with a per-schedule entry registry that supports
// hot reload (post-CRUD) and missed-run catch-up at startup.
type Scheduler struct {
	jobs       Jobs
	store      Store
	metrics    Metrics
	defaultLoc *time.Location // server-level default; never nil

	mu      sync.Mutex
	cron    *cron.Cron
	entries map[int64]cron.EntryID
	started bool
}

// New creates a Scheduler. defaultLoc is the server-level timezone fallback
// applied to any schedule whose own Timezone is nil. Pass time.UTC for the
// standard "fire at UTC wall-clock time" behaviour. A nil defaultLoc defaults
// to time.UTC defensively.
func New(jobs Jobs, store Store, defaultLoc *time.Location) *Scheduler {
	if defaultLoc == nil {
		defaultLoc = time.UTC
	}
	return &Scheduler{
		jobs:       jobs,
		store:      store,
		defaultLoc: defaultLoc,
		entries:    map[int64]cron.EntryID{},
	}
}

// SetMetrics wires per-schedule series. Nil-safe when unset. Must be called
// before Start.
func (s *Scheduler) SetMetrics(m Metrics) { s.metrics = m }

// Start initialises the scheduler. It blocks only briefly during initial load —
// the cron loop runs in a separate goroutine. Stop the scheduler by cancelling
// ctx OR calling Stop(); either is sufficient.
func (s *Scheduler) Start(ctx context.Context) error {
	s.mu.Lock()
	if s.started {
		s.mu.Unlock()
		return errors.New("scheduler already started")
	}
	// Standard 5-field cron expressions ("min hour dom mon dow"). Matches the
	// API/UI validation surface; the seconds field is intentionally not exposed.
	s.cron = cron.New()
	s.started = true
	s.mu.Unlock()

	// Owner startup terminalizes inherited running rows while holding every
	// publication recovery fence. Doing it here would race API admissions opened
	// before Scheduler.Start and could interrupt a brand-new live producer.
	if err := s.store.RecoverDeployObligations(); err != nil {
		s.Stop()
		return fmt.Errorf("recover deploy obligations: %w", err)
	}

	// Load enabled schedules.
	enabled, err := s.store.ListEnabledSchedules()
	if err != nil {
		s.Stop()
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

	// 2b. Recover deploy-triggered runs interrupted by a prior restart.
	s.reconcileDeployRuns()

	// 3. Start cron loop and stop it when ctx is cancelled.
	s.cron.Start()
	go func() {
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				s.Stop()
				return
			case <-ticker.C:
				s.reconcileDeployRuns()
			}
		}
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

// prefixedSpec returns the CRON_TZ=<zone> prefixed cron expression for sched,
// resolving the effective timezone via sched.EffectiveLocation(s.defaultLoc).
// This is the single source of truth for timezone resolution; both register
// and dispatchMissedIfDue must call it to guarantee the two paths cannot drift.
func (s *Scheduler) prefixedSpec(sched *db.Schedule) string {
	loc := sched.EffectiveLocation(s.defaultLoc)
	return "CRON_TZ=" + loc.String() + " " + sched.CronExpr
}

func (s *Scheduler) register(sched *db.Schedule) error {
	s.mu.Lock()
	c := s.cron
	s.mu.Unlock()
	if c == nil {
		return ErrNotStarted
	}
	spec := s.prefixedSpec(sched)
	app, err := s.store.GetAppByID(sched.AppID)
	if err != nil {
		return fmt.Errorf("get app %d: %w", sched.AppID, err)
	}
	// A rename or app change reaches here through Reload, so the identity
	// captured now is current for every dispatch of this entry.
	target := dispatchTarget{scheduleID: sched.ID, name: sched.Name, slug: app.Slug}
	id, err := c.AddFunc(spec, func() { s.dispatch(target, TriggerSchedule) })
	if err != nil {
		return fmt.Errorf("parse cron %q: %w", spec, err)
	}
	s.mu.Lock()
	s.entries[sched.ID] = id
	s.mu.Unlock()
	if s.metrics != nil {
		s.metrics.SeedSchedule(target.slug, target.name)
	}
	return nil
}

// dispatchTarget names the schedule a cron entry dispatches, so a rejected
// dispatch is reported with the same identity as the runs it would have made.
type dispatchTarget struct {
	scheduleID int64
	name       string
	slug       string
}

// dispatch starts one cron or catch-up run. A rejection here leaves no run
// row, audit event or run counter behind, so it is logged by name and counted
// by reason; otherwise a schedule that never runs looks like one that is idle.
func (s *Scheduler) dispatch(t dispatchTarget, trigger string) {
	_, err := s.jobs.Run(t.scheduleID, trigger, nil)
	if err == nil {
		return
	}
	attrs := []any{
		"app", t.slug, "schedule", t.name, "schedule_id", t.scheduleID,
		"trigger", trigger, "err", err,
	}
	if errors.Is(err, jobs.ErrManagerStopped) {
		// Shutdown, not a missed run: the next owner's missed-run policy
		// decides whether this boundary is caught up.
		slog.Info("schedule dispatch skipped: jobs manager stopped", attrs...)
		return
	}
	reason := DispatchRejectError
	switch {
	case errors.Is(err, jobs.ErrAppDeploying):
		reason = DispatchRejectAppDeploying
	case errors.Is(err, jobs.ErrAppCompatibilityQuarantined):
		reason = DispatchRejectQuarantined
	}
	slog.Warn("schedule_dispatch_rejected", append(attrs, "reason", reason)...)
	if s.metrics != nil {
		s.metrics.RecordScheduleDispatchFailure(t.slug, t.name, reason)
	}
}

// dispatchMissedIfDue runs the schedule once immediately if more than one cron
// interval has passed since the last successful run. The schedule's effective
// timezone is applied via the prefixed spec so missed-run detection uses
// local-calendar semantics (same as the registration path).
func (s *Scheduler) dispatchMissedIfDue(sched *db.Schedule) {
	last, err := s.store.LastSuccessfulRun(sched.ID)
	if err != nil {
		// Never run successfully; treat first registration as the baseline.
		return
	}
	spec := s.prefixedSpec(sched)
	parser := schedulespec.ProductionParser
	schedule, err := parser.Parse(spec)
	if err != nil {
		slog.Warn("parse cron for missed-run check", "schedule", sched.ID, "err", err)
		return
	}
	next := schedule.Next(last.StartedAt)
	if next.Before(time.Now()) {
		app, err := s.store.GetAppByID(sched.AppID)
		if err != nil {
			slog.Warn("missed-run dispatch: get app", "schedule", sched.Name, "schedule_id", sched.ID, "err", err)
			return
		}
		go s.dispatch(dispatchTarget{scheduleID: sched.ID, name: sched.Name, slug: app.Slug}, TriggerMissed)
	}
}

// reconcileDeployRuns materializes current desired state and drains its durable
// outbox. It repairs interrupted admission and overlap waits, while failed
// producer executions remain failed until an explicit retry or new identity.
func (s *Scheduler) reconcileDeployRuns() {
	if err := s.store.ReconcileAllDeployObligations(); err != nil {
		slog.Warn("reconcile deploy obligations: query failed", "err", err)
		return
	}
	for attempts := 0; attempts < 256; attempts++ {
		obligation, err := s.store.ClaimNextDeployObligation()
		if errors.Is(err, db.ErrNotFound) {
			return
		}
		if err != nil {
			slog.Warn("claim deploy obligation failed", "err", err)
			return
		}
		if s.jobs == nil {
			err := errors.New("jobs manager unavailable")
			if releaseErr := s.store.ReleaseDeployObligation(obligation.ID, err); releaseErr != nil {
				slog.Error("release deploy obligation without jobs manager", "obligation", obligation.ID, "err", releaseErr)
			}
			slog.Warn("deploy obligation remains pending: jobs manager unavailable", "schedule", obligation.ScheduleID)
			continue
		}
		if _, err := s.jobs.RunDeployObligation(obligation); err != nil {
			if releaseErr := s.store.ReleaseDeployObligation(obligation.ID, err); releaseErr != nil {
				slog.Error("release failed deploy obligation claim", "obligation", obligation.ID, "err", releaseErr)
			}
			slog.Warn("reconcile deploy obligation dispatch failed", "schedule", obligation.ScheduleID, "err", err)
			continue
		}
	}
	slog.Warn("deploy obligation reconciliation stopped at bounded drain limit", "limit", 256)
}
