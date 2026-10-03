package db_test

import (
	"testing"

	"github.com/rvben/shinyhub/internal/db"
	"github.com/rvben/shinyhub/internal/dbtest"
)

// nonDeploymentFieldsOf copies a, zeroing the six fields deploymentSummarySQL
// populates (LastDeployedAt, ReleaseNumber, ReleasedAt, CurrentVersion,
// ContentDigest, LastDeploymentStatus). It is the expected shape of a row
// returned by the lean listing, reduced from a row returned by the full one.
func nonDeploymentFieldsOf(a *db.App) db.App {
	cp := *a
	cp.LastDeployedAt = nil
	cp.ReleaseNumber = 0
	cp.ReleasedAt = nil
	cp.CurrentVersion = ""
	cp.ContentDigest = ""
	cp.LastDeploymentStatus = ""
	return cp
}

// TestListAppsLean_MatchesListApps proves ListAppsLean / ListAppsVisibleToUserLean
// (the skip-deploymentSummarySQL queries introduced for whole-fleet listing
// paths that never read a deployment field) return exactly the row set and
// plain-column values that ListApps / ListAppsVisibleToUser do, in the same
// order, across apps with no deployments, a single succeeded one, and a mix of
// pending/failed/succeeded ones. The six deployment-derived fields are
// asserted at their zero value on the lean side; a positive control below
// proves the full side actually carries non-zero deployment data for at least
// one app, so the comparison cannot pass merely because both sides are empty.
func TestListAppsLean_MatchesListApps(t *testing.T) {
	store := dbtest.New(t)

	if err := store.CreateUser(db.CreateUserParams{Username: "owner", PasswordHash: "h", Role: "developer"}); err != nil {
		t.Fatalf("create owner: %v", err)
	}
	owner, err := store.GetUserByUsername("owner")
	if err != nil {
		t.Fatalf("get owner: %v", err)
	}

	seed := []struct {
		slug        string
		access      string
		status      string
		projectSlug string
		deployments []string // deployment statuses to create, in order
	}{
		{slug: "no-deploys", access: "public", status: "never_deployed", projectSlug: "", deployments: nil},
		{slug: "one-succeeded", access: "private", status: "running", projectSlug: "rockets", deployments: []string{db.DeploymentSucceeded}},
		{slug: "mixed-history", access: "shared", status: "hibernated", projectSlug: "lab", deployments: []string{db.DeploymentFailed, db.DeploymentPending, db.DeploymentSucceeded, db.DeploymentFailed}},
	}

	for _, sd := range seed {
		if _, err := store.CreateApp(db.CreateAppParams{
			Slug: sd.slug, Name: "App " + sd.slug, OwnerID: owner.ID, Access: sd.access, ProjectSlug: sd.projectSlug,
		}); err != nil {
			t.Fatalf("create app %s: %v", sd.slug, err)
		}
		app, err := store.GetAppBySlug(sd.slug)
		if err != nil {
			t.Fatalf("get app %s: %v", sd.slug, err)
		}
		if sd.status != "" {
			if err := store.UpdateAppStatus(db.UpdateAppStatusParams{Slug: sd.slug, Status: sd.status}); err != nil {
				t.Fatalf("set status for %s: %v", sd.slug, err)
			}
		}
		for i, depStatus := range sd.deployments {
			if _, err := store.CreateDeployment(db.CreateDeploymentParams{
				AppID: app.ID, Version: "v" + string(rune('1'+i)), BundleDir: "/bundle/" + sd.slug, Status: depStatus,
			}); err != nil {
				t.Fatalf("create deployment %d for %s: %v", i, sd.slug, err)
			}
		}
	}

	full, err := store.ListApps(0, 0)
	if err != nil {
		t.Fatalf("ListApps: %v", err)
	}
	lean, err := store.ListAppsLean(0, 0)
	if err != nil {
		t.Fatalf("ListAppsLean: %v", err)
	}
	if len(full) != len(lean) {
		t.Fatalf("row count differs: ListApps=%d ListAppsLean=%d", len(full), len(lean))
	}

	// Positive control: at least one seeded app must carry non-zero deployment
	// data in the full listing, or the comparison below would pass trivially
	// even if ListAppsLean silently dropped its query's WHERE/JOIN entirely.
	var sawDeploymentData bool
	for _, a := range full {
		if a.LastDeploymentStatus != "" || a.ReleaseNumber != 0 {
			sawDeploymentData = true
			break
		}
	}
	if !sawDeploymentData {
		t.Fatalf("positive control failed: no app in ListApps carries deployment data; seed is broken")
	}

	for i := range full {
		want := nonDeploymentFieldsOf(full[i])
		got := *lean[i]
		if got != want {
			t.Errorf("row %d: ListAppsLean = %+v, want %+v (from ListApps, deployment fields zeroed)", i, got, want)
		}
	}

	// Same comparison for the visibility-scoped variant, since it carries its
	// own WHERE clause that could independently drift from ListAppsLean.
	fullVisible, err := store.ListAppsVisibleToUser(owner.ID, 0, 0)
	if err != nil {
		t.Fatalf("ListAppsVisibleToUser: %v", err)
	}
	leanVisible, err := store.ListAppsVisibleToUserLean(owner.ID, 0, 0)
	if err != nil {
		t.Fatalf("ListAppsVisibleToUserLean: %v", err)
	}
	if len(fullVisible) != len(leanVisible) {
		t.Fatalf("visible row count differs: full=%d lean=%d", len(fullVisible), len(leanVisible))
	}
	for i := range fullVisible {
		want := nonDeploymentFieldsOf(fullVisible[i])
		got := *leanVisible[i]
		if got != want {
			t.Errorf("visible row %d: ListAppsVisibleToUserLean = %+v, want %+v", i, got, want)
		}
	}
}
