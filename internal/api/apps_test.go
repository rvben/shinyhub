package api_test

import (
	"bytes"
	"encoding/json"
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

func TestGetApp_ForbiddenWhenNoAccess(t *testing.T) {
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

	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d: %s", rec.Code, rec.Body.String())
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
	req := authedRequest(t, "GET", "/api/users?username=alice", nil, token)
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
	req := authedRequest(t, "GET", "/api/users?username=nobody", nil, token)
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Errorf("expected 404, got %d", rec.Code)
	}
}

func TestGetUser_Unauthenticated(t *testing.T) {
	srv, _ := newTestServer(t)
	req := httptest.NewRequest("GET", "/api/users?username=alice", nil)
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d", rec.Code)
	}
}
