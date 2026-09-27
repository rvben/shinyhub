package db_test

import (
	"testing"

	"github.com/rvben/shinyhub/internal/db"
	"github.com/rvben/shinyhub/internal/dbtest"
)

// summaryOf reduces a full App row to the fields AppSummary carries, so the
// lean query's output can be compared field-by-field against the query it
// stands in for.
func summaryOf(a *db.App) db.AppSummary {
	return db.AppSummary{
		Slug:        a.Slug,
		Name:        a.Name,
		Access:      a.Access,
		Status:      a.Status,
		IconEmoji:   a.IconEmoji,
		ProjectSlug: a.ProjectSlug,
	}
}

// TestListAppSummaries_MatchesListApps proves ListAppSummaries (the lean
// query listAppsVisibleTo now uses for /.shinyhub/apps.json and nav.json)
// returns exactly the row set and field values that ListApps (the original,
// deploymentSummarySQL-carrying query) does, in the same order - across apps
// with no deployments, a single succeeded one, and a mix of pending/failed/
// succeeded ones. AppSummary never reads deployment state, so varying it must
// not perturb the comparison; this is what actually proves that true rather
// than assuming it from reading the SELECT list.
func TestListAppSummaries_MatchesListApps(t *testing.T) {
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
		iconEmoji   string
		projectSlug string
		deployments []string // deployment statuses to create, in order
	}{
		{slug: "no-deploys", access: "public", status: "never_deployed", iconEmoji: "", projectSlug: "", deployments: nil},
		{slug: "one-succeeded", access: "private", status: "running", iconEmoji: "🚀", projectSlug: "rockets", deployments: []string{db.DeploymentSucceeded}},
		{slug: "mixed-history", access: "shared", status: "hibernated", iconEmoji: "🧪", projectSlug: "lab", deployments: []string{db.DeploymentFailed, db.DeploymentPending, db.DeploymentSucceeded, db.DeploymentFailed}},
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
		if sd.iconEmoji != "" {
			if err := store.SetAppIconEmoji(sd.slug, sd.iconEmoji); err != nil {
				t.Fatalf("set icon emoji for %s: %v", sd.slug, err)
			}
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
	lean, err := store.ListAppSummaries(0, 0)
	if err != nil {
		t.Fatalf("ListAppSummaries: %v", err)
	}
	if len(full) != len(lean) {
		t.Fatalf("row count differs: ListApps=%d ListAppSummaries=%d", len(full), len(lean))
	}
	for i := range full {
		want := summaryOf(full[i])
		got := *lean[i]
		if got != want {
			t.Errorf("row %d: ListAppSummaries = %+v, want %+v (from ListApps)", i, got, want)
		}
	}

	// Same comparison for the visibility-scoped and public-only variants, since
	// they carry their own WHERE clauses that could independently drift from
	// their full-App counterparts.
	fullVisible, err := store.ListAppsVisibleToUser(owner.ID, 0, 0)
	if err != nil {
		t.Fatalf("ListAppsVisibleToUser: %v", err)
	}
	leanVisible, err := store.ListAppSummariesVisibleToUser(owner.ID, 0, 0)
	if err != nil {
		t.Fatalf("ListAppSummariesVisibleToUser: %v", err)
	}
	if len(fullVisible) != len(leanVisible) {
		t.Fatalf("visible row count differs: full=%d lean=%d", len(fullVisible), len(leanVisible))
	}
	for i := range fullVisible {
		want := summaryOf(fullVisible[i])
		got := *leanVisible[i]
		if got != want {
			t.Errorf("visible row %d: ListAppSummariesVisibleToUser = %+v, want %+v", i, got, want)
		}
	}

	fullPublic, err := store.ListPublicApps(0, 0)
	if err != nil {
		t.Fatalf("ListPublicApps: %v", err)
	}
	leanPublic, err := store.ListPublicAppSummaries(0, 0)
	if err != nil {
		t.Fatalf("ListPublicAppSummaries: %v", err)
	}
	if len(fullPublic) != len(leanPublic) {
		t.Fatalf("public row count differs: full=%d lean=%d", len(fullPublic), len(leanPublic))
	}
	for i := range fullPublic {
		want := summaryOf(fullPublic[i])
		got := *leanPublic[i]
		if got != want {
			t.Errorf("public row %d: ListPublicAppSummaries = %+v, want %+v", i, got, want)
		}
	}
}
