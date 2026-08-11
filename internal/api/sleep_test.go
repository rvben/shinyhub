package api_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/rvben/shinyhub/internal/api"
	"github.com/rvben/shinyhub/internal/auth"
	"github.com/rvben/shinyhub/internal/db"
	"github.com/rvben/shinyhub/internal/lifecycle"
)

// seedSleepApp creates an owner plus one app and returns the owner's token.
func seedSleepApp(t *testing.T, store *db.Store, slug, status string) string {
	t.Helper()
	hash, _ := testHashPassword("pass")
	store.CreateUser(db.CreateUserParams{Username: "owner", PasswordHash: hash, Role: "developer"})
	u, _ := store.GetUserByUsername("owner")
	store.CreateApp(db.CreateAppParams{Slug: slug, Name: slug, OwnerID: u.ID})
	store.UpdateAppStatus(db.UpdateAppStatusParams{Slug: slug, Status: status})
	token, _ := auth.IssueJWT(u.ID, "owner", "developer", "test-secret")
	return token
}

func postSleep(t *testing.T, srv *api.Server, slug, token string) *httptest.ResponseRecorder {
	t.Helper()
	req := authedRequest(t, "POST", "/api/apps/"+slug+"/sleep", nil, token)
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)
	return rec
}

func TestSleepApp_RunningAppBecomesHibernated(t *testing.T) {
	srv, store := newTestServer(t)
	token := seedSleepApp(t, store, "sleepy", "running")

	var called []string
	srv.SetSleepOp(func(slug string) error {
		called = append(called, slug)
		return store.UpdateAppStatus(db.UpdateAppStatusParams{Slug: slug, Status: "hibernated"})
	})

	rec := postSleep(t, srv, "sleepy", token)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var resp map[string]any
	json.NewDecoder(rec.Body).Decode(&resp)
	if resp["status"] != "hibernated" {
		t.Errorf("expected status=hibernated, got %v", resp["status"])
	}
	if len(called) != 1 || called[0] != "sleepy" {
		t.Errorf("sleep op calls = %v, want [sleepy]", called)
	}
}

// The endpoint is the backstop for the hidden UI menu item; the UI must not be
// the only guard.
func TestSleepApp_ElasticAppIsRejected(t *testing.T) {
	srv, store := newTestServer(t)
	token := seedSleepApp(t, store, "elastic", "running")
	srv.SetSleepOp(func(string) error { return lifecycle.ErrElasticNotSleepable })

	rec := postSleep(t, srv, "elastic", token)
	if rec.Code != http.StatusConflict {
		t.Errorf("expected 409, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestSleepApp_NotRunningIsConflict(t *testing.T) {
	srv, store := newTestServer(t)
	token := seedSleepApp(t, store, "downed", "stopped")
	srv.SetSleepOp(func(string) error { return lifecycle.ErrAppNotRunning })

	rec := postSleep(t, srv, "downed", token)
	if rec.Code != http.StatusConflict {
		t.Errorf("expected 409, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestSleepApp_TeardownFailureIsConflict(t *testing.T) {
	srv, store := newTestServer(t)
	token := seedSleepApp(t, store, "stuck", "running")
	srv.SetSleepOp(func(string) error { return lifecycle.ErrSleepTeardownFailed })

	rec := postSleep(t, srv, "stuck", token)
	if rec.Code != http.StatusConflict {
		t.Errorf("expected 409, got %d: %s", rec.Code, rec.Body.String())
	}
}

// An unwired op must report unavailable rather than pretend the app slept.
func TestSleepApp_UnwiredOpIsUnavailable(t *testing.T) {
	srv, store := newTestServer(t)
	token := seedSleepApp(t, store, "nowire", "running")

	rec := postSleep(t, srv, "nowire", token)
	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("expected 503, got %d: %s", rec.Code, rec.Body.String())
	}
}

// A viewer must not be able to take an app down.
func TestSleepApp_ViewerIsForbidden(t *testing.T) {
	srv, store := newTestServer(t)
	hash, _ := testHashPassword("pass")
	store.CreateUser(db.CreateUserParams{Username: "owner", PasswordHash: hash, Role: "developer"})
	owner, _ := store.GetUserByUsername("owner")
	store.CreateApp(db.CreateAppParams{Slug: "guarded", Name: "Guarded", OwnerID: owner.ID})
	store.UpdateAppStatus(db.UpdateAppStatusParams{Slug: "guarded", Status: "running"})

	store.CreateUser(db.CreateUserParams{Username: "viewer", PasswordHash: hash, Role: "developer"})
	viewer, _ := store.GetUserByUsername("viewer")
	store.GrantAppAccess("guarded", viewer.ID)

	var called int
	srv.SetSleepOp(func(string) error { called++; return nil })
	token, _ := auth.IssueJWT(viewer.ID, "viewer", "developer", "test-secret")

	rec := postSleep(t, srv, "guarded", token)
	if rec.Code != http.StatusForbidden && rec.Code != http.StatusNotFound {
		t.Errorf("expected viewer to be denied, got %d: %s", rec.Code, rec.Body.String())
	}
	if called != 0 {
		t.Errorf("sleep op ran %d times for a viewer; authz must gate it", called)
	}
}

func TestSleepApp_UnauthenticatedIsRejected(t *testing.T) {
	srv, store := newTestServer(t)
	seedSleepApp(t, store, "guarded", "running")
	srv.SetSleepOp(func(string) error { return nil })

	req := httptest.NewRequest("POST", "/api/apps/guarded/sleep", nil)
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d", rec.Code)
	}
}
