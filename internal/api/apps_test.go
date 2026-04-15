package api_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/rvben/shinyhub/internal/auth"
	"github.com/rvben/shinyhub/internal/db"
)

func authedRequest(t *testing.T, method, path string, body []byte, token string) *http.Request {
	t.Helper()
	var req *http.Request
	if body != nil {
		req = httptest.NewRequest(method, path, bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
	} else {
		req = httptest.NewRequest(method, path, nil)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	return req
}

func TestListApps(t *testing.T) {
	srv, store := newTestServer(t)
	hash, _ := auth.HashPassword("pass")
	store.CreateUser(db.CreateUserParams{Username: "bob", PasswordHash: hash, Role: "admin"})

	token, _ := auth.IssueJWT(1, "bob", "admin", "test-secret")
	req := authedRequest(t, "GET", "/api/apps", nil, token)
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)

	if rec.Code != 200 {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var apps []any
	json.NewDecoder(rec.Body).Decode(&apps)
	// empty list is fine
}

func TestUnauthenticatedRejected(t *testing.T) {
	srv, _ := newTestServer(t)
	req := httptest.NewRequest("GET", "/api/apps", nil) // no auth header
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("expected 401 for unauthenticated request, got %d", rec.Code)
	}
}

func TestCreateApp(t *testing.T) {
	srv, store := newTestServer(t)
	hash, _ := auth.HashPassword("pass")
	store.CreateUser(db.CreateUserParams{Username: "bob", PasswordHash: hash, Role: "developer"})
	token, _ := auth.IssueJWT(1, "bob", "developer", "test-secret")

	body, _ := json.Marshal(map[string]string{"slug": "new-app", "name": "New App"})
	req := authedRequest(t, "POST", "/api/apps", body, token)
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)

	if rec.Code != 201 {
		t.Fatalf("expected 201, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestPatchApp_SetHibernateTimeout(t *testing.T) {
	srv, store := newTestServer(t)
	hash, _ := auth.HashPassword("pass")
	store.CreateUser(db.CreateUserParams{Username: "bob", PasswordHash: hash, Role: "admin"})
	u, _ := store.GetUserByUsername("bob")
	token, _ := auth.IssueJWT(u.ID, "bob", "admin", "test-secret")
	store.CreateApp(db.CreateAppParams{Slug: "myapp", Name: "My App", OwnerID: u.ID})

	body, _ := json.Marshal(map[string]any{"hibernate_timeout_minutes": 60})
	req := authedRequest(t, "PATCH", "/api/apps/myapp", body, token)
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var resp map[string]any
	json.NewDecoder(rec.Body).Decode(&resp)
	if resp["hibernate_timeout_minutes"] != float64(60) {
		t.Errorf("expected hibernate_timeout_minutes=60 in response, got %v", resp["hibernate_timeout_minutes"])
	}
	app, _ := store.GetAppBySlug("myapp")
	if app.HibernateTimeoutMinutes == nil || *app.HibernateTimeoutMinutes != 60 {
		t.Errorf("DB not updated: got %v", app.HibernateTimeoutMinutes)
	}
}

func TestPatchApp_NotFound(t *testing.T) {
	srv, store := newTestServer(t)
	hash, _ := auth.HashPassword("pass")
	store.CreateUser(db.CreateUserParams{Username: "bob", PasswordHash: hash, Role: "admin"})
	u, _ := store.GetUserByUsername("bob")
	token, _ := auth.IssueJWT(u.ID, "bob", "admin", "test-secret")

	body, _ := json.Marshal(map[string]any{"hibernate_timeout_minutes": 30})
	req := authedRequest(t, "PATCH", "/api/apps/nonexistent", body, token)
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Errorf("expected 404 for nonexistent slug, got %d", rec.Code)
	}
}

func TestPatchApp_ResetToGlobalDefault(t *testing.T) {
	srv, store := newTestServer(t)
	hash, _ := auth.HashPassword("pass")
	store.CreateUser(db.CreateUserParams{Username: "bob", PasswordHash: hash, Role: "admin"})
	u, _ := store.GetUserByUsername("bob")
	token, _ := auth.IssueJWT(u.ID, "bob", "admin", "test-secret")
	store.CreateApp(db.CreateAppParams{Slug: "myapp", Name: "My App", OwnerID: u.ID})

	body := []byte(`{"hibernate_timeout_minutes": null}`)
	req := authedRequest(t, "PATCH", "/api/apps/myapp", body, token)
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	app, _ := store.GetAppBySlug("myapp")
	if app.HibernateTimeoutMinutes != nil {
		t.Errorf("expected nil (global default), got %v", app.HibernateTimeoutMinutes)
	}
}

func TestListApps_FilteredByAccess(t *testing.T) {
	srv, store := newTestServer(t)
	hash, _ := auth.HashPassword("pass")
	store.CreateUser(db.CreateUserParams{Username: "owner", PasswordHash: hash, Role: "developer"})
	store.CreateUser(db.CreateUserParams{Username: "viewer", PasswordHash: hash, Role: "viewer"})

	owner, _ := store.GetUserByUsername("owner")
	viewer, _ := store.GetUserByUsername("viewer")

	if err := store.CreateApp(db.CreateAppParams{Slug: "public-app", Name: "Public App", OwnerID: owner.ID}); err != nil {
		t.Fatal(err)
	}
	if err := store.SetAppAccess("public-app", "public"); err != nil {
		t.Fatal(err)
	}
	if err := store.CreateApp(db.CreateAppParams{Slug: "private-app", Name: "Private App", OwnerID: owner.ID}); err != nil {
		t.Fatal(err)
	}
	if err := store.CreateApp(db.CreateAppParams{Slug: "shared-app", Name: "Shared App", OwnerID: owner.ID}); err != nil {
		t.Fatal(err)
	}
	if err := store.SetAppAccess("shared-app", "shared"); err != nil {
		t.Fatal(err)
	}
	if err := store.GrantAppAccess("shared-app", viewer.ID); err != nil {
		t.Fatal(err)
	}

	token, _ := auth.IssueJWT(viewer.ID, "viewer", "viewer", "test-secret")
	req := authedRequest(t, "GET", "/api/apps", nil, token)
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var apps []db.App
	if err := json.NewDecoder(rec.Body).Decode(&apps); err != nil {
		t.Fatalf("decode apps: %v", err)
	}
	if len(apps) != 2 {
		t.Fatalf("expected 2 visible apps, got %d", len(apps))
	}
	for _, app := range apps {
		if app.Slug == "private-app" {
			t.Fatalf("viewer should not see private-app: %+v", apps)
		}
	}
}

func TestCreateApp_ViewerForbidden(t *testing.T) {
	srv, store := newTestServer(t)
	hash, _ := auth.HashPassword("pass")
	store.CreateUser(db.CreateUserParams{Username: "viewonly", PasswordHash: hash, Role: "viewer"})

	token, _ := auth.IssueJWT(1, "viewonly", "viewer", "test-secret")
	body, _ := json.Marshal(map[string]string{"slug": "new-app", "name": "New App"})
	req := authedRequest(t, "POST", "/api/apps", body, token)
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestGetApp_NotFoundWhenNoAccess(t *testing.T) {
	srv, store := newTestServer(t)
	hash, _ := auth.HashPassword("pass")
	store.CreateUser(db.CreateUserParams{Username: "owner", PasswordHash: hash, Role: "developer"})
	store.CreateUser(db.CreateUserParams{Username: "viewer", PasswordHash: hash, Role: "viewer"})
	owner, _ := store.GetUserByUsername("owner")
	viewer, _ := store.GetUserByUsername("viewer")
	if err := store.CreateApp(db.CreateAppParams{Slug: "secret", Name: "Secret", OwnerID: owner.ID}); err != nil {
		t.Fatal(err)
	}

	token, _ := auth.IssueJWT(viewer.ID, "viewer", "viewer", "test-secret")
	req := authedRequest(t, "GET", "/api/apps/secret", nil, token)
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)

	// 404 prevents confirming that the private slug exists to unauthorized users.
	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d: %s", rec.Code, rec.Body.String())
	}
}

// TestGetApp_GrantedMemberCanView anchors the contract for requireViewApp:
// a user granted explicit access to a shared app can retrieve it.
// This test exists to guard the requireViewApp → requireManageApp refactoring
// that eliminates the redundant auth.UserFromContext call in requireManageApp.
func TestGetApp_GrantedMemberCanView(t *testing.T) {
	srv, store := newTestServer(t)
	hash, _ := auth.HashPassword("pass")
	store.CreateUser(db.CreateUserParams{Username: "owner", PasswordHash: hash, Role: "developer"})
	store.CreateUser(db.CreateUserParams{Username: "member", PasswordHash: hash, Role: "viewer"})
	owner, _ := store.GetUserByUsername("owner")
	member, _ := store.GetUserByUsername("member")

	if err := store.CreateApp(db.CreateAppParams{Slug: "shared", Name: "Shared", OwnerID: owner.ID}); err != nil {
		t.Fatal(err)
	}
	if err := store.SetAppAccess("shared", "shared"); err != nil {
		t.Fatal(err)
	}
	if err := store.GrantAppAccess("shared", member.ID); err != nil {
		t.Fatal(err)
	}

	token, _ := auth.IssueJWT(member.ID, "member", "viewer", "test-secret")
	req := authedRequest(t, "GET", "/api/apps/shared", nil, token)
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 for granted member viewing shared app, got %d: %s",
			rec.Code, rec.Body.String())
	}
}

func TestPatchApp_ForbiddenForNonOwner(t *testing.T) {
	srv, store := newTestServer(t)
	hash, _ := auth.HashPassword("pass")
	store.CreateUser(db.CreateUserParams{Username: "owner", PasswordHash: hash, Role: "developer"})
	store.CreateUser(db.CreateUserParams{Username: "member", PasswordHash: hash, Role: "developer"})
	owner, _ := store.GetUserByUsername("owner")
	member, _ := store.GetUserByUsername("member")

	if err := store.CreateApp(db.CreateAppParams{Slug: "shared", Name: "Shared", OwnerID: owner.ID}); err != nil {
		t.Fatal(err)
	}
	if err := store.SetAppAccess("shared", "shared"); err != nil {
		t.Fatal(err)
	}
	if err := store.GrantAppAccess("shared", member.ID); err != nil {
		t.Fatal(err)
	}

	token, _ := auth.IssueJWT(member.ID, "member", "developer", "test-secret")
	body, _ := json.Marshal(map[string]any{"hibernate_timeout_minutes": 10})
	req := authedRequest(t, "PATCH", "/api/apps/shared", body, token)
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestGetMembers_Empty(t *testing.T) {
	srv, store := newTestServer(t)
	hash, _ := auth.HashPassword("pass")
	store.CreateUser(db.CreateUserParams{Username: "owner", PasswordHash: hash, Role: "developer"})
	owner, _ := store.GetUserByUsername("owner")
	store.CreateApp(db.CreateAppParams{Slug: "myapp", Name: "My App", OwnerID: owner.ID})

	token, _ := auth.IssueJWT(owner.ID, "owner", "developer", "test-secret")
	req := authedRequest(t, "GET", "/api/apps/myapp/members", nil, token)
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var members []map[string]any
	json.NewDecoder(rec.Body).Decode(&members)
	if len(members) != 0 {
		t.Errorf("expected empty list, got %v", members)
	}
}

func TestGetMembers_WithMembers(t *testing.T) {
	srv, store := newTestServer(t)
	hash, _ := auth.HashPassword("pass")
	store.CreateUser(db.CreateUserParams{Username: "owner", PasswordHash: hash, Role: "developer"})
	store.CreateUser(db.CreateUserParams{Username: "alice", PasswordHash: hash, Role: "viewer"})
	owner, _ := store.GetUserByUsername("owner")
	alice, _ := store.GetUserByUsername("alice")
	store.CreateApp(db.CreateAppParams{Slug: "myapp", Name: "My App", OwnerID: owner.ID})
	store.GrantAppAccess("myapp", alice.ID)

	token, _ := auth.IssueJWT(owner.ID, "owner", "developer", "test-secret")
	req := authedRequest(t, "GET", "/api/apps/myapp/members", nil, token)
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var members []map[string]any
	json.NewDecoder(rec.Body).Decode(&members)
	if len(members) != 1 {
		t.Fatalf("expected 1 member, got %d", len(members))
	}
	if members[0]["username"] != "alice" {
		t.Errorf("username = %v, want alice", members[0]["username"])
	}
	if members[0]["role"] != "viewer" {
		t.Errorf("role = %v, want viewer", members[0]["role"])
	}
	if members[0]["user_id"] != float64(alice.ID) {
		t.Errorf("user_id = %v, want %v", members[0]["user_id"], alice.ID)
	}
}

func TestGetMembers_Forbidden(t *testing.T) {
	srv, store := newTestServer(t)
	hash, _ := auth.HashPassword("pass")
	store.CreateUser(db.CreateUserParams{Username: "owner", PasswordHash: hash, Role: "developer"})
	store.CreateUser(db.CreateUserParams{Username: "other", PasswordHash: hash, Role: "developer"})
	owner, _ := store.GetUserByUsername("owner")
	other, _ := store.GetUserByUsername("other")
	store.CreateApp(db.CreateAppParams{Slug: "myapp", Name: "My App", OwnerID: owner.ID})
	store.GrantAppAccess("myapp", other.ID)

	token, _ := auth.IssueJWT(other.ID, "other", "developer", "test-secret")
	req := authedRequest(t, "GET", "/api/apps/myapp/members", nil, token)
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Errorf("expected 403, got %d", rec.Code)
	}
}

func TestGetMembers_NotFound(t *testing.T) {
	srv, _ := newTestServer(t)
	token, _ := auth.IssueJWT(1, "admin", "admin", "test-secret")
	req := authedRequest(t, "GET", "/api/apps/nonexistent/members", nil, token)
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Errorf("expected 404, got %d", rec.Code)
	}
}

func TestGetUser_Found(t *testing.T) {
	srv, store := newTestServer(t)
	hash, _ := auth.HashPassword("pass")
	store.CreateUser(db.CreateUserParams{Username: "alice", PasswordHash: hash, Role: "developer"})
	alice, _ := store.GetUserByUsername("alice")

	token, _ := auth.IssueJWT(alice.ID, "alice", "developer", "test-secret")
	req := authedRequest(t, "GET", "/api/users/alice", nil, token)
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var resp map[string]any
	json.NewDecoder(rec.Body).Decode(&resp)
	if resp["username"] != "alice" {
		t.Errorf("username = %v, want alice", resp["username"])
	}
}

func TestGetUser_NotFound(t *testing.T) {
	srv, store := newTestServer(t)
	hash, _ := auth.HashPassword("pass")
	store.CreateUser(db.CreateUserParams{Username: "alice", PasswordHash: hash, Role: "developer"})
	alice, _ := store.GetUserByUsername("alice")

	token, _ := auth.IssueJWT(alice.ID, "alice", "developer", "test-secret")
	req := authedRequest(t, "GET", "/api/users/nobody", nil, token)
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Errorf("expected 404, got %d", rec.Code)
	}
}

func TestGetUser_Unauthenticated(t *testing.T) {
	srv, _ := newTestServer(t)
	req := httptest.NewRequest("GET", "/api/users/alice", nil)
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d", rec.Code)
	}
}

func TestManagerMember_CanManage(t *testing.T) {
	srv, store := newTestServer(t)
	hash, _ := auth.HashPassword("pass")
	store.CreateUser(db.CreateUserParams{Username: "owner", PasswordHash: hash, Role: "developer"})
	store.CreateUser(db.CreateUserParams{Username: "mgr", PasswordHash: hash, Role: "developer"})
	owner, _ := store.GetUserByUsername("owner")
	mgr, _ := store.GetUserByUsername("mgr")

	store.CreateApp(db.CreateAppParams{Slug: "theapp", Name: "The App", OwnerID: owner.ID})
	// Grant mgr access with role=manager via direct DB insert.
	store.GrantAppAccess("theapp", mgr.ID)
	store.SetMemberRole("theapp", mgr.ID, "manager")

	token, _ := auth.IssueJWT(mgr.ID, "mgr", "developer", "test-secret")
	// PATCH /api/apps/{slug} requires manage rights.
	body, _ := json.Marshal(map[string]any{})
	req := authedRequest(t, "PATCH", "/api/apps/theapp", body, token)
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Errorf("manager member: expected 200 on PATCH, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestViewerMember_CannotManage(t *testing.T) {
	srv, store := newTestServer(t)
	hash, _ := auth.HashPassword("pass")
	store.CreateUser(db.CreateUserParams{Username: "owner", PasswordHash: hash, Role: "developer"})
	store.CreateUser(db.CreateUserParams{Username: "viewer", PasswordHash: hash, Role: "developer"})
	owner, _ := store.GetUserByUsername("owner")
	viewer, _ := store.GetUserByUsername("viewer")

	store.CreateApp(db.CreateAppParams{Slug: "theapp2", Name: "The App 2", OwnerID: owner.ID})
	store.GrantAppAccess("theapp2", viewer.ID)
	// viewer has the default role="viewer" — no explicit SetMemberRole needed.

	token, _ := auth.IssueJWT(viewer.ID, "viewer", "developer", "test-secret")
	body, _ := json.Marshal(map[string]any{})
	req := authedRequest(t, "PATCH", "/api/apps/theapp2", body, token)
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Errorf("viewer member: expected 403 on PATCH, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestDeleteApp(t *testing.T) {
	srv, store := newTestServer(t)
	hash, _ := auth.HashPassword("pass")
	store.CreateUser(db.CreateUserParams{Username: "owner", PasswordHash: hash, Role: "developer"})
	u, _ := store.GetUserByUsername("owner")
	store.CreateApp(db.CreateAppParams{Slug: "to-delete", Name: "To Delete", OwnerID: u.ID})
	token, _ := auth.IssueJWT(u.ID, "owner", "developer", "test-secret")

	req := authedRequest(t, "DELETE", "/api/apps/to-delete", nil, token)
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Errorf("expected 204, got %d: %s", rec.Code, rec.Body.String())
	}

	// App should be gone.
	req = authedRequest(t, "GET", "/api/apps/to-delete", nil, token)
	rec = httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Errorf("expected 404 after delete, got %d", rec.Code)
	}
}

func TestStopApp(t *testing.T) {
	srv, store := newTestServer(t)
	hash, _ := auth.HashPassword("pass")
	store.CreateUser(db.CreateUserParams{Username: "owner", PasswordHash: hash, Role: "developer"})
	u, _ := store.GetUserByUsername("owner")
	store.CreateApp(db.CreateAppParams{Slug: "running-app", Name: "Running App", OwnerID: u.ID})
	// Simulate a running status.
	port := 8181
	pid := 12345
	store.UpdateAppStatus(db.UpdateAppStatusParams{Slug: "running-app", Status: "running", Port: &port, PID: &pid})
	token, _ := auth.IssueJWT(u.ID, "owner", "developer", "test-secret")

	req := authedRequest(t, "POST", "/api/apps/running-app/stop", nil, token)
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Errorf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var resp map[string]any
	json.NewDecoder(rec.Body).Decode(&resp)
	if resp["status"] != "stopped" {
		t.Errorf("expected status=stopped, got %v", resp["status"])
	}
}

func TestRollbackPost(t *testing.T) {
	srv, _ := newTestServer(t)
	token, _ := auth.IssueJWT(1, "admin", "admin", "test-secret")
	// No deployments → should get 409 Conflict (no previous deployment).
	// The goal here is just to verify POST is registered, not 405 Method Not Allowed.
	// We'll create an app but leave it undeployed.
	// Since there's no DB entry, we'll get 404 first — that's fine, POST is registered.
	req := authedRequest(t, "POST", "/api/apps/nonexistent/rollback", nil, token)
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)
	// 404 (no such app) proves POST is registered (not 405 Method Not Allowed).
	if rec.Code == http.StatusMethodNotAllowed {
		t.Errorf("POST /rollback should be registered, got 405")
	}
}

func TestRevokeAppAccess_PathParam(t *testing.T) {
	srv, store := newTestServer(t)
	hash, _ := auth.HashPassword("pass")
	store.CreateUser(db.CreateUserParams{Username: "owner", PasswordHash: hash, Role: "developer"})
	store.CreateUser(db.CreateUserParams{Username: "alice", PasswordHash: hash, Role: "viewer"})
	owner, _ := store.GetUserByUsername("owner")
	alice, _ := store.GetUserByUsername("alice")
	store.CreateApp(db.CreateAppParams{Slug: "myapp", Name: "My App", OwnerID: owner.ID})
	store.GrantAppAccess("myapp", alice.ID)

	token, _ := auth.IssueJWT(owner.ID, "owner", "developer", "test-secret")
	path := fmt.Sprintf("/api/apps/myapp/members/%d", alice.ID)
	req := authedRequest(t, "DELETE", path, nil, token)
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("expected 204, got %d: %s", rec.Code, rec.Body.String())
	}

	// Member should be gone.
	members, _ := store.GetAppMembers("myapp")
	if len(members) != 0 {
		t.Errorf("expected 0 members after revoke, got %d", len(members))
	}
}

func TestListDeployments(t *testing.T) {
	srv, store := newTestServer(t)
	hash, _ := auth.HashPassword("pass")
	store.CreateUser(db.CreateUserParams{Username: "owner", PasswordHash: hash, Role: "developer"})
	owner, _ := store.GetUserByUsername("owner")
	store.CreateApp(db.CreateAppParams{Slug: "myapp", Name: "My App", OwnerID: owner.ID})
	app, _ := store.GetAppBySlug("myapp")

	// Insert a deployment row directly.
	_, err := store.DB().Exec(
		`INSERT INTO deployments (app_id, version, bundle_dir, status) VALUES (?, ?, ?, ?)`,
		app.ID, "v1", "/tmp/v1", "pending",
	)
	if err != nil {
		t.Fatalf("insert deployment: %v", err)
	}

	token, _ := auth.IssueJWT(owner.ID, "owner", "developer", "test-secret")
	req := authedRequest(t, "GET", "/api/apps/myapp/deployments", nil, token)
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var deployments []map[string]any
	json.NewDecoder(rec.Body).Decode(&deployments)
	if len(deployments) != 1 {
		t.Fatalf("expected 1 deployment, got %d", len(deployments))
	}
	if deployments[0]["version"] != "v1" {
		t.Errorf("version = %v, want v1", deployments[0]["version"])
	}
}

func TestRollbackApp_ToSpecificDeployment(t *testing.T) {
	srv, store := newTestServer(t)
	hash, _ := auth.HashPassword("pass")
	if err := store.CreateUser(db.CreateUserParams{Username: "owner", PasswordHash: hash, Role: "developer"}); err != nil {
		t.Fatal(err)
	}
	owner, _ := store.GetUserByUsername("owner")
	if err := store.CreateApp(db.CreateAppParams{Slug: "myapp", Name: "My App", OwnerID: owner.ID}); err != nil {
		t.Fatal(err)
	}
	app, _ := store.GetAppBySlug("myapp")

	dep1, err := store.CreateDeployment(db.CreateDeploymentParams{AppID: app.ID, Version: "v1", BundleDir: "/tmp/v1"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateDeployment(db.CreateDeploymentParams{AppID: app.ID, Version: "v2", BundleDir: "/tmp/v2"}); err != nil {
		t.Fatal(err)
	}

	token, _ := auth.IssueJWT(owner.ID, "owner", "developer", "test-secret")
	body, _ := json.Marshal(map[string]any{"deployment_id": dep1.ID})
	req := authedRequest(t, "POST", "/api/apps/myapp/rollback", body, token)
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)

	// No real process manager, so expect 503 — proves the deployment was found
	// and the handler tried to use it. 400/404 would mean it was rejected.
	if rec.Code == http.StatusBadRequest || rec.Code == http.StatusNotFound {
		t.Errorf("unexpected error for valid deployment_id: %d %s", rec.Code, rec.Body.String())
	}
}

func TestRollbackApp_ToInvalidDeployment(t *testing.T) {
	srv, store := newTestServer(t)
	hash, _ := auth.HashPassword("pass")
	if err := store.CreateUser(db.CreateUserParams{Username: "owner", PasswordHash: hash, Role: "developer"}); err != nil {
		t.Fatal(err)
	}
	owner, _ := store.GetUserByUsername("owner")
	if err := store.CreateApp(db.CreateAppParams{Slug: "myapp", Name: "My App", OwnerID: owner.ID}); err != nil {
		t.Fatal(err)
	}

	token, _ := auth.IssueJWT(owner.ID, "owner", "developer", "test-secret")
	body, _ := json.Marshal(map[string]any{"deployment_id": int64(9999)})
	req := authedRequest(t, "POST", "/api/apps/myapp/rollback", body, token)
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Errorf("expected 404 for invalid deployment_id, got %d %s", rec.Code, rec.Body.String())
	}
}

func TestListDeployments_EmptySlice(t *testing.T) {
	srv, store := newTestServer(t)
	hash, _ := auth.HashPassword("pass")
	store.CreateUser(db.CreateUserParams{Username: "owner", PasswordHash: hash, Role: "developer"})
	owner, _ := store.GetUserByUsername("owner")
	store.CreateApp(db.CreateAppParams{Slug: "myapp", Name: "My App", OwnerID: owner.ID})

	token, _ := auth.IssueJWT(owner.ID, "owner", "developer", "test-secret")
	req := authedRequest(t, "GET", "/api/apps/myapp/deployments", nil, token)
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	// Must be [] not null.
	if rec.Body.String() != "[]\n" {
		t.Errorf("expected empty JSON array, got %q", rec.Body.String())
	}
}

func TestPatchApp_UpdateName(t *testing.T) {
	srv, store := newTestServer(t)
	hash, _ := auth.HashPassword("pass")
	store.CreateUser(db.CreateUserParams{Username: "owner", PasswordHash: hash, Role: "developer"})
	owner, _ := store.GetUserByUsername("owner")
	store.CreateApp(db.CreateAppParams{Slug: "myapp", Name: "Old Name", OwnerID: owner.ID})
	token, _ := auth.IssueJWT(owner.ID, "owner", "developer", "test-secret")

	body, _ := json.Marshal(map[string]string{"name": "New Name"})
	req := authedRequest(t, "PATCH", "/api/apps/myapp", body, token)
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var resp map[string]any
	json.NewDecoder(rec.Body).Decode(&resp)
	if resp["name"] != "New Name" {
		t.Errorf("name = %v, want 'New Name'", resp["name"])
	}
	app, _ := store.GetAppBySlug("myapp")
	if app.Name != "New Name" {
		t.Errorf("DB name = %q, want %q", app.Name, "New Name")
	}
}

func TestPatchApp_UpdateProjectSlug(t *testing.T) {
	srv, store := newTestServer(t)
	hash, _ := auth.HashPassword("pass")
	store.CreateUser(db.CreateUserParams{Username: "owner", PasswordHash: hash, Role: "developer"})
	owner, _ := store.GetUserByUsername("owner")
	store.CreateApp(db.CreateAppParams{Slug: "myapp", Name: "My App", OwnerID: owner.ID})
	token, _ := auth.IssueJWT(owner.ID, "owner", "developer", "test-secret")

	// Set project slug.
	body, _ := json.Marshal(map[string]string{"project_slug": "analytics"})
	req := authedRequest(t, "PATCH", "/api/apps/myapp", body, token)
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	app, _ := store.GetAppBySlug("myapp")
	if app.ProjectSlug != "analytics" {
		t.Errorf("project_slug = %q, want %q", app.ProjectSlug, "analytics")
	}

	// Clear project slug with empty string.
	body, _ = json.Marshal(map[string]string{"project_slug": ""})
	req = authedRequest(t, "PATCH", "/api/apps/myapp", body, token)
	rec = httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 on clear, got %d: %s", rec.Code, rec.Body.String())
	}
	app, _ = store.GetAppBySlug("myapp")
	if app.ProjectSlug != "" {
		t.Errorf("project_slug = %q, want empty", app.ProjectSlug)
	}
}

func TestRollbackApp_DeploymentFromOtherApp(t *testing.T) {
	srv, store := newTestServer(t)
	hash, _ := auth.HashPassword("pass")
	store.CreateUser(db.CreateUserParams{Username: "owner", PasswordHash: hash, Role: "developer"})
	owner, _ := store.GetUserByUsername("owner")

	// Create two apps.
	store.CreateApp(db.CreateAppParams{Slug: "app-a", Name: "App A", OwnerID: owner.ID})
	store.CreateApp(db.CreateAppParams{Slug: "app-b", Name: "App B", OwnerID: owner.ID})
	appB, _ := store.GetAppBySlug("app-b")

	// Create a deployment for app-b.
	dep, err := store.CreateDeployment(db.CreateDeploymentParams{
		AppID: appB.ID, Version: "v1", BundleDir: "/tmp/b-v1",
	})
	if err != nil {
		t.Fatal(err)
	}

	// Try to roll back app-a using app-b's deployment ID.
	token, _ := auth.IssueJWT(owner.ID, "owner", "developer", "test-secret")
	body, _ := json.Marshal(map[string]any{"deployment_id": dep.ID})
	req := authedRequest(t, "POST", "/api/apps/app-a/rollback", body, token)
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Errorf("expected 404 when using deployment from another app, got %d %s", rec.Code, rec.Body.String())
	}
}

func TestRollbackApp_NoPreviousDeployment(t *testing.T) {
	srv, store := newTestServer(t)
	hash, _ := auth.HashPassword("pass")
	store.CreateUser(db.CreateUserParams{Username: "owner", PasswordHash: hash, Role: "developer"})
	owner, _ := store.GetUserByUsername("owner")
	store.CreateApp(db.CreateAppParams{Slug: "myapp", Name: "My App", OwnerID: owner.ID})
	// Create exactly one deployment so there's no "previous" to roll back to.
	app, _ := store.GetAppBySlug("myapp")
	store.CreateDeployment(db.CreateDeploymentParams{AppID: app.ID, Version: "v1", BundleDir: "/tmp/v1"})

	token, _ := auth.IssueJWT(owner.ID, "owner", "developer", "test-secret")
	// Empty body = use previous deployment.
	req := authedRequest(t, "POST", "/api/apps/myapp/rollback", nil, token)
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)

	if rec.Code != http.StatusConflict {
		t.Errorf("expected 409 Conflict when no previous deployment, got %d %s", rec.Code, rec.Body.String())
	}
}

func TestCreateApp_DuplicateSlug(t *testing.T) {
	srv, store := newTestServer(t)
	hash, _ := auth.HashPassword("pass")
	store.CreateUser(db.CreateUserParams{Username: "bob", PasswordHash: hash, Role: "developer"})
	token, _ := auth.IssueJWT(1, "bob", "developer", "test-secret")

	body, _ := json.Marshal(map[string]string{"slug": "my-app", "name": "My App"})

	// First create: success
	req := authedRequest(t, "POST", "/api/apps", body, token)
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", rec.Code, rec.Body.String())
	}

	// Second create: conflict
	req = authedRequest(t, "POST", "/api/apps", body, token)
	rec = httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)
	if rec.Code != http.StatusConflict {
		t.Errorf("expected 409, got %d: %s", rec.Code, rec.Body.String())
	}
}
