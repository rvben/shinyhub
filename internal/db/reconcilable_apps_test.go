package db_test

import (
	"testing"

	"github.com/rvben/shinyhub/internal/db"
)

// TestListReconcilableApps_ReturnsRunningAndDegradedOnly verifies the watcher's
// reconcile query returns exactly the apps the watchdog may act on: those that
// are running or degraded. Hibernated, stopped, and deploying apps are owned by
// other lifecycle paths and must be excluded so reconcile never resurrects them.
func TestListReconcilableApps_ReturnsRunningAndDegradedOnly(t *testing.T) {
	s := openTestStore(t)
	owner := mustCreateUser(t, s, "owner", "user")

	cases := []struct {
		slug, status string
		want         bool
	}{
		{"run", "running", true},
		{"deg", "degraded", true},
		{"hib", "hibernated", false},
		{"stop", "stopped", false},
		{"dep", "deploying", false},
	}
	for _, tc := range cases {
		mustCreateApp(t, s, tc.slug, owner.ID)
		if err := s.UpdateAppStatus(db.UpdateAppStatusParams{Slug: tc.slug, Status: tc.status}); err != nil {
			t.Fatalf("set status %q: %v", tc.slug, err)
		}
	}

	apps, err := s.ListReconcilableApps()
	if err != nil {
		t.Fatalf("ListReconcilableApps: %v", err)
	}
	got := make(map[string]bool, len(apps))
	for _, a := range apps {
		got[a.Slug] = true
	}
	for _, tc := range cases {
		if got[tc.slug] != tc.want {
			t.Errorf("slug %q (status %q): present=%v, want %v", tc.slug, tc.status, got[tc.slug], tc.want)
		}
	}
}
