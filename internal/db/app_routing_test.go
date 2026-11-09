package db_test

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/rvben/shinyhub/internal/db"
	"github.com/rvben/shinyhub/internal/dbtest"
)

// routingOwner creates the user that owns every fixture app and returns its id.
func routingOwner(t testing.TB, st *db.Store) int64 {
	t.Helper()
	if err := st.CreateUser(db.CreateUserParams{Username: "owner", PasswordHash: "h", Role: "admin"}); err != nil {
		t.Fatal(err)
	}
	owner, err := st.GetUserByUsername("owner")
	if err != nil {
		t.Fatal(err)
	}
	return owner.ID
}

// The routing lookup answers every field the proxy reads exactly as the full
// app lookup does, across the states that change what a visitor is shown.
func TestGetAppRoutingBySlugAgreesWithGetAppBySlug(t *testing.T) {
	st := dbtest.New(t)
	ownerID := routingOwner(t, st)

	cases := []struct {
		slug, name, access string
		wantLastDeployment string
		setup              func(t *testing.T, app *db.App)
	}{
		{slug: "never-deployed", name: "Never deployed", access: "private"},
		{slug: "serving", name: "Serving", access: "public", wantLastDeployment: "succeeded", setup: func(t *testing.T, app *db.App) {
			mustDeploy(t, st, app.ID, "v1", "succeeded")
			mustSetStatus(t, st, app.Slug, "running", "")
		}},
		{slug: "redeploying", name: "Redeploying", access: "shared", wantLastDeployment: db.DeploymentPending, setup: func(t *testing.T, app *db.App) {
			mustDeploy(t, st, app.ID, "v1", "succeeded")
			mustDeploy(t, st, app.ID, "v2", db.DeploymentPending)
			mustSetStatus(t, st, app.Slug, "running", "")
		}},
		{slug: "stale-pending", name: "Stale pending", access: "private", wantLastDeployment: db.DeploymentPending, setup: func(t *testing.T, app *db.App) {
			mustDeploy(t, st, app.ID, "v1", db.DeploymentPending)
			mustSetStatus(t, st, app.Slug, "stopped", "")
		}},
		{slug: "crashed", name: "Crashed", access: "private", wantLastDeployment: "succeeded", setup: func(t *testing.T, app *db.App) {
			mustDeploy(t, st, app.ID, "v1", "succeeded")
			mustSetStatus(t, st, app.Slug, "crashed", "exit status 1")
		}},
		{slug: "failed", name: "Failed", access: "public", wantLastDeployment: db.DeploymentPending, setup: func(t *testing.T, app *db.App) {
			mustDeploy(t, st, app.ID, "v1", "succeeded")
			mustDeploy(t, st, app.ID, "v2", db.DeploymentPending)
			mustSetStatus(t, st, app.Slug, "failed", "producer data is incompatible")
		}},
	}
	for _, tc := range cases {
		t.Run(tc.slug, func(t *testing.T) {
			if _, err := st.CreateApp(db.CreateAppParams{Slug: tc.slug, Name: tc.name, OwnerID: ownerID, Access: tc.access}); err != nil {
				t.Fatal(err)
			}
			created, err := st.GetAppBySlug(tc.slug)
			if err != nil {
				t.Fatal(err)
			}
			if tc.setup != nil {
				tc.setup(t, created)
			}
			full, err := st.GetAppBySlug(tc.slug)
			if err != nil {
				t.Fatal(err)
			}
			got, err := st.GetAppRoutingBySlug(tc.slug)
			if err != nil {
				t.Fatal(err)
			}
			want := db.AppRouting{
				Name:                 full.Name,
				Access:               full.Access,
				Status:               full.Status,
				LastError:            full.LastError,
				LastDeploymentStatus: full.LastDeploymentStatus,
			}
			if *got != want {
				t.Fatalf("routing lookup = %+v, full lookup = %+v", *got, want)
			}
			if got.Name != tc.name || got.Access != tc.access || got.LastDeploymentStatus != tc.wantLastDeployment {
				t.Fatalf("routing lookup = %+v, want name %q access %q newest deployment %q", *got, tc.name, tc.access, tc.wantLastDeployment)
			}
			for _, inFlight := range []bool{false, true} {
				gs, gr := got.MissStatus(inFlight)
				fs, fr := full.MissStatus(inFlight)
				if gs != fs || gr != fr {
					t.Fatalf("MissStatus(%v) = (%q, %q), full lookup says (%q, %q)", inFlight, gs, gr, fs, fr)
				}
			}
		})
	}
}

func TestGetAppRoutingBySlugReportsMissingApp(t *testing.T) {
	st := dbtest.New(t)
	if _, err := st.GetAppRoutingBySlug("missing"); !errors.Is(err, db.ErrNotFound) {
		t.Fatalf("missing app: err = %v, want ErrNotFound", err)
	}
}

// The routing lookup runs on every proxied request, so its cost must not grow
// with an app's deployment history: the plan reads the newest deployment from
// an index instead of sorting or counting the history.
func TestAppRoutingQueryReadsHistoryThroughAnIndex(t *testing.T) {
	dbtest.SkipIfPostgres(t)
	st := dbtest.New(t)
	rows, err := st.DB().Query("EXPLAIN QUERY PLAN "+db.AppRoutingQuery, "any")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var details []string
	for rows.Next() {
		var id, parent, unused int
		var detail string
		if err := rows.Scan(&id, &parent, &unused, &detail); err != nil {
			t.Fatal(err)
		}
		details = append(details, detail)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	plan := strings.Join(details, "\n")
	if strings.Contains(plan, "TEMP B-TREE") || strings.Contains(plan, "SCAN deployments") ||
		!strings.Contains(plan, "idx_deployments_app_created") {
		t.Fatalf("routing lookup does not read deployment history through idx_deployments_app_created:\n%s", plan)
	}
}

func mustDeploy(t *testing.T, st *db.Store, appID int64, version, status string) {
	t.Helper()
	if _, err := st.CreateDeployment(db.CreateDeploymentParams{AppID: appID, Version: version, BundleDir: "/bundles/" + version, Status: status}); err != nil {
		t.Fatal(err)
	}
}

func mustSetStatus(t *testing.T, st *db.Store, slug, status, lastError string) {
	t.Helper()
	if _, err := st.DB().Exec("UPDATE apps SET status = ?, last_error = ? WHERE slug = ?", status, lastError, slug); err != nil {
		t.Fatal(err)
	}
}

// BenchmarkGetAppRoutingBySlug measures the per-request proxy lookup against a
// growing deployment history; it should stay flat where BenchmarkGetAppBySlug
// grows linearly.
func BenchmarkGetAppRoutingBySlug(b *testing.B) {
	for _, deployments := range []int{0, 500, 5000} {
		b.Run(fmt.Sprintf("deployments=%d", deployments), func(b *testing.B) {
			st := dbtest.New(b)
			ownerID := routingOwner(b, st)
			for i := 0; i < 10; i++ {
				if _, err := st.CreateApp(db.CreateAppParams{Slug: fmt.Sprintf("app-%d", i), Name: "App", OwnerID: ownerID}); err != nil {
					b.Fatal(err)
				}
			}
			target, err := st.GetAppBySlug("app-5")
			if err != nil {
				b.Fatal(err)
			}
			for i := 0; i < deployments; i++ {
				if _, err := st.CreateDeployment(db.CreateDeploymentParams{AppID: target.ID, Version: fmt.Sprintf("v%d", i), BundleDir: fmt.Sprintf("/bundles/v%d", i), Status: "succeeded"}); err != nil {
					b.Fatal(err)
				}
			}
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				if _, err := st.GetAppRoutingBySlug("app-5"); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
