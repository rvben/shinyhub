package db_test

import (
	"testing"

	"github.com/rvben/shinyhub/internal/db"
	"github.com/rvben/shinyhub/internal/dbtest"
)

// TestListWakingApps_ReturnsOnlyWakingApps verifies that ListWakingApps returns
// exactly the apps in status='waking' and excludes all other statuses.
//
// Runs on SQLite always and on Postgres when SHINYHUB_TEST_POSTGRES_DSN is set.
func TestListWakingApps_ReturnsOnlyWakingApps(t *testing.T) {
	store := dbtest.New(t)
	u := mustCreateUser(t, store, "wake-owner", "developer")

	// Create apps in every lifecycle status.
	statuses := map[string]string{
		"waking-1":  "waking",
		"waking-2":  "waking",
		"running-1": "running",
		"hibern-1":  "hibernated",
		"stopped-1": "stopped",
	}
	for slug, status := range statuses {
		mustCreateApp(t, store, slug, u.ID)
		if status != "stopped" { // default is stopped; only advance when needed
			if err := store.UpdateAppStatus(db.UpdateAppStatusParams{Slug: slug, Status: status}); err != nil {
				t.Fatalf("set status %q for %q: %v", status, slug, err)
			}
		}
	}

	apps, err := store.ListWakingApps()
	if err != nil {
		t.Fatalf("ListWakingApps: %v", err)
	}

	if len(apps) != 2 {
		t.Fatalf("expected 2 waking apps, got %d", len(apps))
	}
	for _, a := range apps {
		if a.Status != "waking" {
			t.Errorf("ListWakingApps returned app %q with status=%q, want waking", a.Slug, a.Status)
		}
		if a.Slug != "waking-1" && a.Slug != "waking-2" {
			t.Errorf("ListWakingApps returned unexpected slug %q", a.Slug)
		}
	}
}

// TestListWakingApps_EmptyWhenNoneWaking verifies that ListWakingApps returns an
// empty (not nil) slice when no apps are in the waking state.
func TestListWakingApps_EmptyWhenNoneWaking(t *testing.T) {
	store := dbtest.New(t)
	u := mustCreateUser(t, store, "wk-empty-owner", "developer")

	mustCreateApp(t, store, "runapp", u.ID)
	if err := store.UpdateAppStatus(db.UpdateAppStatusParams{Slug: "runapp", Status: "running"}); err != nil {
		t.Fatalf("set running: %v", err)
	}

	apps, err := store.ListWakingApps()
	if err != nil {
		t.Fatalf("ListWakingApps: %v", err)
	}
	if len(apps) != 0 {
		t.Errorf("expected 0 waking apps, got %d: %+v", len(apps), apps)
	}
}
