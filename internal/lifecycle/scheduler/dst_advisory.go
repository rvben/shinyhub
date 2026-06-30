package scheduler

import (
	"fmt"
	"time"

	"github.com/robfig/cron/v3"
	"github.com/rvben/shinyhub/internal/schedulespec"
)

// dstScanWindow bounds how far ahead DSTAdvisory looks for a transition. A DST
// zone has a fall-back transition at most once a year, so 13 months guarantees
// we observe the next one regardless of when the schedule is created.
const dstScanWindow = 13 * 30 * 24 * time.Hour

// DSTAdvisory returns a human-readable warning when a fixed-hour schedule will
// fire twice on a daylight-saving fall-back day, or "" when it is safe.
//
// The fall-back transition rewinds the wall clock, so a wall-clock time inside
// the repeated hour occurs at two distinct instants and the job runs twice. The
// repeated hour is zone-specific, so the check is exact: it walks the same
// CRON_TZ-prefixed expression the scheduler fires and reports the first day on
// which any local wall-clock time recurs. Duplicated times may be interleaved
// with other fires (e.g. "0,30 2 * * *" fires 02:00, 02:30, 02:00, 02:30 across
// the rewind), so detection tracks every fire in a seen-set rather than
// comparing only adjacent fires.
//
// Hourly (or finer) schedules are intentionally silent: they already fire in
// every hour of the day, so the extra fall-back repeat is expected rather than
// a footgun. "Every hour" is judged by the set of hours the schedule actually
// fires on a transition-free day, which correctly classifies wildcard, range,
// and step hour fields (e.g. "0-23" is hourly even though it has no "*").
func DSTAdvisory(cronExpr string, loc *time.Location, ref time.Time) string {
	if loc == nil || loc == time.UTC || loc.String() == "UTC" {
		return ""
	}
	spec := "CRON_TZ=" + loc.String() + " " + cronExpr
	schedule, err := schedulespec.ProductionParser.Parse(spec)
	if err != nil {
		return ""
	}
	if firesEveryHour(schedule, loc, ref) {
		return ""
	}

	type wallKey struct{ year, yearDay, hour, minute int }
	end := ref.Add(dstScanWindow)
	seen := make(map[wallKey]struct{})
	for t := schedule.Next(ref); !t.IsZero() && t.Before(end); t = schedule.Next(t) {
		lt := t.In(loc)
		k := wallKey{lt.Year(), lt.YearDay(), lt.Hour(), lt.Minute()}
		if _, dup := seen[k]; dup {
			return fmt.Sprintf(
				"Schedule fires twice on %s: %s observes daylight saving time and this wall-clock time recurs when clocks fall back. Use UTC or a time outside the transition hour to fire once.",
				lt.Format("2006-01-02"), loc.String())
		}
		seen[k] = struct{}{}
	}
	return ""
}

// firesEveryHour reports whether the schedule fires in all 24 hours of a normal
// (transition-free) day. Such schedules are hourly-or-finer and the fall-back
// repeat is expected, so they get no advisory. Counting actual fires handles
// wildcards, ranges ("0-23"), and steps uniformly without parsing the field.
func firesEveryHour(schedule cron.Schedule, loc *time.Location, ref time.Time) bool {
	day := transitionFreeDay(loc, ref)
	dayEnd := day.Add(24 * time.Hour)
	hours := make(map[int]struct{})
	for t := schedule.Next(day.Add(-time.Second)); !t.IsZero() && t.Before(dayEnd); t = schedule.Next(t) {
		if t.Before(day) {
			continue
		}
		hours[t.In(loc).Hour()] = struct{}{}
	}
	return len(hours) == 24
}

// transitionFreeDay returns local midnight of the first day after ref whose UTC
// offset is constant from start to end, so hour counting on it is not skewed by
// a DST transition landing mid-day.
func transitionFreeDay(loc *time.Location, ref time.Time) time.Time {
	r := ref.In(loc)
	day := time.Date(r.Year(), r.Month(), r.Day(), 0, 0, 0, 0, loc).AddDate(0, 0, 1)
	for range 400 {
		_, offStart := day.Zone()
		_, offEnd := day.Add(24*time.Hour - time.Second).Zone()
		if offStart == offEnd {
			return day
		}
		day = day.AddDate(0, 0, 1)
	}
	return day
}
