package scheduler

import (
	"strings"
	"testing"
	"time"
)

func mustLoad(t *testing.T, name string) *time.Location {
	t.Helper()
	loc, err := time.LoadLocation(name)
	if err != nil {
		t.Skipf("tz database missing %s: %v", name, err)
	}
	return loc
}

// TestDSTAdvisory_FallBackDoubleFire: a fixed-hour daily schedule whose local
// time lands in the fall-back repeated hour fires twice on the transition day.
// The advisory must name that date and the zone. Amsterdam falls back
// 2025-10-26 (03:00 CEST -> 02:00 CET), so "30 2 * * *" recurs at 02:30.
func TestDSTAdvisory_FallBackDoubleFire(t *testing.T) {
	loc := mustLoad(t, "Europe/Amsterdam")
	ref := time.Date(2025, time.September, 1, 0, 0, 0, 0, time.UTC)

	got := DSTAdvisory("30 2 * * *", loc, ref)
	if got == "" {
		t.Fatal("expected a double-fire advisory for 02:30 Europe/Amsterdam, got none")
	}
	if !strings.Contains(got, "2025-10-26") {
		t.Errorf("advisory should name the fall-back date 2025-10-26, got %q", got)
	}
	if !strings.Contains(got, "Europe/Amsterdam") {
		t.Errorf("advisory should name the zone, got %q", got)
	}
}

// TestDSTAdvisory_NonOverlappingHourIsSilent: a fixed-hour job at a time that
// never falls in the repeated hour (14:30) must not warn.
func TestDSTAdvisory_NonOverlappingHourIsSilent(t *testing.T) {
	loc := mustLoad(t, "Europe/Amsterdam")
	ref := time.Date(2025, time.September, 1, 0, 0, 0, 0, time.UTC)
	if got := DSTAdvisory("30 14 * * *", loc, ref); got != "" {
		t.Errorf("14:30 never overlaps a DST transition; want no advisory, got %q", got)
	}
}

// TestDSTAdvisory_UTCIsSilent: UTC never observes DST, so no schedule warns.
func TestDSTAdvisory_UTCIsSilent(t *testing.T) {
	ref := time.Date(2025, time.September, 1, 0, 0, 0, 0, time.UTC)
	if got := DSTAdvisory("30 2 * * *", time.UTC, ref); got != "" {
		t.Errorf("UTC has no DST; want no advisory, got %q", got)
	}
}

// TestDSTAdvisory_HourlyJobIsSilent: an every-hour schedule already fires many
// times a day, so the extra fall-back repeat is expected, not a footgun. The
// advisory targets fixed-hour jobs only.
func TestDSTAdvisory_HourlyJobIsSilent(t *testing.T) {
	loc := mustLoad(t, "Europe/Amsterdam")
	ref := time.Date(2025, time.September, 1, 0, 0, 0, 0, time.UTC)
	if got := DSTAdvisory("30 * * * *", loc, ref); got != "" {
		t.Errorf("hourly schedule should not warn; got %q", got)
	}
}

// TestDSTAdvisory_USEasternFallBackHour: the repeated hour is zone-specific.
// US Eastern falls back 2025-11-02 (02:00 EDT -> 01:00 EST), so 01:30 recurs
// while 02:30 does not.
func TestDSTAdvisory_USEasternFallBackHour(t *testing.T) {
	loc := mustLoad(t, "America/New_York")
	ref := time.Date(2025, time.October, 1, 0, 0, 0, 0, time.UTC)

	if got := DSTAdvisory("30 1 * * *", loc, ref); got == "" {
		t.Error("01:30 America/New_York lands in the fall-back hour; want an advisory")
	} else if !strings.Contains(got, "2025-11-02") {
		t.Errorf("advisory should name 2025-11-02, got %q", got)
	}
	if got := DSTAdvisory("30 2 * * *", loc, ref); got != "" {
		t.Errorf("02:30 America/New_York does not overlap; want no advisory, got %q", got)
	}
}

// TestDSTAdvisory_InvalidCronIsSilent: a malformed expression is handled by
// validation elsewhere; the advisory must not panic or invent a warning.
func TestDSTAdvisory_InvalidCronIsSilent(t *testing.T) {
	loc := mustLoad(t, "Europe/Amsterdam")
	ref := time.Date(2025, time.September, 1, 0, 0, 0, 0, time.UTC)
	if got := DSTAdvisory("not a cron", loc, ref); got != "" {
		t.Errorf("invalid cron should yield no advisory, got %q", got)
	}
}
