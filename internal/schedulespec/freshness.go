package schedulespec

import "time"

// staleMargin is the grace added to a schedule's next expected fire before it
// is treated as stale. It covers clock jitter and the scheduler's start delay.
// The schedule's own timeout is deliberately NOT added here: a legitimately
// long run is covered by the running-status check in IsStale, and folding the
// timeout into the grace would only delay alerts (a daily schedule with a 24h
// timeout would otherwise flag stale at T+48h instead of T+24h).
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
}

// IsStale reports whether a schedule's data is overdue. loc is the location to
// evaluate the cron in (the caller applies the per-schedule zone or the server
// default). now is injected for testability.
func IsStale(f Freshness, loc *time.Location, now time.Time) bool {
	if !f.Enabled {
		return false
	}
	// A run legitimately in progress within its timeout is not stale. The
	// timeout bounds how long a "running" row is trusted; beyond it the run is
	// a zombie and staleness applies on its own merits.
	if f.LastRunStatus == "running" && f.LastRunAt != nil &&
		now.Sub(*f.LastRunAt) < time.Duration(f.TimeoutSeconds)*time.Second {
		return false
	}
	// Anchor on the last success (finished) if it ever succeeded, else creation.
	anchor := f.CreatedAt
	if f.LastSuccessAt != nil {
		anchor = *f.LastSuccessAt
	}
	next, err := NextFire(f.CronExpr, loc, anchor)
	if err != nil {
		// Stored crons are gated by Validate, so this should not happen; treat
		// an unparseable expression as not-stale rather than crash a health path.
		return false
	}
	return next.Add(staleMargin).Before(now)
}
