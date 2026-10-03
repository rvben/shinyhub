package api_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/rvben/shinyhub/internal/auth"
	"github.com/rvben/shinyhub/internal/db"
)

// deploymentSummaryMarker is a substring that appears in this codebase only
// inside the SQL text deploymentSummarySQL produces (internal/db/queries.go,
// "AS last_deployment_status" is its final projected column and unique to
// it, confirmed by grep against every other query). A captured statement
// containing it ran the six correlated per-row deployment subqueries; a
// whole handler execution containing none of them proves the handler used a
// Lean listing query instead.
const deploymentSummaryMarker = "AS last_deployment_status"

// captureQueries installs an ObserveQueries hook on store, collecting every
// rebound SQL statement issued through the store's connection pool until the
// returned restore func runs. The observer may be invoked concurrently with
// itself, so the collected slice is mutex-guarded.
func captureQueries(store *db.Store) (queries func() []string, restore func()) {
	var mu sync.Mutex
	var seen []string
	r := store.ObserveQueries(func(sql string) {
		mu.Lock()
		seen = append(seen, sql)
		mu.Unlock()
	})
	return func() []string {
		mu.Lock()
		defer mu.Unlock()
		out := make([]string, len(seen))
		copy(out, seen)
		return out
	}, r
}

func assertNoDeploymentSummaryQuery(t *testing.T, queries []string) {
	t.Helper()
	for _, q := range queries {
		if strings.Contains(q, deploymentSummaryMarker) {
			t.Fatalf("expected no query to run the per-row deployment subqueries, but observed one:\n%s", q)
		}
	}
}

// TestFleetHealth_SkipsDeploymentSummaryQuery proves GET /api/fleet/health
// lists apps via ListAppsLean: fleetHealthResponse never surfaces a
// deployment-derived field (see the comment at its ListAppsLean call site),
// so the six per-row deployment subqueries should never run for this
// request.
func TestFleetHealth_SkipsDeploymentSummaryQuery(t *testing.T) {
	srv, store := newFleetHealthServer(t)
	hash, _ := testHashPassword("pass")
	store.CreateUser(db.CreateUserParams{Username: "fh-admin", PasswordHash: hash, Role: "admin"})
	admin, _ := store.GetUserByUsername("fh-admin")
	adminTok, _ := auth.IssueJWT(admin.ID, "fh-admin", "admin", "test-secret")
	store.CreateApp(db.CreateAppParams{Slug: "watched", Name: "Watched", OwnerID: admin.ID})
	app, _ := store.GetAppBySlug("watched")
	if _, err := store.CreateDeployment(db.CreateDeploymentParams{
		AppID: app.ID, Version: "v1", BundleDir: "/bundle/watched", Status: db.DeploymentSucceeded,
	}); err != nil {
		t.Fatal(err)
	}

	queries, restore := captureQueries(store)
	defer restore()

	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, authedRequest(t, "GET", "/api/fleet/health", nil, adminTok))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
	assertNoDeploymentSummaryQuery(t, queries())
}

// TestListProjects_SkipsDeploymentSummaryQuery proves GET /api/projects lists
// apps via ListAppsLean/ListAppsVisibleToUserLean when it reaches
// scopedProjects: that function reads only a.ProjectSlug and a.Slug, never a
// deployment field.
//
// handleListProjects only calls scopedProjects for a scope-restricted
// identity (u.HasAppScopeRestriction()); an unrestricted caller goes through
// ListProjectsVisibleToUser/ListProjects instead, which this change does not
// touch. So the request here attaches a ContextUser with AppScopeRestricted
// directly, the same seam apps_scoped_listing_test.go's
// restrictedEmptyScopeVisibilityRequest documents for an upstream
// authenticator that has already resolved the caller, to actually exercise
// scopedProjects rather than silently missing it.
func TestListProjects_SkipsDeploymentSummaryQuery(t *testing.T) {
	srv, store := newTestServer(t)
	ownerID, _ := mkUser(t, store, "proj-owner", "developer")
	if _, err := store.CreateApp(db.CreateAppParams{
		Slug: "proj-app", Name: "Proj App", OwnerID: ownerID, Access: "public", ProjectSlug: "demo",
	}); err != nil {
		t.Fatal(err)
	}
	app, _ := store.GetAppBySlug("proj-app")
	if _, err := store.CreateDeployment(db.CreateDeploymentParams{
		AppID: app.ID, Version: "v1", BundleDir: "/bundle/proj-app", Status: db.DeploymentSucceeded,
	}); err != nil {
		t.Fatal(err)
	}

	queries, restore := captureQueries(store)
	defer restore()

	req := httptest.NewRequest(http.MethodGet, "/api/projects", nil)
	u := &auth.ContextUser{
		ID: ownerID, Username: "proj-owner", Role: "developer",
		AppScope: []string{"proj-app"}, AppScopeRestricted: true,
	}
	req = req.WithContext(auth.WithUser(req.Context(), u))
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
	var body struct {
		Items []struct {
			Slug     string `json:"slug"`
			AppCount int    `json:"app_count"`
		} `json:"items"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(body.Items) != 1 || body.Items[0].Slug != "demo" || body.Items[0].AppCount != 1 {
		t.Fatalf("expected scopedProjects to actually run (one project 'demo' with app_count 1), got %+v (query never exercised, so this guard proves nothing)", body.Items)
	}
	assertNoDeploymentSummaryQuery(t, queries())
}

// TestListSupportSessionApps_SkipsDeploymentSummaryQuery proves
// GET /api/users/{id}/support-apps lists apps via ListAppsLean/
// ListAppsVisibleToUserLean: handleListSupportSessionApps reads only
// app.ID, app.Slug and app.Name, never a deployment field.
func TestListSupportSessionApps_SkipsDeploymentSummaryQuery(t *testing.T) {
	srv, store, admin, _ := newSupportSessionServer(t, true)
	adminTok, _ := auth.IssueJWT(admin.ID, admin.Username, admin.Role, "test-secret")
	app, _ := store.GetAppBySlug("sales")
	if _, err := store.CreateDeployment(db.CreateDeploymentParams{
		AppID: app.ID, Version: "v1", BundleDir: "/bundle/sales", Status: db.DeploymentSucceeded,
	}); err != nil {
		t.Fatal(err)
	}

	queries, restore := captureQueries(store)
	defer restore()

	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, authedRequest(t, "GET", "/api/users/"+strconv.FormatInt(admin.ID, 10)+"/support-apps", nil, adminTok))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
	assertNoDeploymentSummaryQuery(t, queries())
}

// TestBatchMetrics_StillRunsDeploymentSummaryQuery is the deliberate
// opposite of the three tests above: metricAppsForUser's whole-fleet branch
// (GET /api/apps/metrics with no ?slugs=) was NOT switched to a Lean listing,
// because buildAppMetricsFrom reads app.LastDeploymentStatus into the
// response. This guards that decision: if a future change switches this
// branch to a Lean listing to save the subqueries, batch metrics would
// silently stop reporting last_deployment_status, and this test would be the
// one to catch it.
func TestBatchMetrics_StillRunsDeploymentSummaryQuery(t *testing.T) {
	srv, store := newTestServer(t)
	hash, _ := testHashPassword("pass")
	store.CreateUser(db.CreateUserParams{Username: "bm-owner", PasswordHash: hash, Role: "developer"})
	owner, _ := store.GetUserByUsername("bm-owner")
	store.CreateApp(db.CreateAppParams{Slug: "bm-app", Name: "BM App", OwnerID: owner.ID})
	app, _ := store.GetAppBySlug("bm-app")
	if _, err := store.CreateDeployment(db.CreateDeploymentParams{
		AppID: app.ID, Version: "v1", BundleDir: "/bundle/bm-app", Status: db.DeploymentSucceeded,
	}); err != nil {
		t.Fatal(err)
	}
	token, _ := auth.IssueJWT(owner.ID, "bm-owner", "developer", "test-secret")

	queries, restore := captureQueries(store)
	defer restore()

	req := authedRequest(t, "GET", "/api/apps/metrics", nil, token)
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}

	var sawDeploymentSummaryQuery bool
	for _, q := range queries() {
		if strings.Contains(q, deploymentSummaryMarker) {
			sawDeploymentSummaryQuery = true
			break
		}
	}
	if !sawDeploymentSummaryQuery {
		t.Fatal("metricAppsForUser's whole-fleet branch must still run the deployment-summary query: " +
			"buildAppMetricsFrom reads app.LastDeploymentStatus into the response, so switching this " +
			"to a Lean listing would silently zero that field")
	}
}
