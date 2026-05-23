package scheduler

import (
	"fmt"
	"strings"
	"time"
)

// dstScanWindow bounds how far ahead DSTAdvisory looks for a transition. A DST
// zone has a fall-back transition at most once a year, so 13 months guarantees
// we observe the next one regardless of when the schedule is created.
const dstScanWindow = 13 * 30 * 24 * time.Hour

// DSTAdvisory returns a human-readable warning when a fixed-hour schedule will
// fire twice on a daylight-saving fall-back day, or "" when it is safe.
//
// The fall-back transition rewinds the wall clock, so a pinned wall-clock time
// inside the repeated hour occurs at two distinct instants and the job runs
// twice. The repeated hour is zone-specific, so the check is exact: it walks
// the same CRON_TZ-prefixed expression the scheduler fires and reports a
// duplicate local wall-clock reading between consecutive fires.
//
// Hourly (or finer) schedules are intentionally silent: they already fire many
// times a day, so the extra fall-back repeat is expected rather than a footgun.
func DSTAdvisory(cronExpr string, loc *time.Location, ref time.Time) string {
	if loc == nil || loc == time.UTC || loc.String() == "UTC" {
		return ""
	}
	if !hourFieldIsPinned(cronExpr) {
		return ""
	}
	spec := "CRON_TZ=" + loc.String() + " " + cronExpr
	schedule, err := productionParser().Parse(spec)
	if err != nil {
		return ""
	}

	end := ref.Add(dstScanWindow)
	prev := schedule.Next(ref)
	for !prev.IsZero() && prev.Before(end) {
		next := schedule.Next(prev)
		if next.IsZero() {
			break
		}
		p := prev.In(loc)
		n := next.In(loc)
		if sameLocalWallClock(p, n) {
			return fmt.Sprintf(
				"Schedule fires twice on %s: %s observes daylight saving time and this wall-clock time recurs when clocks fall back. Use UTC or a time outside the transition hour to fire once.",
				p.Format("2006-01-02"), loc.String())
		}
		prev = next
	}
	return ""
}

// sameLocalWallClock reports whether two instants render to the same local
// calendar date and minute, which on a fall-back day means the schedule fired
// twice for one wall-clock time.
func sameLocalWallClock(a, b time.Time) bool {
	return a.Year() == b.Year() && a.YearDay() == b.YearDay() &&
		a.Hour() == b.Hour() && a.Minute() == b.Minute()
}

// hourFieldIsPinned reports whether the cron expression targets specific
// hour(s) rather than every hour. A wildcard hour ("*", "*/2", ...) means the
// job is hourly-or-finer and the fall-back repeat is expected, so no advisory.
func hourFieldIsPinned(cronExpr string) bool {
	fields := strings.Fields(cronExpr)
	// Standard 5-field cron puts the hour in field index 1; the scheduler's
	// parser also accepts an optional leading seconds field (6 fields), which
	// shifts the hour to index 2.
	var hour string
	switch len(fields) {
	case 5:
		hour = fields[1]
	case 6:
		hour = fields[2]
	default:
		return false
	}
	return !strings.Contains(hour, "*")
}
