package main

import (
	"testing"
	"time"

	"github.com/rvben/shinyhub/internal/db"
)

func TestScheduleMetricsFreshnessPolicy(t *testing.T) {
	at := func(day, hour, minute int) time.Time { return time.Date(2026, 11, day, hour, minute, 0, 0, time.UTC) }
	now := at(10, 12, 0)
	lastSuccess := at(8, 6, 0)
	runningAt := at(10, 11, 55)
	invalidZone := "Not/A_Timezone"
	for _, tc := range []struct {
		name         string
		row          db.ScheduleFreshness
		known, stale bool
		deadline     time.Time
	}{
		{"never succeeded overdue", db.ScheduleFreshness{Enabled: true, CronExpr: "0 6 * * *", CreatedAt: at(8, 0, 0)}, true, true, at(8, 6, 10)},
		{"never succeeded before first fire", db.ScheduleFreshness{Enabled: true, CronExpr: "0 6 * * *", CreatedAt: at(10, 10, 0)}, true, false, at(11, 6, 10)},
		{"successful history", db.ScheduleFreshness{Enabled: true, CronExpr: "0 6 * * *", CreatedAt: at(1, 0, 0), LastSuccessAt: &lastSuccess}, true, true, at(9, 6, 10)},
		{"running does not erase overdue data", db.ScheduleFreshness{Enabled: true, CronExpr: "0 6 * * *", CreatedAt: at(1, 0, 0), LastSuccessAt: &lastSuccess, ActiveRunAt: &runningAt, TimeoutSeconds: 3600}, true, true, at(9, 6, 10)},
		{"disabled", db.ScheduleFreshness{CronExpr: "invalid", Timezone: &invalidZone}, true, false, time.Time{}},
		{"invalid cron", db.ScheduleFreshness{Enabled: true, CronExpr: "invalid"}, false, false, time.Time{}},
		{"invalid timezone", db.ScheduleFreshness{Enabled: true, CronExpr: "0 6 * * *", Timezone: &invalidZone}, false, false, time.Time{}},
		{"impossible cron", db.ScheduleFreshness{Enabled: true, CronExpr: "0 6 30 2 *", CreatedAt: at(1, 0, 0)}, false, false, time.Time{}},
		{"weekly", db.ScheduleFreshness{Enabled: true, CronExpr: "0 6 * * 1", CreatedAt: at(1, 0, 0)}, true, true, at(2, 6, 10)},
		{"monthly", db.ScheduleFreshness{Enabled: true, CronExpr: "0 6 1 * *", CreatedAt: at(1, 0, 0)}, true, true, at(1, 6, 10)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := scheduleSample(tc.row, time.UTC, now)
			if s.FreshnessKnown != tc.known || s.Stale != tc.stale {
				t.Fatalf("sample=%+v", s)
			}
			if !tc.known || !tc.row.Enabled {
				if s.ExpectedSuccessUnix != 0 || s.NextRunUnix != 0 || s.StalenessSeconds != 0 {
					t.Fatalf("unknown/disabled timing=%+v", s)
				}
				return
			}
			if s.ExpectedSuccessUnix != tc.deadline.Unix() || s.StalenessSeconds != max(0, now.Sub(tc.deadline).Seconds()) || s.NextRunUnix <= now.Unix() {
				t.Fatalf("timing=%+v deadline=%s", s, tc.deadline)
			}

		})
	}
}

func TestScheduleMetricsTimezoneAndDST(t *testing.T) {
	amsterdam, err := time.LoadLocation("Europe/Amsterdam")
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ name, cron, anchor, now, deadline, next string }{
		{"spring forward", "0 6 * * *", "2026-03-28T05:00:00Z", "2026-03-29T05:00:00Z", "2026-03-29T04:10:00Z", "2026-03-30T04:00:00Z"},
		{"fall back", "0 6 * * *", "2026-10-24T04:00:00Z", "2026-10-25T06:00:00Z", "2026-10-25T05:10:00Z", "2026-10-26T05:00:00Z"},
		{"spring nonexistent local fire", "30 2 * * *", "2026-03-28T01:30:00Z", "2026-03-29T12:00:00Z", "2026-03-30T00:40:00Z", "2026-03-30T00:30:00Z"},
		{"fall repeated local fire", "30 2 * * *", "2026-10-24T00:30:00Z", "2026-10-25T00:45:00Z", "2026-10-25T00:40:00Z", "2026-10-25T01:30:00Z"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			parse := func(s string) time.Time {
				at, e := time.Parse(time.RFC3339, s)
				if e != nil {
					t.Fatal(e)
				}
				return at
			}
			anchor, now := parse(tc.anchor), parse(tc.now)
			row := db.ScheduleFreshness{Enabled: true, CronExpr: tc.cron, LastSuccessAt: &anchor}
			inherited := scheduleSample(row, amsterdam, now)
			tz := "Europe/Amsterdam"
			row.Timezone = &tz
			override := scheduleSample(row, time.UTC, now)
			if inherited != override {
				t.Fatalf("timezone inheritance differs: %+v != %+v", inherited, override)
			}
			if !inherited.FreshnessKnown || inherited.Stale != parse(tc.deadline).Before(now) || inherited.ExpectedSuccessUnix != parse(tc.deadline).Unix() || inherited.NextRunUnix != parse(tc.next).Unix() {
				t.Fatalf("DST timing=%+v", inherited)
			}
		})
	}
}

func TestScheduleMetricsDeadlineBoundaryAndRecovery(t *testing.T) {
	created := time.Date(2026, 11, 10, 4, 0, 0, 0, time.UTC)
	deadline := time.Date(2026, 11, 10, 6, 10, 0, 0, time.UTC)
	row := db.ScheduleFreshness{Enabled: true, CronExpr: "0 6 * * *", CreatedAt: created}
	atDeadline := scheduleSample(row, time.UTC, deadline)
	if atDeadline.Stale || atDeadline.StalenessSeconds != 0 {
		t.Fatalf("boundary=%+v", atDeadline)
	}
	later := deadline.Add(500 * time.Millisecond)
	overdue := scheduleSample(row, time.UTC, later)
	if !overdue.Stale || overdue.StalenessSeconds != 0.5 {
		t.Fatalf("subsecond=%+v", overdue)
	}
	row.LastSuccessAt = &later
	recovered := scheduleSample(row, time.UTC, later)
	if recovered.Stale || recovered.StalenessSeconds != 0 || recovered.ExpectedSuccessUnix != deadline.Add(24*time.Hour).Unix() {
		t.Fatalf("recovery=%+v", recovered)
	}
}

func TestScheduleMetricsIntervalDoesNotInventNextDispatch(t *testing.T) {
	created := time.Date(2026, 11, 10, 4, 0, 0, 0, time.UTC)
	now := created.Add(2 * time.Hour)
	s := scheduleSample(db.ScheduleFreshness{Enabled: true, CronExpr: "@every 1h", CreatedAt: created}, time.UTC, now)
	if !s.FreshnessKnown || !s.Stale || s.NextRunUnix != 0 || s.ExpectedSuccessUnix != created.Add(70*time.Minute).Unix() || s.StalenessSeconds != 3000 {
		t.Fatalf("interval=%+v", s)
	}
}
