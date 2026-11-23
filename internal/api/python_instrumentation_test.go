package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/rvben/shinyhub/internal/auth"
	"github.com/rvben/shinyhub/internal/config"
	"github.com/rvben/shinyhub/internal/db"
	"github.com/rvben/shinyhub/internal/dbtest"
	"github.com/rvben/shinyhub/internal/deploy"
	"github.com/rvben/shinyhub/internal/process"
)

func TestPythonInstrumentationReconcileAndRetryFencePublication(t *testing.T) {
	store := dbtest.New(t)
	appsDir := t.TempDir()
	cfg := &config.Config{
		Auth:    config.AuthConfig{Secret: "test-secret"},
		Storage: config.StorageConfig{AppsDir: appsDir, AppDataDir: t.TempDir()},
		Tracing: config.TracingConfig{Enabled: true},
	}
	mgr := process.NewManager(appsDir, process.NewNativeRuntime())
	mgr.SetAutoInstrumentAppsDefault(true)
	srv := New(cfg, store, mgr, nil)
	if err := store.CreateUser(db.CreateUserParams{Username: "owner", Role: "developer"}); err != nil {
		t.Fatal(err)
	}
	user, err := store.GetUserByUsername("owner")
	if err != nil {
		t.Fatal(err)
	}
	token, err := auth.IssueJWT(user.ID, user.Username, user.Role, cfg.Auth.Secret)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateApp(db.CreateAppParams{Slug: "demo", Name: "Demo", OwnerID: user.ID}); err != nil {
		t.Fatal(err)
	}
	serving, err := store.GetAppBySlug("demo")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateApp(db.CreateAppParams{Slug: "empty", Name: "Empty", OwnerID: user.ID}); err != nil {
		t.Fatal(err)
	}
	activate := func(version string) *db.Deployment {
		t.Helper()
		bundle := filepath.Join(appsDir, "demo", version)
		if err := os.MkdirAll(filepath.Join(bundle, ".venv", "bin"), 0o755); err != nil {
			t.Fatal(err)
		}
		for name, mode := range map[string]os.FileMode{"pyproject.toml": 0o644, "uv.lock": 0o644, ".venv/bin/python": 0o755} {
			if err := os.WriteFile(filepath.Join(bundle, name), nil, mode); err != nil {
				t.Fatal(err)
			}
		}
		dep, err := store.CreateDeployment(db.CreateDeploymentParams{AppID: serving.ID, Version: version, BundleDir: bundle})
		if err != nil {
			t.Fatal(err)
		}
		return dep
	}
	first := activate("v1")
	started := make(chan deploy.Params, 2)
	continueBuild := make(chan struct{})
	buildDone := make(chan struct{})
	published := make(chan string, 2)
	srv.preparePythonOverlay = func(ctx context.Context, p deploy.Params, publish func(func() error) error) error {
		started <- p
		if p.BundleDir == first.BundleDir {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-continueBuild:
			}
		}
		return publish(func() error { published <- p.BundleDir; return nil })
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	go func() { defer close(buildDone); srv.ReconcilePythonInstrumentation(ctx) }()
	select {
	case p := <-started:
		if p.Slug != "demo" || p.BundleDir != first.BundleDir {
			t.Fatalf("reconcile selected the wrong deployment: %s %s", p.Slug, p.BundleDir)
		}
	case <-ctx.Done():
		t.Fatal("reconcile did not start preparation")
	}
	retry := func() *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/api/apps/demo/traces/instrumentation", nil)
		req.Header.Set("Authorization", "Bearer "+token)
		rec := httptest.NewRecorder()
		srv.Router().ServeHTTP(rec, req)
		return rec
	}
	if rec := retry(); rec.Code != http.StatusConflict {
		t.Fatalf("concurrent retry: got %d, want 409: %s", rec.Code, rec.Body.String())
	}
	release, ok := srv.TryAcquireAppOperation("demo")
	if !ok {
		t.Fatal("preparation held the app lifecycle lock during the build")
	}
	second := activate("v2")
	release()
	close(continueBuild)
	select {
	case <-buildDone:
	case <-ctx.Done():
		t.Fatal("reconcile did not finish")
	}
	select {
	case stale := <-published:
		t.Fatalf("reconcile published a stale build: %s", stale)
	default:
	}
	if rec := retry(); rec.Code != http.StatusNoContent {
		t.Fatalf("retry of the current deployment: got %d, want 204: %s", rec.Code, rec.Body.String())
	}
	select {
	case bundle := <-published:
		if bundle != second.BundleDir {
			t.Fatalf("retry published %s, want %s", bundle, second.BundleDir)
		}
	default:
		t.Fatal("retry never published the current deployment")
	}
}
