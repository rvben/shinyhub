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
