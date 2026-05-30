package api

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/rvben/shinyhub/internal/bundletoken"
	"github.com/rvben/shinyhub/internal/db"
)

// makeBundleTestDB creates a minimal DB with one app and one deployment, and
// writes a fake bundle zip at the expected path under appsDir.
func makeBundleTestDB(t *testing.T, appsDir string) (*db.Store, string) {
	t.Helper()
	store := newTestStore(t)
	// Create an owner user (required by CreateApp).
	if err := store.CreateUser(db.CreateUserParams{
		Username:     "bundleowner",
		PasswordHash: "h",
		Role:         "developer",
	}); err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	owner, err := store.GetUserByUsername("bundleowner")
	if err != nil {
		t.Fatalf("GetUserByUsername: %v", err)
	}
	// Create an app.
	if err := store.CreateApp(db.CreateAppParams{
		Slug:    "myapp",
		Name:    "My App",
		OwnerID: owner.ID,
		Access:  "private",
	}); err != nil {
		t.Fatalf("CreateApp: %v", err)
	}
	app, err := store.GetAppBySlug("myapp")
	if err != nil {
		t.Fatalf("GetAppBySlug: %v", err)
	}
	bundleDir := filepath.Join(appsDir, "myapp", "versions", "v1")
	dep, err := store.BeginDeployment(app.ID, "v1", bundleDir)
	if err != nil {
		t.Fatalf("BeginDeployment: %v", err)
	}
	const digest = "sha256:deadbeef"
	if err := store.SetDeploymentDigest(dep.ID, digest); err != nil {
		t.Fatalf("SetDeploymentDigest: %v", err)
	}
	if err := store.PromoteDeployment(dep.ID); err != nil {
		t.Fatalf("PromoteDeployment: %v", err)
	}
	// Write a fake bundle zip at <appsDir>/<slug>/bundles/<version>.zip
	zipDir := filepath.Join(appsDir, "myapp", "bundles")
	if err := os.MkdirAll(zipDir, 0755); err != nil {
		t.Fatal(err)
	}
	zipPath := filepath.Join(zipDir, "v1.zip")
	if err := os.WriteFile(zipPath, []byte("fakebundledata"), 0644); err != nil {
		t.Fatal(err)
	}
	return store, digest
}

func TestFargateBundleHandler_MissingBearer(t *testing.T) {
	secret := []byte("aaaabbbbccccddddeeeeffffgggghhhh")
	appsDir := t.TempDir()
	store, _ := makeBundleTestDB(t, appsDir)
	h := NewFargateBundleHandler(store, appsDir, secret)
	r := chi.NewRouter()
	r.Get("/internal/fargate-bundle/{digest}", h.Handle)

	req := httptest.NewRequest("GET", "/internal/fargate-bundle/sha256:deadbeef", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("want 401, got %d", rec.Code)
	}
}

func TestFargateBundleHandler_BadBearer(t *testing.T) {
	secret := []byte("aaaabbbbccccddddeeeeffffgggghhhh")
	appsDir := t.TempDir()
	store, _ := makeBundleTestDB(t, appsDir)
	h := NewFargateBundleHandler(store, appsDir, secret)
	r := chi.NewRouter()
	r.Get("/internal/fargate-bundle/{digest}", h.Handle)

	req := httptest.NewRequest("GET", "/internal/fargate-bundle/sha256:deadbeef", nil)
	req.Header.Set("Authorization", "Bearer notavalidtoken")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("want 401, got %d", rec.Code)
	}
}

func TestFargateBundleHandler_ExpiredBearer(t *testing.T) {
	secret := []byte("aaaabbbbccccddddeeeeffffgggghhhh")
	appsDir := t.TempDir()
	store, digest := makeBundleTestDB(t, appsDir)
	h := NewFargateBundleHandler(store, appsDir, secret)
	r := chi.NewRouter()
	r.Get("/internal/fargate-bundle/{digest}", h.Handle)

	// Mint a token that expired well in the past.
	pastNow := time.Now().Unix() - 700 // well past a 10-minute TTL
	tok := bundletoken.Mint(secret, digest, 10*time.Minute, pastNow)
	req := httptest.NewRequest("GET", "/internal/fargate-bundle/"+digest, nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("want 401 for expired token, got %d", rec.Code)
	}
}

func TestFargateBundleHandler_ValidToken(t *testing.T) {
	secret := []byte("aaaabbbbccccddddeeeeffffgggghhhh")
	appsDir := t.TempDir()
	store, digest := makeBundleTestDB(t, appsDir)
	h := NewFargateBundleHandler(store, appsDir, secret)
	r := chi.NewRouter()
	r.Get("/internal/fargate-bundle/{digest}", h.Handle)

	tok := bundletoken.Mint(secret, digest, 10*time.Minute, time.Now().Unix())
	req := httptest.NewRequest("GET", "/internal/fargate-bundle/"+digest, nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/zip" {
		t.Fatalf("want Content-Type application/zip, got %q", ct)
	}
	body, _ := io.ReadAll(rec.Body)
	if string(body) != "fakebundledata" {
		t.Fatalf("unexpected body %q", body)
	}
}

func TestFargateBundleHandler_RateLimit(t *testing.T) {
	secret := []byte("aaaabbbbccccddddeeeeffffgggghhhh")
	appsDir := t.TempDir()
	store, digest := makeBundleTestDB(t, appsDir)
	// Use a handler with a very tight rate limit for the test: 1 req/window.
	h := newFargateBundleHandlerWithRL(store, appsDir, secret, 1, time.Minute)
	r := chi.NewRouter()
	r.Get("/internal/fargate-bundle/{digest}", h.Handle)

	// Two requests from the same IP with a bad token: first gets 401, second gets 429.
	for i := 0; i < 2; i++ {
		req := httptest.NewRequest("GET", "/internal/fargate-bundle/"+digest, nil)
		req.Header.Set("Authorization", "Bearer badtoken")
		req.RemoteAddr = "10.0.0.1:9999"
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, req)
		if i == 0 && rec.Code != http.StatusUnauthorized {
			t.Fatalf("first: want 401, got %d", rec.Code)
		}
		if i == 1 && rec.Code != http.StatusTooManyRequests {
			t.Fatalf("second: want 429 after rate limit, got %d", rec.Code)
		}
	}
}
