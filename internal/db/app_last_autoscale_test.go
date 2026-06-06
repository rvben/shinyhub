package db_test

import (
	"testing"

	"github.com/rvben/shinyhub/internal/dbtest"
)

func TestAppLastAutoscaleAt_RoundTrip(t *testing.T) {
	s := dbtest.New(t)
	owner := mustCreateUser(t, s, "owner", "developer")
	mustCreateApp(t, s, "demo", owner.ID)

	got, err := s.GetAppBySlug("demo")
	if err != nil {
		t.Fatal(err)
	}
	if got.LastAutoscaleAt != 0 {
		t.Fatalf("default last_autoscale_at = %d, want 0", got.LastAutoscaleAt)
	}

	if err := s.SetAppLastAutoscaleAt("demo", 1730000000); err != nil {
		t.Fatal(err)
	}
	got, err = s.GetAppBySlug("demo")
	if err != nil {
		t.Fatal(err)
	}
	if got.LastAutoscaleAt != 1730000000 {
		t.Fatalf("last_autoscale_at = %d, want 1730000000", got.LastAutoscaleAt)
	}

	// An unknown slug is a no-op, not an error.
	if err := s.SetAppLastAutoscaleAt("nope", 1); err != nil {
		t.Fatalf("set on unknown slug: %v", err)
	}
}
