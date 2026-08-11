package api_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/rvben/shinyhub/internal/api"
	"github.com/rvben/shinyhub/internal/auth"
	"github.com/rvben/shinyhub/internal/db"
)

// projectEnv returns a server plus a JWT for a developer who will own the apps
// created below. The token is minted from the user's real DB id: BearerMiddleware
// re-resolves the user on every request, so a token for a nonexistent id 401s.
func projectEnv(t *testing.T) (*api.Server, *db.Store, string) {
	t.Helper()
	srv, store := newTestServer(t)
	hash, err := testHashPassword("pass")
	if err != nil {
		t.Fatalf("hash password: %v", err)
	}
	if err := store.CreateUser(db.CreateUserParams{
		Username: "owner", PasswordHash: hash, Role: "developer",
	}); err != nil {
		t.Fatalf("create user: %v", err)
	}
	u, err := store.GetUserByUsername("owner")
	if err != nil {
		t.Fatalf("lookup user: %v", err)
	}
	token, err := auth.IssueJWT(u.ID, u.Username, u.Role, "test-secret")
	if err != nil {
		t.Fatalf("issue jwt: %v", err)
	}
	return srv, store, token
}

func projectReq(t *testing.T, srv *api.Server, method, path, token string, body any) *httptest.ResponseRecorder {
	t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal body: %v", err)
	}
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, authedRequest(t, method, path, raw, token))
	return rec
}

func TestCreateAppRejectsInvalidProjectSlug(t *testing.T) {
	srv, _, token := projectEnv(t)
	// 64 chars is one over the 63-char ceiling migration 050's backfill predicate
	// enforces, so the API and the schema agree on the boundary.
	bad := []string{"Bad Slug", "-lead", "trail-", strings.Repeat("a", 64), "UPPER", "under_score"}
	for i, p := range bad {
		rec := projectReq(t, srv, http.MethodPost, "/api/apps", token, map[string]any{
			"slug": fmt.Sprintf("app-%d", i), "name": "A", "project_slug": p,
		})
		if rec.Code != http.StatusBadRequest {
			t.Errorf("POST /api/apps with project_slug %q = %d, want 400", p, rec.Code)
		}
	}
	// Positive control: without it, a handler that rejected EVERY create would
	// pass the loop above. The empty string is always legal (it means "no
	// project"), and a legal slug must survive.
	for i, good := range []string{"", "analytics"} {
		rec := projectReq(t, srv, http.MethodPost, "/api/apps", token, map[string]any{
			"slug": fmt.Sprintf("ok-%d", i), "name": "A", "project_slug": good,
		})
		if rec.Code != http.StatusCreated {
			t.Errorf("POST with project_slug %q = %d, want 201: %s", good, rec.Code, rec.Body.String())
		}
	}
}

func TestPatchAppRejectsInvalidProjectSlug(t *testing.T) {
	srv, store, token := projectEnv(t)
	owner, err := store.GetUserByUsername("owner")
	if err != nil {
		t.Fatal(err)
	}
	// Two returns: Task 5 changed CreateApp to (projectCreated bool, err error).
	if _, err := store.CreateApp(db.CreateAppParams{Slug: "a", Name: "A", OwnerID: owner.ID}); err != nil {
		t.Fatal(err)
	}

	if rec := projectReq(t, srv, http.MethodPatch, "/api/apps/a", token,
		map[string]any{"project_slug": "Not A Slug"}); rec.Code != http.StatusBadRequest {
		t.Errorf("PATCH with an invalid project_slug = %d, want 400", rec.Code)
	}
	// Whitespace is trimmed before validation, so a padded legal slug passes.
	if rec := projectReq(t, srv, http.MethodPatch, "/api/apps/a", token,
		map[string]any{"project_slug": "  analytics  "}); rec.Code != http.StatusOK {
		t.Fatalf("PATCH with a padded legal slug = %d, want 200", rec.Code)
	}
	app, err := store.GetAppBySlug("a")
	if err != nil {
		t.Fatal(err)
	}
	if app.ProjectSlug != "analytics" {
		t.Errorf("project_slug = %q, want analytics (trimmed)", app.ProjectSlug)
	}
}

func TestPatchAppAuditsProjectMoveAndProjectCreate(t *testing.T) {
	srv, store, token := projectEnv(t)
	owner, err := store.GetUserByUsername("owner")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateApp(db.CreateAppParams{Slug: "a", Name: "A", OwnerID: owner.ID}); err != nil {
		t.Fatal(err)
	}
	if rec := projectReq(t, srv, http.MethodPatch, "/api/apps/a", token,
		map[string]any{"project_slug": "analytics"}); rec.Code != http.StatusOK {
		t.Fatalf("patch: %d: %s", rec.Code, rec.Body.String())
	}

	updates, err := store.ListAuditEvents("update_app", 50, 0)
	if err != nil {
		t.Fatal(err)
	}
	var sawMove bool
	for _, e := range updates {
		if strings.Contains(e.Detail, `"project_slug"`) && strings.Contains(e.Detail, `"analytics"`) {
			sawMove = true
		}
	}
	if !sawMove {
		t.Error("moving an app between projects must appear in the update_app detail blob")
	}

	// The literal action string, not db.AuditProjectCreate: this pins the value
	// that reaches the audit log and the /api/audit consumer, so renaming the Go
	// constant cannot silently change the wire contract.
	creates, err := store.ListAuditEvents("project.create", 50, 0)
	if err != nil {
		t.Fatal(err)
	}
	var sawCreate bool
	for _, e := range creates {
		if e.ResourceType == "project" && e.ResourceID == "analytics" &&
			e.UserID != nil && *e.UserID == owner.ID {
			sawCreate = true
		}
	}
	if !sawCreate {
		t.Error("implicitly creating a project must emit project.create attributed to the acting user")
	}
}
