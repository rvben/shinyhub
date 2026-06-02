package api_test

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"fmt"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rvben/shinyhub/internal/api"
	"github.com/rvben/shinyhub/internal/auth"
	"github.com/rvben/shinyhub/internal/config"
	"github.com/rvben/shinyhub/internal/db"
	"github.com/rvben/shinyhub/internal/deploy"
	"github.com/rvben/shinyhub/internal/process"
	"github.com/rvben/shinyhub/internal/proxy"
)

// newManagerTestServer creates a test server with a real in-memory process manager.
func newManagerTestServer(t *testing.T) (*api.Server, *db.Store, *process.Manager) {
	t.Helper()
	appsDir := t.TempDir()
	store, err := db.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Migrate(); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{
		Auth:    config.AuthConfig{Secret: "test-secret"},
		Storage: config.StorageConfig{AppsDir: appsDir},
	}
	mgr := process.NewManager(appsDir, process.NewNativeRuntime())
	srv := api.New(cfg, store, mgr, nil)
	t.Cleanup(func() { store.Close() })
	return srv, store, mgr
}

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

// hibernate_timeout_minutes=0 disables hibernation for the app and is distinct
// from null (use global default). The UI's "Never hibernate" radio sends 0.
func TestPatchApp_NeverHibernate(t *testing.T) {
	srv, store := newTestServer(t)
	hash, _ := auth.HashPassword("pass")
	store.CreateUser(db.CreateUserParams{Username: "bob", PasswordHash: hash, Role: "admin"})
	u, _ := store.GetUserByUsername("bob")
	token, _ := auth.IssueJWT(u.ID, "bob", "admin", "test-secret")
	store.CreateApp(db.CreateAppParams{Slug: "myapp", Name: "My App", OwnerID: u.ID})

	body, _ := json.Marshal(map[string]any{"hibernate_timeout_minutes": 0})
	req := authedRequest(t, "PATCH", "/api/apps/myapp", body, token)
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var resp map[string]any
	json.NewDecoder(rec.Body).Decode(&resp)
	if resp["hibernate_timeout_minutes"] != float64(0) {
		t.Errorf("expected hibernate_timeout_minutes=0 in response, got %v", resp["hibernate_timeout_minutes"])
	}
	app, _ := store.GetAppBySlug("myapp")
	if app.HibernateTimeoutMinutes == nil || *app.HibernateTimeoutMinutes != 0 {
		t.Errorf("expected DB value 0 (never hibernate), got %v", app.HibernateTimeoutMinutes)
	}
}

func TestPatchApp_HibernateTimeoutRejectsNegative(t *testing.T) {
	srv, store := newTestServer(t)
	hash, _ := auth.HashPassword("pass")
	store.CreateUser(db.CreateUserParams{Username: "bob", PasswordHash: hash, Role: "admin"})
	u, _ := store.GetUserByUsername("bob")
	token, _ := auth.IssueJWT(u.ID, "bob", "admin", "test-secret")
	store.CreateApp(db.CreateAppParams{Slug: "myapp", Name: "My App", OwnerID: u.ID})

	body := []byte(`{"hibernate_timeout_minutes": -5}`)
	req := authedRequest(t, "PATCH", "/api/apps/myapp", body, token)
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for negative timeout, got %d: %s", rec.Code, rec.Body.String())
	}
	app, _ := store.GetAppBySlug("myapp")
	if app.HibernateTimeoutMinutes != nil {
		t.Errorf("expected DB unchanged on rejection, got %v", app.HibernateTimeoutMinutes)
	}
}

func TestPatchApp_HibernateTimeoutRejectsNonInteger(t *testing.T) {
	srv, store := newTestServer(t)
	hash, _ := auth.HashPassword("pass")
	store.CreateUser(db.CreateUserParams{Username: "bob", PasswordHash: hash, Role: "admin"})
	u, _ := store.GetUserByUsername("bob")
	token, _ := auth.IssueJWT(u.ID, "bob", "admin", "test-secret")
	store.CreateApp(db.CreateAppParams{Slug: "myapp", Name: "My App", OwnerID: u.ID})

	body := []byte(`{"hibernate_timeout_minutes": "60"}`)
	req := authedRequest(t, "PATCH", "/api/apps/myapp", body, token)
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for string value, got %d: %s", rec.Code, rec.Body.String())
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
	srv, store := newTestServer(t)
	token, _ := seedUserAndJWT(t, store, "admin", "admin")
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
	if rec.Code != http.StatusOK {
		t.Errorf("expected 200, got %d: %s", rec.Code, rec.Body.String())
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
	store.UpdateAppStatus(db.UpdateAppStatusParams{Slug: "running-app", Status: "running"})
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

// TestRollbackApp_MissingBundleRefusesBeforeStop guards against a regression
// where the rollback handler called manager.Stop and proxy.Deregister before
// validating that the target deployment's bundle directory still existed on
// disk. If the bundle had been pruned out from under us the deploy would then
// fail and the live app would already be down with no path back to running.
func TestRollbackApp_MissingBundleRefusesBeforeStop(t *testing.T) {
	srv, store, _ := newManagerTestServer(t)
	hash, _ := auth.HashPassword("pass")
	store.CreateUser(db.CreateUserParams{Username: "owner", PasswordHash: hash, Role: "developer"})
	owner, _ := store.GetUserByUsername("owner")
	store.CreateApp(db.CreateAppParams{Slug: "myapp", Name: "My App", OwnerID: owner.ID})
	app, _ := store.GetAppBySlug("myapp")

	// Older deployment points at a directory that no longer exists.
	missingDir := filepath.Join(t.TempDir(), "deleted-bundle")
	missingDep, err := store.CreateDeployment(db.CreateDeploymentParams{
		AppID: app.ID, Version: "v1", BundleDir: missingDir,
	})
	if err != nil {
		t.Fatal(err)
	}
	// Newer deployment has a real (empty) directory so it's "current".
	currentDir := t.TempDir()
	if _, err := store.CreateDeployment(db.CreateDeploymentParams{
		AppID: app.ID, Version: "v2", BundleDir: currentDir,
	}); err != nil {
		t.Fatal(err)
	}

	token, _ := auth.IssueJWT(owner.ID, "owner", "developer", "test-secret")
	body, _ := json.Marshal(map[string]any{"deployment_id": missingDep.ID})
	req := authedRequest(t, "POST", "/api/apps/myapp/rollback", body, token)
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)

	if rec.Code != http.StatusConflict {
		t.Fatalf("expected 409 when target bundle is missing, got %d %s", rec.Code, rec.Body.String())
	}
}

// TestRollbackApp_BundlePathIsFileRefuses also rejects a deployment whose
// recorded BundleDir resolves to a file rather than a directory — same
// safety reason as above.
func TestRollbackApp_BundlePathIsFileRefuses(t *testing.T) {
	srv, store, _ := newManagerTestServer(t)
	hash, _ := auth.HashPassword("pass")
	store.CreateUser(db.CreateUserParams{Username: "owner", PasswordHash: hash, Role: "developer"})
	owner, _ := store.GetUserByUsername("owner")
	store.CreateApp(db.CreateAppParams{Slug: "myapp", Name: "My App", OwnerID: owner.ID})
	app, _ := store.GetAppBySlug("myapp")

	// "Bundle" is actually a regular file (corrupted state).
	bogus := filepath.Join(t.TempDir(), "not-a-dir")
	if err := os.WriteFile(bogus, []byte("oops"), 0o600); err != nil {
		t.Fatal(err)
	}
	dep, err := store.CreateDeployment(db.CreateDeploymentParams{
		AppID: app.ID, Version: "v1", BundleDir: bogus,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateDeployment(db.CreateDeploymentParams{
		AppID: app.ID, Version: "v2", BundleDir: t.TempDir(),
	}); err != nil {
		t.Fatal(err)
	}

	token, _ := auth.IssueJWT(owner.ID, "owner", "developer", "test-secret")
	body, _ := json.Marshal(map[string]any{"deployment_id": dep.ID})
	req := authedRequest(t, "POST", "/api/apps/myapp/rollback", body, token)
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)

	if rec.Code != http.StatusConflict {
		t.Fatalf("expected 409 when bundle path is a file, got %d %s", rec.Code, rec.Body.String())
	}
}

// loginAsAdmin returns a valid JWT token for user ID 1 with the admin role.
// The caller must have already created the admin user in the store before calling this.
func loginAsAdmin(t *testing.T, _ *api.Server) string {
	t.Helper()
	token, err := auth.IssueJWT(1, "admin", "admin", "test-secret")
	if err != nil {
		t.Fatalf("loginAsAdmin: IssueJWT: %v", err)
	}
	return token
}

// createApp creates an app with the given slug via the API and fails the test on error.
func createApp(t *testing.T, srv *api.Server, token, slug string) {
	t.Helper()
	body, _ := json.Marshal(map[string]string{"slug": slug, "name": slug})
	req := httptest.NewRequest(http.MethodPost, "/api/apps", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	srv.Router().ServeHTTP(rr, req)
	if rr.Code != http.StatusCreated {
		t.Fatalf("createApp(%q): expected 201, got %d: %s", slug, rr.Code, rr.Body.String())
	}
}

func TestPatchAppResourceLimits(t *testing.T) {
	srv, store := newTestServer(t)
	hash, _ := auth.HashPassword("pass")
	store.CreateUser(db.CreateUserParams{Username: "admin", PasswordHash: hash, Role: "admin"})
	token := loginAsAdmin(t, srv)
	createApp(t, srv, token, "my-app")

	// Set limits.
	patch := map[string]any{"memory_limit_mb": 256, "cpu_quota_percent": 50}
	body, _ := json.Marshal(patch)
	req := httptest.NewRequest(http.MethodPatch, "/api/apps/my-app", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	srv.Router().ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body.String())
	}

	// Verify via GET.
	req = httptest.NewRequest(http.MethodGet, "/api/apps/my-app", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rr = httptest.NewRecorder()
	srv.Router().ServeHTTP(rr, req)
	var getResp map[string]any
	json.NewDecoder(rr.Body).Decode(&getResp)
	app := getResp["app"].(map[string]any)
	if app["memory_limit_mb"] != float64(256) {
		t.Errorf("expected memory_limit_mb=256, got %v", app["memory_limit_mb"])
	}
	if app["cpu_quota_percent"] != float64(50) {
		t.Errorf("expected cpu_quota_percent=50, got %v", app["cpu_quota_percent"])
	}
}

func TestPatchAppResourceLimitsClear(t *testing.T) {
	srv, store := newTestServer(t)
	hash, _ := auth.HashPassword("pass")
	store.CreateUser(db.CreateUserParams{Username: "admin", PasswordHash: hash, Role: "admin"})
	token := loginAsAdmin(t, srv)
	createApp(t, srv, token, "my-app")

	// Set then clear.
	for _, patch := range []map[string]any{
		{"memory_limit_mb": 256},
		{"memory_limit_mb": nil},
	} {
		body, _ := json.Marshal(patch)
		req := httptest.NewRequest(http.MethodPatch, "/api/apps/my-app", bytes.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Content-Type", "application/json")
		rr := httptest.NewRecorder()
		srv.Router().ServeHTTP(rr, req)
		if rr.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d", rr.Code)
		}
	}

	req := httptest.NewRequest(http.MethodGet, "/api/apps/my-app", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rr := httptest.NewRecorder()
	srv.Router().ServeHTTP(rr, req)
	var getResp map[string]any
	json.NewDecoder(rr.Body).Decode(&getResp)
	app := getResp["app"].(map[string]any)
	if app["memory_limit_mb"] != nil {
		t.Errorf("expected null memory_limit_mb after clear, got %v", app["memory_limit_mb"])
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

// TestAppsAPI_GetIncludesReplicasStatus verifies that GET /api/apps/:slug returns
// a wrapped response with both "app" and "replicas_status" fields.
// Live manager state is merged into the DB rows when the manager is populated.
func TestAppsAPI_GetIncludesReplicasStatus(t *testing.T) {
	srv, store, mgr := newManagerTestServer(t)
	hash, _ := auth.HashPassword("pass")
	store.CreateUser(db.CreateUserParams{Username: "owner", PasswordHash: hash, Role: "developer"})
	owner, _ := store.GetUserByUsername("owner")
	store.CreateApp(db.CreateAppParams{Slug: "demo", Name: "Demo", OwnerID: owner.ID})
	app, _ := store.GetAppBySlug("demo")

	// Upsert two replica DB rows.
	pid0, port0 := 111, 20001
	pid1, port1 := 222, 20002
	store.UpsertReplica(db.UpsertReplicaParams{AppID: app.ID, Index: 0, PID: &pid0, Port: &port0, Status: "running"})
	store.UpsertReplica(db.UpsertReplicaParams{AppID: app.ID, Index: 1, PID: &pid1, Port: &port1, Status: "running"})

	// Inject live state into the manager for one replica so the merge is exercised.
	mgr.ForceEntry("demo", process.ProcessInfo{Slug: "demo", Index: 0, PID: 111, Port: 20001, Status: process.StatusRunning})

	token, _ := auth.IssueJWT(owner.ID, "owner", "developer", "test-secret")
	req := authedRequest(t, "GET", "/api/apps/demo", nil, token)
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var body struct {
		App            *db.App       `json:"app"`
		ReplicasStatus []*db.Replica `json:"replicas_status"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if body.App == nil {
		t.Fatal("expected app in response, got nil")
	}
	if body.App.Slug != "demo" {
		t.Errorf("app.slug = %q, want %q", body.App.Slug, "demo")
	}
	if len(body.ReplicasStatus) != 2 {
		t.Fatalf("want 2 replicas_status entries, got %d", len(body.ReplicasStatus))
	}
	// Replica 0: live state should be merged in.
	if body.ReplicasStatus[0].Status != "running" {
		t.Errorf("replica 0 status = %q, want running", body.ReplicasStatus[0].Status)
	}
	if body.ReplicasStatus[0].PID == nil || *body.ReplicasStatus[0].PID != 111 {
		t.Errorf("replica 0 PID = %v, want 111", body.ReplicasStatus[0].PID)
	}
}

// newManagerTestServerWithMaxReplicas creates a test server with a specific MaxReplicas cap.
func newManagerTestServerWithMaxReplicas(t *testing.T, maxReplicas int) (*api.Server, *db.Store) {
	t.Helper()
	appsDir := t.TempDir()
	store, err := db.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Migrate(); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{
		Auth:    config.AuthConfig{Secret: "test-secret"},
		Storage: config.StorageConfig{AppsDir: appsDir},
		Runtime: config.RuntimeConfig{MaxReplicas: maxReplicas},
	}
	srv := api.New(cfg, store, nil, nil)
	t.Cleanup(func() { store.Close() })
	return srv, store
}

// newTestServerWithTiers builds a server whose runtime declares two tiers
// (local/native default + burst/docker), so tests can exercise per-tier
// placement validation on PATCH.
func newTestServerWithTiers(t *testing.T) (*api.Server, *db.Store) {
	t.Helper()
	appsDir := t.TempDir()
	store, err := db.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Migrate(); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{
		Auth:    config.AuthConfig{Secret: "test-secret"},
		Storage: config.StorageConfig{AppsDir: appsDir},
		Runtime: config.RuntimeConfig{
			MaxReplicas: 8,
			Tiers: []config.TierConfig{
				{Name: "local", Runtime: "native"},
				{Name: "burst", Runtime: "docker"},
			},
		},
	}
	srv := api.New(cfg, store, nil, nil)
	t.Cleanup(func() { store.Close() })
	return srv, store
}

// newManagerTestServerWithRuntimeMode builds a server whose runtime mode is set
// explicitly, so tests can exercise behavior that branches on native vs docker.
func newManagerTestServerWithRuntimeMode(t *testing.T, mode string) (*api.Server, *db.Store) {
	t.Helper()
	appsDir := t.TempDir()
	store, err := db.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Migrate(); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{
		Auth:    config.AuthConfig{Secret: "test-secret"},
		Storage: config.StorageConfig{AppsDir: appsDir},
		Runtime: config.RuntimeConfig{Mode: mode},
	}
	srv := api.New(cfg, store, process.NewManager(appsDir, process.NewNativeRuntime()), nil)
	t.Cleanup(func() { store.Close() })
	return srv, store
}

// newTestServerWithDefaultReplicas creates a test server with a specific DefaultReplicas value.
func newTestServerWithDefaultReplicas(t *testing.T, defaultReplicas int) (*api.Server, *db.Store) {
	t.Helper()
	appsDir := t.TempDir()
	store, err := db.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Migrate(); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{
		Auth:    config.AuthConfig{Secret: "test-secret"},
		Storage: config.StorageConfig{AppsDir: appsDir},
		Runtime: config.RuntimeConfig{DefaultReplicas: defaultReplicas, MaxReplicas: 32},
	}
	srv := api.New(cfg, store, nil, nil)
	t.Cleanup(func() { store.Close() })
	return srv, store
}

// newTestServerWithDefaultMaxSessions creates a test server with a specific
// runtime DefaultMaxSessionsPerReplica.
func newTestServerWithDefaultMaxSessions(t *testing.T, def int) (*api.Server, *db.Store) {
	t.Helper()
	appsDir := t.TempDir()
	store, err := db.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Migrate(); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{
		Auth:    config.AuthConfig{Secret: "test-secret"},
		Storage: config.StorageConfig{AppsDir: appsDir},
		Runtime: config.RuntimeConfig{MaxReplicas: 32, DefaultMaxSessionsPerReplica: def},
	}
	srv := api.New(cfg, store, nil, nil)
	t.Cleanup(func() { store.Close() })
	return srv, store
}

// DEP-5: GET /api/apps/{slug} must expose the effective per-replica session cap
// (the resolved value, falling back to the runtime default when the app's own
// value is 0) so clients can render an honest admission ceiling instead of a
// bare "0".
func TestAppsAPI_GetApp_ExposesEffectiveMaxSessions(t *testing.T) {
	srv, store := newTestServerWithDefaultMaxSessions(t, 10)
	hash, _ := auth.HashPassword("pass")
	store.CreateUser(db.CreateUserParams{Username: "owner", PasswordHash: hash, Role: "developer"})
	owner, _ := store.GetUserByUsername("owner")
	// App's own cap stays 0 (inherit the runtime default).
	store.CreateApp(db.CreateAppParams{Slug: "demo", Name: "Demo", OwnerID: owner.ID})
	token, _ := auth.IssueJWT(owner.ID, "owner", "developer", "test-secret")

	req := authedRequest(t, "GET", "/api/apps/demo", nil, token)
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	eff, ok := body["effective_max_sessions_per_replica"]
	if !ok {
		t.Fatal("response missing effective_max_sessions_per_replica")
	}
	if int(eff.(float64)) != 10 {
		t.Errorf("effective_max_sessions_per_replica = %v, want 10 (runtime default)", eff)
	}
}

// TestAppsAPI_PatchReplicasUpdatesCount verifies that PATCH with {"replicas": N}
// updates the DB and does not trigger a redeploy when the app is stopped.
func TestAppsAPI_PatchReplicasUpdatesCount(t *testing.T) {
	srv, store := newTestServer(t)
	hash, _ := auth.HashPassword("pass")
	store.CreateUser(db.CreateUserParams{Username: "owner", PasswordHash: hash, Role: "developer"})
	owner, _ := store.GetUserByUsername("owner")
	store.CreateApp(db.CreateAppParams{Slug: "demo", Name: "Demo", OwnerID: owner.ID})
	// Keep status as "stopped" (default) to avoid triggering async redeployApp.

	token, _ := auth.IssueJWT(owner.ID, "owner", "developer", "test-secret")
	body, _ := json.Marshal(map[string]any{"replicas": 3})
	req := authedRequest(t, "PATCH", "/api/apps/demo", body, token)
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	got, err := store.GetAppBySlug("demo")
	if err != nil {
		t.Fatalf("get app: %v", err)
	}
	if got.Replicas != 3 {
		t.Errorf("want replicas=3, got %d", got.Replicas)
	}
}

// TestAppsAPI_PatchReplicasAboveMaxRejected verifies that PATCH with a replica
// count exceeding MaxReplicas returns 400.
func TestAppsAPI_PatchReplicasAboveMaxRejected(t *testing.T) {
	srv, store := newManagerTestServerWithMaxReplicas(t, 8)
	hash, _ := auth.HashPassword("pass")
	store.CreateUser(db.CreateUserParams{Username: "owner", PasswordHash: hash, Role: "developer"})
	owner, _ := store.GetUserByUsername("owner")
	store.CreateApp(db.CreateAppParams{Slug: "demo", Name: "Demo", OwnerID: owner.ID})

	token, _ := auth.IssueJWT(owner.ID, "owner", "developer", "test-secret")
	body, _ := json.Marshal(map[string]any{"replicas": 100})
	req := authedRequest(t, "PATCH", "/api/apps/demo", body, token)
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for over-cap, got %d: %s", rec.Code, rec.Body.String())
	}
}

// TestAppsAPI_PatchReplicasBelowMinRejected verifies that PATCH with replicas=0 returns 400.
func TestAppsAPI_PatchReplicasBelowMinRejected(t *testing.T) {
	srv, store := newTestServer(t)
	hash, _ := auth.HashPassword("pass")
	store.CreateUser(db.CreateUserParams{Username: "owner", PasswordHash: hash, Role: "developer"})
	owner, _ := store.GetUserByUsername("owner")
	store.CreateApp(db.CreateAppParams{Slug: "demo", Name: "Demo", OwnerID: owner.ID})

	token, _ := auth.IssueJWT(owner.ID, "owner", "developer", "test-secret")
	body, _ := json.Marshal(map[string]any{"replicas": 0})
	req := authedRequest(t, "PATCH", "/api/apps/demo", body, token)
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for replicas=0, got %d: %s", rec.Code, rec.Body.String())
	}
}

// patchTierApp is a small helper for the placement tests: it spins up a
// tier-configured server, creates an owner + app, and returns everything needed
// to issue authenticated PATCH requests.
func patchTierApp(t *testing.T) (*api.Server, *db.Store, *db.App, string) {
	t.Helper()
	srv, store := newTestServerWithTiers(t)
	hash, _ := auth.HashPassword("pass")
	store.CreateUser(db.CreateUserParams{Username: "owner", PasswordHash: hash, Role: "developer"})
	owner, _ := store.GetUserByUsername("owner")
	store.CreateApp(db.CreateAppParams{Slug: "demo", Name: "Demo", OwnerID: owner.ID})
	app, _ := store.GetAppBySlug("demo")
	token, _ := auth.IssueJWT(owner.ID, "owner", "developer", "test-secret")
	return srv, store, app, token
}

func patchDemo(t *testing.T, srv *api.Server, token string, payload map[string]any) *httptest.ResponseRecorder {
	t.Helper()
	body, _ := json.Marshal(payload)
	req := authedRequest(t, "PATCH", "/api/apps/demo", body, token)
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)
	return rec
}

// TestPatchApp_SetPlacement persists a per-tier placement and derives the total
// replica count from the placement sum.
func TestPatchApp_SetPlacement(t *testing.T) {
	srv, store, _, token := patchTierApp(t)

	rec := patchDemo(t, srv, token, map[string]any{"placement": map[string]int{"local": 2, "burst": 1}})
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	got, _ := store.GetAppBySlug("demo")
	if got.Replicas != 3 {
		t.Errorf("want replicas=3 (placement sum), got %d", got.Replicas)
	}
	pm := got.PlacementMap()
	if pm["local"] != 2 || pm["burst"] != 1 {
		t.Errorf("want placement {local:2, burst:1}, got %+v", pm)
	}
}

// TestPatchApp_ClearPlacement clears a stored placement with an explicit null,
// preserving the current replica count (all replicas fall back to the default tier).
func TestPatchApp_ClearPlacement(t *testing.T) {
	srv, store, app, token := patchTierApp(t)
	if err := store.SetAppPlacement(app.ID, `{"local":2,"burst":1}`, 3); err != nil {
		t.Fatalf("seed placement: %v", err)
	}

	rec := patchDemo(t, srv, token, map[string]any{"placement": nil})
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	got, _ := store.GetAppBySlug("demo")
	if len(got.PlacementMap()) != 0 {
		t.Errorf("expected placement cleared, got %+v", got.PlacementMap())
	}
	if got.Replicas != 3 {
		t.Errorf("expected replica count preserved at 3, got %d", got.Replicas)
	}
}

// TestPatchApp_PlacementUnknownTierRejected rejects a placement naming a tier
// that is not configured.
func TestPatchApp_PlacementUnknownTierRejected(t *testing.T) {
	srv, _, _, token := patchTierApp(t)
	rec := patchDemo(t, srv, token, map[string]any{"placement": map[string]int{"nope": 1}})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for unknown tier, got %d: %s", rec.Code, rec.Body.String())
	}
}

// TestPatchApp_PlacementAndReplicasConflict rejects a request that sets both
// replicas and placement (they both describe the pool shape).
func TestPatchApp_PlacementAndReplicasConflict(t *testing.T) {
	srv, _, _, token := patchTierApp(t)
	rec := patchDemo(t, srv, token, map[string]any{
		"placement": map[string]int{"local": 1},
		"replicas":  2,
	})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for replicas+placement, got %d: %s", rec.Code, rec.Body.String())
	}
}

// TestPatchApp_PlacementZeroTotalRejected rejects a placement whose counts sum to zero.
func TestPatchApp_PlacementZeroTotalRejected(t *testing.T) {
	srv, _, _, token := patchTierApp(t)
	rec := patchDemo(t, srv, token, map[string]any{"placement": map[string]int{"local": 0, "burst": 0}})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for zero-total placement, got %d: %s", rec.Code, rec.Body.String())
	}
}

// TestPatchApp_PlacementAboveMaxRejected rejects a placement whose total exceeds MaxReplicas.
func TestPatchApp_PlacementAboveMaxRejected(t *testing.T) {
	srv, _, _, token := patchTierApp(t) // MaxReplicas=8
	rec := patchDemo(t, srv, token, map[string]any{"placement": map[string]int{"local": 5, "burst": 4}})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for over-cap placement, got %d: %s", rec.Code, rec.Body.String())
	}
}

// TestPatchApp_ReplicasRejectedWhenPlacementStored rejects a bare replicas change
// on an app that already uses tier placement, so the stored placement cannot
// drift out of sync with the replica count.
func TestPatchApp_ReplicasRejectedWhenPlacementStored(t *testing.T) {
	srv, store, app, token := patchTierApp(t)
	if err := store.SetAppPlacement(app.ID, `{"local":2}`, 2); err != nil {
		t.Fatalf("seed placement: %v", err)
	}
	rec := patchDemo(t, srv, token, map[string]any{"replicas": 5})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for replicas on placement app, got %d: %s", rec.Code, rec.Body.String())
	}
}

// TestAppsAPI_CreateAppRespectsDefaultReplicas verifies that a newly created app
// gets the replica count from cfg.Runtime.DefaultReplicas when it is greater than 1.
func TestAppsAPI_CreateAppRespectsDefaultReplicas(t *testing.T) {
	srv, store := newTestServerWithDefaultReplicas(t, 4)
	hash, _ := auth.HashPassword("pass")
	store.CreateUser(db.CreateUserParams{Username: "owner", PasswordHash: hash, Role: "developer"})
	owner, _ := store.GetUserByUsername("owner")

	token, _ := auth.IssueJWT(owner.ID, "owner", "developer", "test-secret")
	body, _ := json.Marshal(map[string]any{"slug": "demo", "name": "Demo"})
	req := authedRequest(t, "POST", "/api/apps", body, token)
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", rec.Code, rec.Body.String())
	}

	got, err := store.GetAppBySlug("demo")
	if err != nil {
		t.Fatalf("get app: %v", err)
	}
	if got.Replicas != 4 {
		t.Errorf("want replicas=4 (from DefaultReplicas config), got %d", got.Replicas)
	}
}

func TestCreateApp_RejectsLingeringDataDir(t *testing.T) {
	srv, store := newTestServer(t)
	hash, _ := auth.HashPassword("pass")
	store.CreateUser(db.CreateUserParams{Username: "admin", PasswordHash: hash, Role: "admin"})
	token, _ := auth.IssueJWT(1, "admin", "admin", "test-secret")

	cfg := srv.Config()
	leftover := filepath.Join(cfg.Storage.AppDataDir, "demo")
	if err := os.MkdirAll(leftover, 0o750); err != nil {
		t.Fatal(err)
	}

	body := []byte(`{"slug":"demo","name":"Demo"}`)
	req := authedRequest(t, "POST", "/api/apps", body, token)
	rr := httptest.NewRecorder()
	srv.Router().ServeHTTP(rr, req)
	if rr.Code != http.StatusConflict {
		t.Fatalf("status = %d body=%s, want 409", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "demo") {
		t.Errorf("body should mention slug: %s", rr.Body.String())
	}
}

func TestCreateApp_RejectsLingeringAppsDir(t *testing.T) {
	srv, store := newTestServer(t)
	hash, _ := auth.HashPassword("pass")
	store.CreateUser(db.CreateUserParams{Username: "admin", PasswordHash: hash, Role: "admin"})
	token, _ := auth.IssueJWT(1, "admin", "admin", "test-secret")

	cfg := srv.Config()
	leftover := filepath.Join(cfg.Storage.AppsDir, "demo")
	if err := os.MkdirAll(leftover, 0o750); err != nil {
		t.Fatal(err)
	}

	body := []byte(`{"slug":"demo","name":"Demo"}`)
	req := authedRequest(t, "POST", "/api/apps", body, token)
	rr := httptest.NewRecorder()
	srv.Router().ServeHTTP(rr, req)
	if rr.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409", rr.Code)
	}
}

// TestDeployApp_RejectsRAppOnFargateTier verifies that deploying an R app
// (app.R) onto a Fargate tier is rejected with 400 before the running pool is
// touched: the reference Fargate runner image is Python-only, so the task would
// otherwise start and fail at exec with a cryptic error.
func TestDeployApp_RejectsRAppOnFargateTier(t *testing.T) {
	appsDir := t.TempDir()
	store, err := db.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if err := store.Migrate(); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{
		Auth:    config.AuthConfig{Secret: "test-secret"},
		Storage: config.StorageConfig{AppsDir: appsDir, VersionRetention: 5},
		Runtime: config.RuntimeConfig{
			Tiers: []config.TierConfig{{Name: "prod", Runtime: "fargate"}},
		},
	}
	mgr := process.NewManager(appsDir, process.NewNativeRuntime())
	srv := api.New(cfg, store, mgr, proxy.New())
	// If the guard is missing, the deploy would proceed to the deploy hook;
	// stub it to fail fast so a regression surfaces as a non-400 status rather
	// than actually launching a process.
	srv.SetDeployRunForTest(func(deploy.Params) (*deploy.PoolResult, error) {
		return nil, fmt.Errorf("stub: deploy hook must not be reached for a rejected R-on-Fargate deploy")
	})

	hash, _ := auth.HashPassword("pass")
	store.CreateUser(db.CreateUserParams{Username: "admin", PasswordHash: hash, Role: "admin"})
	token, _ := auth.IssueJWT(1, "admin", "admin", "test-secret")
	createApp(t, srv, token, "demo")

	// A valid R bundle (app.R only) that ExtractBundle accepts.
	var zipBuf bytes.Buffer
	zw := zip.NewWriter(&zipBuf)
	w, err := zw.Create("app.R")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write([]byte("library(shiny)\n")); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}

	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	part, _ := mw.CreateFormFile("bundle", "bundle.zip")
	part.Write(zipBuf.Bytes())
	mw.Close()

	req := httptest.NewRequest(http.MethodPost, "/api/apps/demo/deploy", &body)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.Header.Set("Authorization", "Bearer "+token)
	rr := httptest.NewRecorder()
	srv.Router().ServeHTTP(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body = %s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "Fargate") {
		t.Errorf("body = %s, want mention of 'Fargate'", rr.Body.String())
	}
}

// TestDeployApp_RejectsDataEntry verifies that a bundle containing a data/
// directory is rejected with 422 Unprocessable Entity, not 500.
func TestDeployApp_RejectsDataEntry(t *testing.T) {
	appsDir := t.TempDir()
	srv, store := newQuotaTestServer(t, appsDir, 0)
	hash, _ := auth.HashPassword("pass")
	store.CreateUser(db.CreateUserParams{Username: "admin", PasswordHash: hash, Role: "admin"})
	token, _ := auth.IssueJWT(1, "admin", "admin", "test-secret")
	createApp(t, srv, token, "demo")

	// Build a zip containing a data/ entry, which bundle.Inspect rejects.
	var zipBuf bytes.Buffer
	zw := zip.NewWriter(&zipBuf)
	for name, body := range map[string]string{
		"app.R":      "x",
		"data/x.csv": "a,b",
	} {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write([]byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}

	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	part, err := mw.CreateFormFile("bundle", "bundle.zip")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := part.Write(zipBuf.Bytes()); err != nil {
		t.Fatal(err)
	}
	if err := mw.Close(); err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodPost, "/api/apps/demo/deploy", &body)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.Header.Set("Authorization", "Bearer "+token)
	rr := httptest.NewRecorder()
	srv.Router().ServeHTTP(rr, req)

	if rr.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422; body = %s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "data") {
		t.Errorf("body = %s, want mention of 'data'", rr.Body.String())
	}
}

// TestDeployApp_OrphanCleanupOnExtractFailure ensures that when ExtractBundle
// rejects an upload, the saved bundle zip and any partially-extracted version
// directory are removed from disk. Otherwise repeated bad uploads would
// silently fill the apps tree.
func TestDeployApp_OrphanCleanupOnExtractFailure(t *testing.T) {
	appsDir := t.TempDir()
	srv, store := newQuotaTestServer(t, appsDir, 0)
	hash, _ := auth.HashPassword("pass")
	store.CreateUser(db.CreateUserParams{Username: "admin", PasswordHash: hash, Role: "admin"})
	token, _ := auth.IssueJWT(1, "admin", "admin", "test-secret")
	createApp(t, srv, token, "demo")

	// Bundle with a data/ entry — bundle.Inspect inside ExtractBundle returns
	// ErrBundleRejected and the handler responds 422.
	var zipBuf bytes.Buffer
	zw := zip.NewWriter(&zipBuf)
	for _, name := range []string{"app.R", "data/x.csv"} {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write([]byte("x")); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	part, _ := mw.CreateFormFile("bundle", "bundle.zip")
	part.Write(zipBuf.Bytes())
	mw.Close()

	req := httptest.NewRequest(http.MethodPost, "/api/apps/demo/deploy", &body)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.Header.Set("Authorization", "Bearer "+token)
	rr := httptest.NewRecorder()
	srv.Router().ServeHTTP(rr, req)

	if rr.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422; body = %s", rr.Code, rr.Body.String())
	}

	versionsDir := filepath.Join(appsDir, "demo", "versions")
	if entries, err := os.ReadDir(versionsDir); err == nil && len(entries) > 0 {
		names := make([]string, len(entries))
		for i, e := range entries {
			names[i] = e.Name()
		}
		t.Errorf("expected versions/ to be empty after rejected extract, found: %v", names)
	}

	bundlesDir := filepath.Join(appsDir, "demo", "bundles")
	if entries, err := os.ReadDir(bundlesDir); err == nil && len(entries) > 0 {
		names := make([]string, len(entries))
		for i, e := range entries {
			names[i] = e.Name()
		}
		t.Errorf("expected bundles/ to be empty after rejected extract, found: %v", names)
	}
}

// newTestServerWithDefaultVisibility creates a test server with a specific default app visibility.
func newTestServerWithDefaultVisibility(t *testing.T, visibility string) (*api.Server, *db.Store) {
	t.Helper()
	store, err := db.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Migrate(); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{
		Auth:     config.AuthConfig{Secret: "test-secret"},
		Storage:  config.StorageConfig{AppsDir: t.TempDir(), AppDataDir: t.TempDir()},
		Defaults: config.DefaultsConfig{AppVisibility: visibility},
	}
	srv := api.New(cfg, store, nil, nil)
	t.Cleanup(func() { store.Close() })
	return srv, store
}

// TestCreateApp_DefaultVisibilityPrivate verifies that when no config default is set,
// newly created apps get access=private.
func TestCreateApp_DefaultVisibilityPrivate(t *testing.T) {
	srv, store := newTestServer(t)
	hash, _ := auth.HashPassword("pass")
	store.CreateUser(db.CreateUserParams{Username: "bob", PasswordHash: hash, Role: "developer"})
	u, _ := store.GetUserByUsername("bob")
	token, _ := auth.IssueJWT(u.ID, "bob", "developer", "test-secret")

	body, _ := json.Marshal(map[string]string{"slug": "vis-test", "name": "Vis Test"})
	req := authedRequest(t, "POST", "/api/apps", body, token)
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", rec.Code, rec.Body.String())
	}
	app, err := store.GetAppBySlug("vis-test")
	if err != nil {
		t.Fatalf("get app: %v", err)
	}
	if app.Access != "private" {
		t.Errorf("want access=private (default), got %q", app.Access)
	}
}

// TestCreateApp_ConfigDefaultVisibilityPublic verifies that defaults.app_visibility=public
// is applied to newly created apps when no per-request access is specified.
func TestCreateApp_ConfigDefaultVisibilityPublic(t *testing.T) {
	srv, store := newTestServerWithDefaultVisibility(t, "public")
	hash, _ := auth.HashPassword("pass")
	store.CreateUser(db.CreateUserParams{Username: "bob", PasswordHash: hash, Role: "developer"})
	u, _ := store.GetUserByUsername("bob")
	token, _ := auth.IssueJWT(u.ID, "bob", "developer", "test-secret")

	body, _ := json.Marshal(map[string]string{"slug": "pub-app", "name": "Public App"})
	req := authedRequest(t, "POST", "/api/apps", body, token)
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", rec.Code, rec.Body.String())
	}
	app, err := store.GetAppBySlug("pub-app")
	if err != nil {
		t.Fatalf("get app: %v", err)
	}
	if app.Access != "public" {
		t.Errorf("want access=public (from config default), got %q", app.Access)
	}
}

// TestCreateApp_ExplicitAccessOverridesConfigDefault verifies that an explicit
// access value in the request body overrides the config default.
func TestCreateApp_ExplicitAccessOverridesConfigDefault(t *testing.T) {
	srv, store := newTestServerWithDefaultVisibility(t, "public")
	hash, _ := auth.HashPassword("pass")
	store.CreateUser(db.CreateUserParams{Username: "bob", PasswordHash: hash, Role: "developer"})
	u, _ := store.GetUserByUsername("bob")
	token, _ := auth.IssueJWT(u.ID, "bob", "developer", "test-secret")

	body, _ := json.Marshal(map[string]string{"slug": "priv-app", "name": "Private App", "access": "private"})
	req := authedRequest(t, "POST", "/api/apps", body, token)
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", rec.Code, rec.Body.String())
	}
	app, err := store.GetAppBySlug("priv-app")
	if err != nil {
		t.Fatalf("get app: %v", err)
	}
	if app.Access != "private" {
		t.Errorf("want access=private (explicit override), got %q", app.Access)
	}
}

// TestCreateApp_InvalidAccessRejected verifies that an invalid access value in
// the request body is rejected with 400.
func TestCreateApp_InvalidAccessRejected(t *testing.T) {
	srv, store := newTestServer(t)
	hash, _ := auth.HashPassword("pass")
	store.CreateUser(db.CreateUserParams{Username: "bob", PasswordHash: hash, Role: "developer"})
	u, _ := store.GetUserByUsername("bob")
	token, _ := auth.IssueJWT(u.ID, "bob", "developer", "test-secret")

	body, _ := json.Marshal(map[string]string{"slug": "bad-app", "name": "Bad App", "access": "secret"})
	req := authedRequest(t, "POST", "/api/apps", body, token)
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for invalid access, got %d: %s", rec.Code, rec.Body.String())
	}
}

// TestCreateApp_ConfigDefaultVisibilityShared verifies that defaults.app_visibility=shared
// is applied to newly created apps.
func TestCreateApp_ConfigDefaultVisibilityShared(t *testing.T) {
	srv, store := newTestServerWithDefaultVisibility(t, "shared")
	hash, _ := auth.HashPassword("pass")
	store.CreateUser(db.CreateUserParams{Username: "bob", PasswordHash: hash, Role: "developer"})
	u, _ := store.GetUserByUsername("bob")
	token, _ := auth.IssueJWT(u.ID, "bob", "developer", "test-secret")

	body, _ := json.Marshal(map[string]string{"slug": "shared-app", "name": "Shared App"})
	req := authedRequest(t, "POST", "/api/apps", body, token)
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", rec.Code, rec.Body.String())
	}
	app, err := store.GetAppBySlug("shared-app")
	if err != nil {
		t.Fatalf("get app: %v", err)
	}
	if app.Access != "shared" {
		t.Errorf("want access=shared (from config default), got %q", app.Access)
	}
}

// TestDeleteApp_RemovesBothDirs verifies that deleting an app removes both the
// apps dir (code) and the app-data dir (persistent data) from disk.
func TestDeleteApp_RemovesBothDirs(t *testing.T) {
	srv, store := newTestServer(t)
	hash, _ := auth.HashPassword("pass")
	store.CreateUser(db.CreateUserParams{Username: "admin", PasswordHash: hash, Role: "admin"})
	token, _ := auth.IssueJWT(1, "admin", "admin", "test-secret")
	createApp(t, srv, token, "demo")

	cfg := srv.Config()
	appsPath := filepath.Join(cfg.Storage.AppsDir, "demo")
	dataPath := filepath.Join(cfg.Storage.AppDataDir, "demo")
	if err := os.MkdirAll(appsPath, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(dataPath, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dataPath, "x.txt"), []byte("hi"), 0o640); err != nil {
		t.Fatal(err)
	}

	rr := httptest.NewRecorder()
	srv.Router().ServeHTTP(rr, authedRequest(t, "DELETE", "/api/apps/demo", nil, token))
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rr.Code, rr.Body.String())
	}
	if _, err := os.Stat(appsPath); !os.IsNotExist(err) {
		t.Errorf("apps dir still present: %v", err)
	}
	if _, err := os.Stat(dataPath); !os.IsNotExist(err) {
		t.Errorf("data dir still present: %v", err)
	}
}

// TestDeleteApp_CleanupFailureRetainsTombstone verifies the delete ordering
// contract: if on-disk cleanup fails, the row is NOT removed but left in the
// 'deleting' tombstone state so startup reconciliation can finish it, rather
// than dropping the row and orphaning bytes with no owning quota.
func TestDeleteApp_CleanupFailureRetainsTombstone(t *testing.T) {
	srv, store := newTestServer(t)
	hash, _ := auth.HashPassword("pass")
	store.CreateUser(db.CreateUserParams{Username: "admin", PasswordHash: hash, Role: "admin"})
	token, _ := auth.IssueJWT(1, "admin", "admin", "test-secret")
	createApp(t, srv, token, "demo")

	cfg := srv.Config()
	appsPath := filepath.Join(cfg.Storage.AppsDir, "demo")
	if err := os.MkdirAll(appsPath, 0o750); err != nil {
		t.Fatal(err)
	}
	// Make the parent read-only so removing demo/ inside it fails (EACCES).
	if err := os.Chmod(cfg.Storage.AppsDir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(cfg.Storage.AppsDir, 0o750) })

	rr := httptest.NewRecorder()
	srv.Router().ServeHTTP(rr, authedRequest(t, "DELETE", "/api/apps/demo", nil, token))
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rr.Code, rr.Body.String())
	}

	app, err := store.GetAppBySlug("demo")
	if err != nil {
		t.Fatalf("row was removed despite failed cleanup (bytes orphaned): %v", err)
	}
	if app.Status != "deleting" {
		t.Errorf("status = %q, want deleting (tombstone retained for reconcile)", app.Status)
	}
}

// TestDeployToken_AppOwnershipAndAdminBypass verifies two properties of the
// deploy-token identity (__deploy__):
//
//  1. POST /api/apps authenticated via the deploy token stores the __deploy__
//     user's ID as the app's owner_id.
//  2. An admin user who does not own the app can still GET and PATCH it — the
//     admin-bypass in canManageApp (isPrivilegedAppOperator) grants access even
//     when owner_id != admin.ID.
func TestDeployToken_AppOwnershipAndAdminBypass(t *testing.T) {
	srv, store := newTestServer(t)

	// --- deploy-token identity ---
	deployUser, err := store.UpsertSystemUser(db.SystemUsernameDeploy, "developer")
	if err != nil {
		t.Fatalf("upsert system user: %v", err)
	}
	rawToken := "shk_" + strings.Repeat("d", 64)
	srv.SetDeployToken(auth.NewDeployToken(rawToken, &auth.ContextUser{
		ID:       deployUser.ID,
		Username: deployUser.Username,
		Role:     deployUser.Role,
	}))

	// --- create app via deploy token ---
	createBody, _ := json.Marshal(map[string]string{"slug": "deploy-owned", "name": "Deploy Owned"})
	createReq := httptest.NewRequest("POST", "/api/apps", bytes.NewReader(createBody))
	createReq.Header.Set("Content-Type", "application/json")
	createReq.Header.Set("Authorization", "Token "+rawToken)
	createRec := httptest.NewRecorder()
	srv.Router().ServeHTTP(createRec, createReq)
	if createRec.Code != http.StatusCreated {
		t.Fatalf("POST /api/apps = %d, want 201: %s", createRec.Code, createRec.Body.String())
	}

	// Verify the app is stored with owner_id == __deploy__ user's ID.
	app, err := store.GetAppBySlug("deploy-owned")
	if err != nil {
		t.Fatalf("GetAppBySlug: %v", err)
	}
	if app.OwnerID != deployUser.ID {
		t.Errorf("app.OwnerID = %d, want %d (__deploy__ ID)", app.OwnerID, deployUser.ID)
	}

	// --- admin user: does not own the app but must be able to manage it ---
	hash, _ := auth.HashPassword("adminpass")
	if err := store.CreateUser(db.CreateUserParams{Username: "sysadmin", PasswordHash: hash, Role: "admin"}); err != nil {
		t.Fatalf("create admin: %v", err)
	}
	adminUser, _ := store.GetUserByUsername("sysadmin")
	adminToken, err := auth.IssueJWT(adminUser.ID, adminUser.Username, adminUser.Role, "test-secret")
	if err != nil {
		t.Fatalf("issue admin JWT: %v", err)
	}

	// GET /api/apps/{slug} — admin must receive 200.
	getReq := authedRequest(t, "GET", "/api/apps/deploy-owned", nil, adminToken)
	getRec := httptest.NewRecorder()
	srv.Router().ServeHTTP(getRec, getReq)
	if getRec.Code != http.StatusOK {
		t.Errorf("admin GET /api/apps/deploy-owned = %d, want 200: %s", getRec.Code, getRec.Body.String())
	}

	// PATCH /api/apps/{slug} — admin must receive 2xx even though owner_id != admin.ID.
	patchBody, _ := json.Marshal(map[string]string{"name": "Deploy Owned (updated)"})
	patchReq := authedRequest(t, "PATCH", "/api/apps/deploy-owned", patchBody, adminToken)
	patchRec := httptest.NewRecorder()
	srv.Router().ServeHTTP(patchRec, patchReq)
	if patchRec.Code/100 != 2 {
		t.Errorf("admin PATCH /api/apps/deploy-owned = %d, want 2xx: %s", patchRec.Code, patchRec.Body.String())
	}
}

// seedAppWithPromotedDeploy creates an app and promotes a deployment with the
// given content digest, so GetAppBySlug returns a non-empty ContentDigest.
func seedAppWithPromotedDeploy(t *testing.T, store *db.Store, slug, digest string) (token string) {
	t.Helper()
	tok, userID := seedUserAndJWT(t, store, slug+"-owner", "admin")
	if err := store.CreateApp(db.CreateAppParams{Slug: slug, Name: slug, OwnerID: userID, Access: "private"}); err != nil {
		t.Fatalf("seedAppWithPromotedDeploy create app: %v", err)
	}
	app, err := store.GetAppBySlug(slug)
	if err != nil {
		t.Fatalf("seedAppWithPromotedDeploy get app: %v", err)
	}
	dep, err := store.BeginDeployment(app.ID, "v1", "/tmp/precond-bundle")
	if err != nil {
		t.Fatalf("seedAppWithPromotedDeploy begin deployment: %v", err)
	}
	if err := store.SetDeploymentDigest(dep.ID, digest); err != nil {
		t.Fatalf("seedAppWithPromotedDeploy set digest: %v", err)
	}
	if err := store.PromoteDeployment(dep.ID); err != nil {
		t.Fatalf("seedAppWithPromotedDeploy promote: %v", err)
	}
	return tok
}

func TestPatchAppManagedByConflictOn409Header(t *testing.T) {
	srv, store := newTestServer(t)
	token, userID := seedUserAndJWT(t, store, "precond-user", "admin")
	if err := store.CreateApp(db.CreateAppParams{Slug: "precond", Name: "Precond", OwnerID: userID, Access: "private"}); err != nil {
		t.Fatalf("create app: %v", err)
	}
	// current managed_by is NULL; precondition expects "fleet:other" -> 409
	req := authedRequest(t, "PATCH", "/api/apps/precond", []byte(`{"managed_by":"fleet:prod"}`), token)
	req.Header.Set("X-Shinyhub-If-Managed-By", "fleet:other")
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)
	if rec.Code != http.StatusConflict {
		t.Fatalf("want 409 on managed_by precondition mismatch, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestPatchAppManagedBySucceedsWhenPreconditionMatches(t *testing.T) {
	srv, store := newTestServer(t)
	token, userID := seedUserAndJWT(t, store, "precond2-user", "admin")
	if err := store.CreateApp(db.CreateAppParams{Slug: "precond2", Name: "Precond2", OwnerID: userID, Access: "private"}); err != nil {
		t.Fatalf("create app: %v", err)
	}
	// no precondition header -> unconditional
	req := authedRequest(t, "PATCH", "/api/apps/precond2", []byte(`{"managed_by":"fleet:prod"}`), token)
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", rec.Code, rec.Body.String())
	}
	a, err := store.GetAppBySlug("precond2")
	if err != nil {
		t.Fatalf("get app: %v", err)
	}
	if a.ManagedBy == nil || *a.ManagedBy != "fleet:prod" {
		t.Fatalf("managed_by not persisted: %v", a.ManagedBy)
	}
}

func TestDeleteAppPreconditionMismatch409(t *testing.T) {
	srv, store := newTestServer(t)
	token := seedAppWithPromotedDeploy(t, store, "delprecond", "sha256:live")
	req := authedRequest(t, "DELETE", "/api/apps/delprecond", nil, token)
	req.Header.Set("X-Shinyhub-If-Content-Digest", "sha256:stale")
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)
	if rec.Code != http.StatusConflict {
		t.Fatalf("want 409 on delete precondition mismatch, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestListAppsJSONHasFleetFields(t *testing.T) {
	srv, store := newTestServer(t)
	token, userID := seedUserAndJWT(t, store, "fleet-check", "admin")
	store.CreateApp(db.CreateAppParams{Slug: "fleet-app", Name: "Fleet App", OwnerID: userID, Access: "private"})
	app, err := store.GetAppBySlug("fleet-app")
	if err != nil {
		t.Fatalf("get app: %v", err)
	}
	dep, err := store.BeginDeployment(app.ID, "v1", "/tmp/fleet-bundle")
	if err != nil {
		t.Fatalf("begin deployment: %v", err)
	}
	if err := store.SetDeploymentDigest(dep.ID, "sha256:apicheck"); err != nil {
		t.Fatalf("set deployment digest: %v", err)
	}
	if err := store.PromoteDeployment(dep.ID); err != nil {
		t.Fatalf("promote deployment: %v", err)
	}

	req := authedRequest(t, "GET", "/api/apps", nil, token)
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/apps = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.Bytes()
	if !bytes.Contains(body, []byte(`"managed_by"`)) {
		t.Fatal(`GET /api/apps must expose "managed_by"`)
	}
	if !bytes.Contains(body, []byte(`"content_digest"`)) {
		t.Fatal(`GET /api/apps must expose "content_digest"`)
	}
	if !bytes.Contains(body, []byte(`"sha256:apicheck"`)) {
		t.Fatalf(`GET /api/apps must include the promoted digest value, body: %s`, body)
	}
}

func TestSetAppAccessPreconditionMismatch409(t *testing.T) {
	srv, store := newTestServer(t)
	token := seedAppWithPromotedDeploy(t, store, "accprecond", "sha256:live")

	body, _ := json.Marshal(map[string]string{"access": "public"})
	req := authedRequest(t, "PATCH", "/api/apps/accprecond/access", body, token)
	req.Header.Set("X-Shinyhub-If-Content-Digest", "sha256:stale")
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)
	if rec.Code != http.StatusConflict {
		t.Fatalf("want 409 on set-access precondition mismatch, got %d: %s", rec.Code, rec.Body.String())
	}
	// Access must not have changed.
	app, err := store.GetAppBySlug("accprecond")
	if err != nil {
		t.Fatalf("get app: %v", err)
	}
	if app.Access != "private" {
		t.Fatalf("access must remain private after 409, got %q", app.Access)
	}
}

func TestPatchAppIfManagedByEmptyAssertsUnmanaged409(t *testing.T) {
	srv, store := newTestServer(t)
	token, userID := seedUserAndJWT(t, store, "managed-user", "admin")
	if err := store.CreateApp(db.CreateAppParams{Slug: "managed-app", Name: "Managed App", OwnerID: userID, Access: "private"}); err != nil {
		t.Fatalf("create app: %v", err)
	}
	// Mark the app as managed so the precondition (empty = unmanaged) will fail.
	val := "fleet:x"
	if err := store.SetAppManagedBy("managed-app", &val); err != nil {
		t.Fatalf("set managed_by: %v", err)
	}
	// Empty header value asserts "currently unmanaged"; the app IS managed -> 409.
	req := authedRequest(t, "PATCH", "/api/apps/managed-app", []byte(`{"name":"updated"}`), token)
	req.Header["X-Shinyhub-If-Managed-By"] = []string{""}
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)
	if rec.Code != http.StatusConflict {
		t.Fatalf("want 409 when empty If-Managed-By sent to a managed app, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestPatchAppIfContentDigestMismatch409(t *testing.T) {
	srv, store := newTestServer(t)
	token := seedAppWithPromotedDeploy(t, store, "digestprecond", "sha256:current")

	req := authedRequest(t, "PATCH", "/api/apps/digestprecond", []byte(`{"name":"new-name"}`), token)
	req.Header.Set("X-Shinyhub-If-Content-Digest", "sha256:wrong")
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)
	if rec.Code != http.StatusConflict {
		t.Fatalf("want 409 on content-digest mismatch, got %d: %s", rec.Code, rec.Body.String())
	}
	// Name must not have changed.
	app, err := store.GetAppBySlug("digestprecond")
	if err != nil {
		t.Fatalf("get app: %v", err)
	}
	if app.Name != "digestprecond" {
		t.Fatalf("app name must remain unchanged after 409, got %q", app.Name)
	}
}

func TestHandleGetApp_IncludesRejectsByReason(t *testing.T) {
	// Build a server that shares one proxy handle so the test can drive a reject
	// through the proxy and read it back via the app-detail GET. (newTestServer
	// passes a nil proxy and returns no handle, so we wire it inline here,
	// mirroring newTestServerWithTrustedProxies.)
	store, err := db.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Migrate(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	cfg := &config.Config{
		Auth:    config.AuthConfig{Secret: "test-secret"},
		Storage: config.StorageConfig{AppsDir: t.TempDir(), AppDataDir: t.TempDir()},
	}
	prx := proxy.New()
	srv := api.New(cfg, store, nil, prx)

	hash, _ := auth.HashPassword("pass")
	store.CreateUser(db.CreateUserParams{Username: "owner", PasswordHash: hash, Role: "developer"})
	owner, _ := store.GetUserByUsername("owner")
	if err := store.CreateApp(db.CreateAppParams{Slug: "demo", Name: "Demo", OwnerID: owner.ID}); err != nil {
		t.Fatal(err)
	}

	// Register a pool for "demo" but never complete a WS handshake. A readiness
	// poll then records exactly one app-not-ready reject under slug "demo"
	// (registered == true), synchronously and with no goroutines.
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer backend.Close()
	if err := prx.Register("demo", backend.URL); err != nil {
		t.Fatal(err)
	}
	prx.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/app/demo/.shinyhub/ready", nil))

	token, _ := auth.IssueJWT(owner.ID, "owner", "developer", "test-secret")
	req := authedRequest(t, "GET", "/api/apps/demo", nil, token)
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}

	var body map[string]any
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	block, ok := body["rejects_by_reason"].(map[string]any)
	if !ok {
		t.Fatalf("rejects_by_reason missing or wrong type: %v", body["rejects_by_reason"])
	}
	if block["window_seconds"].(float64) != 600 {
		t.Errorf("window_seconds = %v, want 600", block["window_seconds"])
	}
	counts := block["counts"].(map[string]any)
	if counts["app-not-ready"].(float64) != 1 {
		t.Errorf("counts[app-not-ready] = %v, want 1", counts["app-not-ready"])
	}
}

// newInlineServerWithProxy builds an isolated test server that shares a real
// proxy handle, mirroring the wiring in TestHandleGetApp_IncludesRejectsByReason.
// It returns the server, store, and proxy so tests can drive rejects through
// prx.ServeHTTP and read them back via the API.
func newInlineServerWithProxy(t *testing.T) (*api.Server, *db.Store, *proxy.Proxy) {
	t.Helper()
	store, err := db.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Migrate(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	cfg := &config.Config{
		Auth:    config.AuthConfig{Secret: "test-secret"},
		Storage: config.StorageConfig{AppsDir: t.TempDir(), AppDataDir: t.TempDir()},
	}
	prx := proxy.New()
	srv := api.New(cfg, store, nil, prx)
	return srv, store, prx
}

func TestDeleteApp_ForgetsRejects(t *testing.T) {
	srv, store, prx := newInlineServerWithProxy(t)

	hash, _ := auth.HashPassword("pass")
	store.CreateUser(db.CreateUserParams{Username: "owner", PasswordHash: hash, Role: "developer"})
	owner, _ := store.GetUserByUsername("owner")
	if err := store.CreateApp(db.CreateAppParams{Slug: "demo", Name: "Demo", OwnerID: owner.ID}); err != nil {
		t.Fatal(err)
	}

	// Register a pool and drive one readiness-probe reject to populate reject
	// history for the slug.
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer backend.Close()
	if err := prx.Register("demo", backend.URL); err != nil {
		t.Fatal(err)
	}
	prx.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/app/demo/.shinyhub/ready", nil))

	// Precondition: reject history is non-nil.
	if got := prx.RejectsByReason("demo", 10*time.Minute); got == nil {
		t.Fatal("precondition: RejectsByReason should be non-nil before delete")
	}

	token, _ := auth.IssueJWT(owner.ID, "owner", "developer", "test-secret")
	req := authedRequest(t, "DELETE", "/api/apps/demo", nil, token)
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("DELETE: expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	// Reject history must be cleared after the app is deleted.
	if got := prx.RejectsByReason("demo", 10*time.Minute); got != nil {
		t.Errorf("RejectsByReason after delete = %v, want nil", got)
	}
}

func TestHandleGetApp_RejectsByReason_MultipleReasons(t *testing.T) {
	// Verify that the rejects_by_reason envelope includes all distinct reasons
	// recorded for the same slug, each with the correct count.
	srv, store, prx := newInlineServerWithProxy(t)

	hash, _ := auth.HashPassword("pass")
	store.CreateUser(db.CreateUserParams{Username: "owner", PasswordHash: hash, Role: "developer"})
	owner, _ := store.GetUserByUsername("owner")
	if err := store.CreateApp(db.CreateAppParams{Slug: "demo", Name: "Demo", OwnerID: owner.ID}); err != nil {
		t.Fatal(err)
	}

	// To produce pool-saturated: hold one connection open via a blocking backend
	// (per-replica cap = 1, pool size = 1) so the next new-session request is shed.
	// inFlight is closed by the backend handler the moment it starts blocking,
	// giving the test a synchronisation point before it fires the saturated probe.
	release := make(chan struct{})
	inFlight := make(chan struct{})
	var holdOnce sync.Once
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		held := false
		holdOnce.Do(func() { held = true })
		if held {
			close(inFlight) // signal: held connection is now in-flight
			<-release       // block until cleanup releases us
		}
		w.WriteHeader(http.StatusOK)
	}))
	prx.SetPoolSize("demo", 1)
	prx.SetPoolCap("demo", 1)
	if err := prx.RegisterReplica("demo", 0, backend.URL, nil); err != nil {
		t.Fatal(err)
	}

	// Drive the held request in a goroutine; wait for inFlight before probing.
	// All three teardown steps run in one cleanup to guarantee ordering:
	// close(release) unblocks the held request, wg.Wait() drains the goroutine,
	// then backend.Close() tears down the server.
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		prx.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/app/demo/", nil))
	}()
	t.Cleanup(func() {
		close(release)
		wg.Wait()
		backend.Close()
	})

	// Wait until the held connection is actually in the backend handler before
	// probing saturation. This is deterministic: no sleep, no polling.
	select {
	case <-inFlight:
	case <-time.After(2 * time.Second):
		t.Fatal("held request did not reach backend within 2s")
	}

	// This new-session request lands on a saturated pool -> pool-saturated reject.
	prx.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/app/demo/", nil))

	// Drive one app-not-ready reject via the readiness probe.
	prx.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/app/demo/.shinyhub/ready", nil))

	token, _ := auth.IssueJWT(owner.ID, "owner", "developer", "test-secret")
	req := authedRequest(t, "GET", "/api/apps/demo", nil, token)
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}

	var body map[string]any
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	block, ok := body["rejects_by_reason"].(map[string]any)
	if !ok {
		t.Fatalf("rejects_by_reason missing or wrong type: %v", body["rejects_by_reason"])
	}
	if block["window_seconds"].(float64) != 600 {
		t.Errorf("window_seconds = %v, want 600", block["window_seconds"])
	}
	counts := block["counts"].(map[string]any)
	if counts["app-not-ready"].(float64) != 1 {
		t.Errorf("counts[app-not-ready] = %v, want 1", counts["app-not-ready"])
	}
	if counts["pool-saturated"].(float64) != 1 {
		t.Errorf("counts[pool-saturated] = %v, want 1", counts["pool-saturated"])
	}
}
