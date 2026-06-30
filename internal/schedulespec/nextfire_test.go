package schedulespec

import (
	"testing"
	"time"
)

func TestNextFire_Daily(t *testing.T) {
	after := time.Date(2026, 6, 30, 5, 0, 0, 0, time.UTC) // 05:00 UTC
	got, err := NextFire("0 6 * * *", time.UTC, after)    // daily at 06:00
	if err != nil {
		t.Fatalf("NextFire: %v", err)
	}
	want := time.Date(2026, 6, 30, 6, 0, 0, 0, time.UTC)
	if !got.Equal(want) {
		t.Fatalf("NextFire = %v, want %v", got, want)
	}
}

func TestNextFire_TimezoneHonored(t *testing.T) {
	loc, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Fatalf("LoadLocation: %v", err)
	}
	// Daily at 06:00 New York. On 2026-06-30 EDT is UTC-4, so 06:00 EDT = 10:00 UTC.
	after := time.Date(2026, 6, 30, 9, 0, 0, 0, time.UTC)
	got, err := NextFire("0 6 * * *", loc, after)
	if err != nil {
		t.Fatalf("NextFire: %v", err)
	}
	want := time.Date(2026, 6, 30, 10, 0, 0, 0, time.UTC)
	if !got.Equal(want) {
		t.Fatalf("NextFire = %v (UTC %v), want %v", got, got.UTC(), want)
	}
}
