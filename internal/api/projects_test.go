package api_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/rvben/shinyhub/internal/db"
)

// projectList decodes a GET /api/projects body into slug -> app_count. A slug
// present with 0 apps and a slug absent entirely are different answers, so the
// map's key set is the list membership and callers check both.
func projectList(t *testing.T, rec *httptest.ResponseRecorder) map[string]int {
	t.Helper()
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/projects = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	var body struct {
		Items []struct {
			Slug     string `json:"slug"`
			AppCount int    `json:"app_count"`
		} `json:"items"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode projects: %v", err)
	}
	out := map[string]int{}
	for _, it := range body.Items {
		out[it.Slug] = it.AppCount
	}
	return out
}

// mkProjectApp creates an app in a project with a given visibility. access is
// passed to CreateApp rather than set afterwards: CreateAppParams.Access
// (queries.go:796) is always written, so leaving it empty stores a literal ""
// that matches none of the visibility paths.
func mkProjectApp(t *testing.T, store *db.Store, slug, project, access string, ownerID int64) {
	t.Helper()
	// Two returns: Task 5 changed CreateApp to (projectCreated bool, err error).
	if _, err := store.CreateApp(db.CreateAppParams{
		Slug: slug, Name: slug, ProjectSlug: project, OwnerID: ownerID, Access: access,
	}); err != nil {
		t.Fatalf("create app %s: %v", slug, err)
	}
}

func TestListProjectsIsAccessScoped(t *testing.T) {
	srv, store := newTestServer(t)
	ownerID, _ := mkUser(t, store, "owner", "developer")
	_, viewerTok := mkUser(t, store, "viewer", "developer")
	_, adminTok := mkUser(t, store, "boss", "admin")

	mkProjectApp(t, store, "hidden", "secret", "private", ownerID)
	mkProjectApp(t, store, "open", "shown", "public", ownerID)
	// A second app in the SAME project as "open", invisible to the viewer.
	mkProjectApp(t, store, "open-sibling", "shown", "private", ownerID)

	got := projectList(t, do(t, srv, "GET", "/api/projects", viewerTok, nil))
	if _, ok := got["secret"]; ok {
		t.Error("a viewer must not see a project whose only app is invisible to them")
	}
	if _, ok := got["shown"]; !ok {
		t.Errorf("a viewer must see the project of an app they can see, got %v", got)
	}
	// app_count is scoped the same way the list is: counting the private
	// sibling would tell the viewer an app exists that they cannot open.
	if got["shown"] != 1 {
		t.Errorf("viewer's app_count for shown = %d, want 1", got["shown"])
	}

	// An admin sees everything, including a project with no apps at all.
	if rec := do(t, srv, "POST", "/api/projects", adminTok, []byte(`{"slug":"empty"}`)); rec.Code != http.StatusCreated {
		t.Fatalf("create empty project = %d: %s", rec.Code, rec.Body.String())
	}
	got = projectList(t, do(t, srv, "GET", "/api/projects", adminTok, nil))
	if _, ok := got["secret"]; !ok {
		t.Errorf("privileged operator must see every project, got %v", got)
	}
	if got["shown"] != 2 {
		t.Errorf("admin's app_count for shown = %d, want 2", got["shown"])
	}
	// A project with no apps is listed with 0, not omitted: an operator who
	// just created it must find it in the list and the autocomplete.
	if n, ok := got["empty"]; !ok || n != 0 {
		t.Errorf("app_count for an empty project = %d ok=%v, want 0 present", n, ok)
	}
}

// TestListProjectsRespectsTokenScope pins scope-beats-role on this endpoint. A
// deploy token allowlisted to one app must not enumerate project names through
// its admin role, and must not count apps outside the allowlist - including
// public ones, which every other identity can see.
func TestListProjectsRespectsTokenScope(t *testing.T) {
	srv, store := newTestServer(t)
	ownerID, _ := mkUser(t, store, "owner", "developer")
	mkProjectApp(t, store, "inscope", "alpha", "private", ownerID)
	mkProjectApp(t, store, "sibling", "alpha", "public", ownerID)
	mkProjectApp(t, store, "outscope", "beta", "public", ownerID)
	tok := scopedDeployToken(t, srv, store, "admin", []string{"inscope"})

	got := projectList(t, doToken(t, srv, "GET", "/api/projects", tok, nil))
	if _, ok := got["beta"]; ok {
		t.Errorf("a scoped token must not see a project holding no in-scope app, got %v", got)
	}
	if got["alpha"] != 1 {
		t.Errorf("scoped app_count for alpha = %d, want 1 (the public sibling is out of scope)", got["alpha"])
	}
}

func TestCreateProjectIsIdempotent(t *testing.T) {
	srv, store := newTestServer(t)
	_, adminTok := mkUser(t, store, "boss", "admin")

	rec := do(t, srv, "POST", "/api/projects", adminTok,
		[]byte(`{"slug":"analytics","name":"Analytics","icon_emoji":"📊"}`))
	if rec.Code != http.StatusCreated {
		t.Fatalf("first POST = %d, want 201: %s", rec.Code, rec.Body.String())
	}
	// Second POST with different metadata: 200, and the stored values stand.
	rec = do(t, srv, "POST", "/api/projects", adminTok, []byte(`{"slug":"analytics","name":"Clobbered"}`))
	if rec.Code != http.StatusOK {
		t.Fatalf("second POST = %d, want 200 (idempotent, not 201, not 409)", rec.Code)
	}
	p, err := store.GetProject("analytics")
	if err != nil {
		t.Fatal(err)
	}
	if p.Name != "Analytics" {
		t.Errorf("POST overwrote the name: %q", p.Name)
	}
}

func TestCreateProjectRejectsBadInput(t *testing.T) {
	srv, store := newTestServer(t)
	_, adminTok := mkUser(t, store, "boss", "admin")
	for _, body := range []string{
		`{"slug":""}`,
		`{"slug":"Bad Slug"}`,
		`{"slug":"ok","icon_emoji":"not-an-emoji"}`,
	} {
		if rec := do(t, srv, "POST", "/api/projects", adminTok, []byte(body)); rec.Code != http.StatusBadRequest {
			t.Errorf("POST %s = %d, want 400", body, rec.Code)
		}
	}
}

func TestProjectMutationsRequirePrivilege(t *testing.T) {
	srv, store := newTestServer(t)
	_, devTok := mkUser(t, store, "dev", "developer")
	for _, tc := range []struct {
		method, path, body string
	}{
		{"POST", "/api/projects", `{"slug":"x"}`},
		{"PATCH", "/api/projects/x", `{"name":"y"}`},
		{"DELETE", "/api/projects/x", ""},
	} {
		var body []byte
		if tc.body != "" {
			body = []byte(tc.body)
		}
		rec := do(t, srv, tc.method, tc.path, devTok, body)
		if rec.Code != http.StatusForbidden {
			t.Errorf("%s %s as developer = %d, want 403", tc.method, tc.path, rec.Code)
		}
	}
}

func TestDeleteProjectRefusesWhileReferenced(t *testing.T) {
	srv, store := newTestServer(t)
	adminID, adminTok := mkUser(t, store, "boss", "admin")
	mkProjectApp(t, store, "a", "busy", "private", adminID)

	if rec := do(t, srv, "DELETE", "/api/projects/busy", adminTok, nil); rec.Code != http.StatusConflict {
		t.Fatalf("DELETE a referenced project = %d, want 409", rec.Code)
	}
	if rec := do(t, srv, "PATCH", "/api/apps/a", adminTok, []byte(`{"project_slug":""}`)); rec.Code != http.StatusOK {
		t.Fatalf("clear app project = %d: %s", rec.Code, rec.Body.String())
	}
	if rec := do(t, srv, "DELETE", "/api/projects/busy", adminTok, nil); rec.Code != http.StatusNoContent {
		t.Fatalf("DELETE an unreferenced project = %d, want 204", rec.Code)
	}
	if rec := do(t, srv, "DELETE", "/api/projects/busy", adminTok, nil); rec.Code != http.StatusNotFound {
		t.Fatalf("DELETE a missing project = %d, want 404", rec.Code)
	}
}
