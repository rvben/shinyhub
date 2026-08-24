package schedulespec

import "time"

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
	if !f.Enabled {
		return false, nil
	}
	// Anchor on the last success (finished) if it ever succeeded, else creation.
	anchor := f.CreatedAt
	if f.LastSuccessAt != nil {
		anchor = *f.LastSuccessAt
	}
	next, err := NextFire(f.CronExpr, loc, anchor)
	if err != nil {
		return false, err
	}
	return next.Add(staleMargin).Before(now), nil
}

// IsStale is the compatibility predicate for callers that cannot represent an
// unknown state. New health and convergence surfaces use EvaluateStale.
func IsStale(f Freshness, loc *time.Location, now time.Time) bool {
	stale, _ := EvaluateStale(f, loc, now)
	return stale
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
