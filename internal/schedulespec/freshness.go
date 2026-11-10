package schedulespec

import (
	"time"

	"github.com/robfig/cron/v3"
)

// staleMargin is the grace added to a schedule's next expected fire before it
// is treated as stale. It covers clock jitter and the scheduler's start delay.
// The schedule's own timeout is deliberately NOT added here: a legitimately
// long run is reported separately by IsRefreshing; folding the timeout into
// freshness would make process activity look like successful data delivery.
const staleMargin = 10 * time.Minute

// Freshness is the policy-package view of a schedule's run history, mapped from
// db.ScheduleFreshness by the caller. schedulespec stays storage-free (no db
// import). The caller resolves the per-schedule timezone and passes the
// resulting *time.Location to IsStale.
type Freshness struct {
	Enabled        bool
	CronExpr       string
	CreatedAt      time.Time
	TimeoutSeconds int
	LastRunStatus  string     // "" if never run
	LastRunAt      *time.Time // started_at of the most recent run, nil if never
	LastSuccessAt  *time.Time // finished_at of the most recent succeeded run, nil if never
	ActiveRunAt    *time.Time // started_at of the newest running run, even if a later overlap row is terminal
}

// EvaluateStale reports whether a schedule's data is overdue and preserves an
// invalid cron as an error. Strict callers must never turn an unreadable
// schedule into "fresh" merely because its next fire could not be computed.
func EvaluateStale(f Freshness, loc *time.Location, now time.Time) (bool, error) {
	evaluation, err := EvaluateFreshness(f, loc, now)
	return evaluation.Stale, err
}

// FreshnessEvaluation is the shared API and metrics view of a schedule's data.
// Disabled schedules have no deadline. NextRun is a nominal cron fire, not a
// dispatch guarantee; it is absent for @every intervals, whose real next fire
// depends on scheduler registration rather than wall-clock cron boundaries.
type FreshnessEvaluation struct {
	Stale            bool
	StalenessSeconds float64
	ExpectedSuccess  time.Time
	NextRun          time.Time
}

// EvaluateFreshness computes the first unmet deadline using one parsed schedule
// and one clock instant. Failures and active runs never advance that deadline.
// Invalid or impossible cron expressions return an error, never a fresh result.
func EvaluateFreshness(f Freshness, loc *time.Location, now time.Time) (FreshnessEvaluation, error) {
	if !f.Enabled {
		return FreshnessEvaluation{}, nil
	}
	sched, err := parseInLocation(f.CronExpr, loc)
	if err != nil {
		return FreshnessEvaluation{}, err
	}
	anchor := f.CreatedAt
	if f.LastSuccessAt != nil {
		anchor = *f.LastSuccessAt
	}
	next, err := nextAfter(sched, anchor)
	if err != nil {
		return FreshnessEvaluation{}, err
	}
	deadline := next.Add(staleMargin)
	result := FreshnessEvaluation{
		ExpectedSuccess:  deadline,
		Stale:            deadline.Before(now),
		StalenessSeconds: max(0, now.Sub(deadline).Seconds()),
	}
	if _, interval := sched.(cron.ConstantDelaySchedule); !interval {
		result.NextRun, err = nextAfter(sched, now)
		if err != nil {
			return FreshnessEvaluation{}, err
		}
	}
	return result, nil
}

// IsStale is the conservative compatibility predicate for callers that cannot
// represent an unknown state. New health surfaces use EvaluateStale so errors
// remain distinguishable from overdue data.
func IsStale(f Freshness, loc *time.Location, now time.Time) bool {
	stale, err := EvaluateStale(f, loc, now)
	return stale || err != nil
}

// IsRefreshing reports a live, non-zombie run independently of data freshness.
// A stale schedule can therefore be both stale and refreshing until the active
// run succeeds; callers never have to infer data delivery from process activity.
func IsRefreshing(f Freshness, now time.Time) bool {
	activeAt := f.ActiveRunAt
	if activeAt == nil && f.LastRunStatus == "running" {
		activeAt = f.LastRunAt
	}
	if !f.Enabled || activeAt == nil || f.TimeoutSeconds <= 0 {
		return false
	}
	age := now.Sub(*activeAt)
	return age < time.Duration(f.TimeoutSeconds)*time.Second
}
