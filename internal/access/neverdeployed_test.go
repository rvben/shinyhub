package access_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/rvben/shinyhub/internal/access"
	"github.com/rvben/shinyhub/internal/auth"
	"github.com/rvben/shinyhub/internal/db"
)

func TestNeverDeployed_PassThroughWhenDeployed(t *testing.T) {
	store := makeStore(t)
	store.CreateUser(db.CreateUserParams{Username: "owner", PasswordHash: "h", Role: "admin"})
	owner, _ := store.GetUserByUsername("owner")
	store.CreateApp(db.CreateAppParams{Slug: "app", Name: "App", OwnerID: owner.ID})
	app, _ := store.GetAppBySlug("app")
	if _, err := store.CreateDeployment(db.CreateDeploymentParams{
		AppID: app.ID, Version: "v1", BundleDir: "/tmp/v1", Status: "succeeded",
	}); err != nil {
		t.Fatal(err)
	}

	called := false
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	})
	handler := access.NeverDeployedMiddleware(store, "test-secret", nil, nil)(next)

	req := httptest.NewRequest("GET", "/app/app/", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if !called {
		t.Fatal("expected request to pass through to next handler when a deployment row exists")
	}
}

// Regression: when the deploy succeeded but the post-deploy
// IncrementDeployCount write transiently failed, deploy_count stays 0 but
// the deployments row exists and the pool is live. The gate must consult
// the durable deployments row, not the counter, or the user is locked
// out of their own app behind the never-deployed empty-state page.
func TestNeverDeployed_PassThroughWhenCounterLagsDeploymentRow(t *testing.T) {
	store := makeStore(t)
	store.CreateUser(db.CreateUserParams{Username: "owner", PasswordHash: "h", Role: "admin"})
	owner, _ := store.GetUserByUsername("owner")
	store.CreateApp(db.CreateAppParams{Slug: "app", Name: "App", OwnerID: owner.ID})
	app, _ := store.GetAppBySlug("app")
	// Insert the deployment row WITHOUT calling IncrementDeployCount.
	if _, err := store.CreateDeployment(db.CreateDeploymentParams{
		AppID: app.ID, Version: "v1", BundleDir: "/tmp/v1", Status: "succeeded",
	}); err != nil {
		t.Fatal(err)
	}

	called := false
	handler := access.NeverDeployedMiddleware(store, "test-secret", nil, nil)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest("GET", "/app/app/", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if !called {
		t.Fatal("expected pass-through when a deployment row exists, even though deploy_count is still 0")
	}
}

func TestNeverDeployed_ManagerSeesCLISnippet(t *testing.T) {
	store := makeStore(t)
	store.CreateUser(db.CreateUserParams{Username: "owner", PasswordHash: "h", Role: "developer"})
	owner, _ := store.GetUserByUsername("owner")
	store.CreateApp(db.CreateAppParams{Slug: "newapp", Name: "Fresh App", OwnerID: owner.ID})
	token, _ := auth.IssueJWT(owner.ID, "owner", "developer", "test-secret")

	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("next handler should not be invoked for never-deployed app")
	})
	handler := access.NeverDeployedMiddleware(store, "test-secret", nil, nil)(next)

	req := httptest.NewRequest("GET", "/app/newapp/", nil)
	req.Host = "shiny.example.com"
	req.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: token})
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "Fresh App") {
		t.Errorf("expected app name in body, got %q", body)
	}
	if !strings.Contains(body, "shinyhub login --host http://shiny.example.com --username owner") {
		t.Errorf("expected login snippet with real username, got %q", body)
	}
	if !strings.Contains(body, "shinyhub deploy --slug newapp") {
		t.Errorf("expected deploy snippet with slug, got %q", body)
	}
	if !strings.Contains(body, `href="http://shiny.example.com/#deploy=newapp"`) {
		t.Errorf("expected browser-deploy link, got %q", body)
	}
	if !strings.Contains(body, "Awaiting first deploy") {
		t.Errorf("expected awaiting-first-deploy eyebrow copy, got %q", body)
	}
	if !strings.Contains(body, `<link rel="stylesheet" href="/static/style.css">`) {
		t.Errorf("expected page to link shared stylesheet, got %q", body)
	}
	if strings.Contains(body, "<style>") {
		t.Errorf("expected no inline <style> block — page should use shared stylesheet")
	}
	if !strings.Contains(body, "What should my bundle contain?") {
		t.Errorf("expected scaffold help summary, got %q", body)
	}
}

func TestNeverDeployed_NonManagerSeesUnpublishedNotice(t *testing.T) {
	store := makeStore(t)
	store.CreateUser(db.CreateUserParams{Username: "owner", PasswordHash: "h", Role: "developer"})
	owner, _ := store.GetUserByUsername("owner")
	store.CreateUser(db.CreateUserParams{Username: "viewer", PasswordHash: "h", Role: "developer"})
	viewer, _ := store.GetUserByUsername("viewer")

	store.CreateApp(db.CreateAppParams{Slug: "shared", Name: "Shared App", OwnerID: owner.ID})
	store.SetAppAccess("shared", "shared")

	token, _ := auth.IssueJWT(viewer.ID, "viewer", "developer", "test-secret")

	handler := access.NeverDeployedMiddleware(store, "test-secret", nil, nil)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("next handler should not be invoked for never-deployed app")
	}))

	req := httptest.NewRequest("GET", "/app/shared/", nil)
	req.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: token})
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	body := rec.Body.String()
	if !strings.Contains(body, "being prepared by its owner") {
		t.Errorf("expected non-manager 'being prepared' notice, got %q", body)
	}
	if strings.Contains(body, "shinyhub login") {
		t.Errorf("non-manager should not see CLI snippet, got %q", body)
	}
	if strings.Contains(body, "#deploy=") {
		t.Errorf("non-manager should not see browser-deploy link, got %q", body)
	}
}

func TestNeverDeployed_NoSlugPassThrough(t *testing.T) {
	store := makeStore(t)
	called := false
	handler := access.NeverDeployedMiddleware(store, "test-secret", nil, nil)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
	}))

	req := httptest.NewRequest("GET", "/app/", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if !called {
		t.Fatal("expected pass-through when path has no slug")
	}
}

func TestNeverDeployed_UnknownAppPassThrough(t *testing.T) {
	store := makeStore(t)
	called := false
	handler := access.NeverDeployedMiddleware(store, "test-secret", nil, nil)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
	}))

	req := httptest.NewRequest("GET", "/app/ghost/", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if !called {
		t.Fatal("expected pass-through when app does not exist (proxy's loading page owns that case)")
	}
}
