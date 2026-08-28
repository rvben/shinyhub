package main

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rvben/shinyhub/internal/api"
	"github.com/rvben/shinyhub/internal/auth"
	"github.com/rvben/shinyhub/internal/config"
	"github.com/rvben/shinyhub/internal/db"
	"github.com/rvben/shinyhub/internal/dbtest"
	"github.com/rvben/shinyhub/internal/deploy"
	"github.com/rvben/shinyhub/internal/process"
	"github.com/rvben/shinyhub/internal/proxy"
)

func TestEligibleForFirstRunExampleIsDeliberatelyNarrow(t *testing.T) {
	local := &setupResult{
		CreatedConfig: true,
		CreatedAdmin:  true,
		Username:      "admin",
		DatabaseDSN:   "data/shinyhub.db",
	}
	for _, tc := range []struct {
		name   string
		result *setupResult
		host   string
		want   bool
	}{
		{name: "fresh loopback", result: local, host: "127.0.0.1", want: true},
		{name: "IPv6 loopback", result: local, host: "::1", want: true},
		{name: "existing install", result: nil, host: "127.0.0.1"},
		{name: "config already existed", result: &setupResult{CreatedAdmin: true, Username: "admin", DatabaseDSN: "data/shinyhub.db"}, host: "127.0.0.1"},
		{name: "admin already existed", result: &setupResult{CreatedConfig: true, Username: "admin", DatabaseDSN: "data/shinyhub.db"}, host: "127.0.0.1"},
		{name: "network reachable", result: local, host: "0.0.0.0"},
		{name: "Postgres", result: &setupResult{CreatedConfig: true, CreatedAdmin: true, Username: "admin", DatabaseDSN: "postgres://db/shinyhub"}, host: "127.0.0.1"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := eligibleForFirstRunExample(tc.result, &config.Config{Server: config.ServerConfig{Host: tc.host}})
			if got != tc.want {
				t.Fatalf("eligibleForFirstRunExample() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestFirstRunExampleBundleIsARealPythonShinyApp(t *testing.T) {
	bundle, err := buildFirstRunExampleBundle()
	if err != nil {
		t.Fatalf("buildFirstRunExampleBundle: %v", err)
	}
	zr, err := zip.NewReader(bytes.NewReader(bundle), int64(len(bundle)))
	if err != nil {
		t.Fatalf("open bundle: %v", err)
	}
	gotFiles := make(map[string]bool)
	for _, file := range zr.File {
		gotFiles[file.Name] = true
	}
	for _, want := range []string{"app.py", "pyproject.toml", "shinyhub.toml"} {
		if !gotFiles[want] {
			t.Errorf("example bundle is missing %s", want)
		}
	}

	zipPath := filepath.Join(t.TempDir(), "example.zip")
	if err := os.WriteFile(zipPath, bundle, 0o600); err != nil {
		t.Fatal(err)
	}
	bundleDir := filepath.Join(t.TempDir(), "bundle")
	if err := deploy.ExtractBundle(zipPath, bundleDir); err != nil {
		t.Fatalf("extract example bundle: %v", err)
	}
	if got := deploy.DetectAppType(bundleDir); got != "python" {
		t.Fatalf("DetectAppType = %q, want python", got)
	}
	manifest, err := deploy.LoadManifest(bundleDir)
	if err != nil {
		t.Fatalf("load example manifest: %v", err)
	}
	if manifest == nil || manifest.App.Name == nil || *manifest.App.Name != firstRunExampleName {
		t.Fatalf("example manifest name = %+v, want %q", manifest, firstRunExampleName)
	}
	if manifest.App.Description == nil || !strings.Contains(*manifest.App.Description, "real build pipeline") {
		t.Fatalf("example manifest description = %+v", manifest.App.Description)
	}
}

func TestInstallFirstRunExampleUsesCreateAndDeployRoutes(t *testing.T) {
	store := newFirstRunTestStore(t)
	if err := store.CreateUser(db.CreateUserParams{Username: "admin", PasswordHash: "hash", Role: "admin"}); err != nil {
		t.Fatal(err)
	}
	admin, err := store.GetUserByUsername("admin")
	if err != nil {
		t.Fatal(err)
	}

	var routes []string
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		routes = append(routes, r.Method+" "+r.URL.Path)
		user := auth.UserFromContext(r.Context())
		if user == nil || user.Username != "admin" {
			t.Errorf("request user = %+v, want admin", user)
		}
		if r.Header.Get("Authorization") == "" {
			t.Error("first-run API request is missing the CSRF-exempt Authorization header")
		}
		switch r.URL.Path {
		case "/api/apps":
			var body struct {
				Slug   string `json:"slug"`
				Name   string `json:"name"`
				Access string `json:"access"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Errorf("decode create body: %v", err)
			}
			if body.Slug != firstRunExampleSlug || body.Name != firstRunExampleName || body.Access != "private" {
				t.Errorf("unexpected create body: %+v", body)
			}
			if _, err := store.CreateApp(db.CreateAppParams{Slug: body.Slug, Name: body.Name, OwnerID: admin.ID, Access: body.Access}); err != nil {
				t.Errorf("create app: %v", err)
			}
			w.WriteHeader(http.StatusCreated)
		case "/api/apps/shinyhub-tour/deploy":
			file, _, err := r.FormFile("bundle")
			if err != nil {
				t.Errorf("read bundle upload: %v", err)
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			defer file.Close()
			content, err := io.ReadAll(file)
			if err != nil || len(content) == 0 {
				t.Errorf("uploaded bundle is empty: %v", err)
			}
			w.WriteHeader(http.StatusOK)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	})

	installed, err := installFirstRunExample(context.Background(), handler, store, &setupResult{Username: "admin"})
	if err != nil {
		t.Fatalf("installFirstRunExample: %v", err)
	}
	if !installed {
		t.Fatal("installFirstRunExample reported skipped")
	}
	if got, want := strings.Join(routes, ", "), "POST /api/apps, POST /api/apps/shinyhub-tour/deploy"; got != want {
		t.Fatalf("routes = %q, want %q", got, want)
	}
	app, err := store.GetAppBySlug(firstRunExampleSlug)
	if err != nil || app.Access != "private" {
		t.Fatalf("installed app = %+v, err = %v", app, err)
	}
}

func TestInstallFirstRunExampleTraversesProductionAPI(t *testing.T) {
	store := newFirstRunTestStore(t)
	if err := store.CreateUser(db.CreateUserParams{Username: "admin", PasswordHash: "hash", Role: "admin"}); err != nil {
		t.Fatal(err)
	}
	appsDir := t.TempDir()
	cfg := &config.Config{
		Auth:    config.AuthConfig{Secret: "test-secret"},
		Storage: config.StorageConfig{AppsDir: appsDir, AppDataDir: t.TempDir()},
	}
	mgr := process.NewManager(appsDir, process.NewNativeRuntime())
	srv := api.New(cfg, store, mgr, proxy.New())
	srv.SetOwnership(func() bool { return true })
	srv.SetDeployRunForTest(func(params deploy.Params) (*deploy.PoolResult, error) {
		if params.Slug != firstRunExampleSlug {
			t.Errorf("deploy slug = %q, want %q", params.Slug, firstRunExampleSlug)
		}
		if got := deploy.DetectAppType(params.BundleDir); got != "python" {
			t.Errorf("deployed app type = %q, want python", got)
		}
		return &deploy.PoolResult{}, nil
	})

	installed, err := installFirstRunExample(context.Background(), srv.Router(), store, &setupResult{Username: "admin"})
	if err != nil {
		t.Fatalf("installFirstRunExample through production API: %v", err)
	}
	if !installed {
		t.Fatal("installFirstRunExample reported skipped")
	}
	app, err := store.GetAppBySlug(firstRunExampleSlug)
	if err != nil {
		t.Fatal(err)
	}
	if app.Status != "running" || app.DeployCount != 1 || app.IconEmoji != "⚡" || !strings.Contains(app.Description, "real build pipeline") {
		t.Fatalf("production API did not apply the example deployment and manifest: %+v", app)
	}
}

func TestInstallFirstRunExampleNeverAddsContentToNonEmptyServer(t *testing.T) {
	store := newFirstRunTestStore(t)
	if err := store.CreateUser(db.CreateUserParams{Username: "owner", PasswordHash: "hash", Role: "developer"}); err != nil {
		t.Fatal(err)
	}
	owner, err := store.GetUserByUsername("owner")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateApp(db.CreateAppParams{Slug: "existing", Name: "Existing", OwnerID: owner.ID, Access: "private"}); err != nil {
		t.Fatal(err)
	}
	called := false
	handler := http.HandlerFunc(func(http.ResponseWriter, *http.Request) { called = true })
	installed, err := installFirstRunExample(context.Background(), handler, store, &setupResult{Username: "admin"})
	if err != nil {
		t.Fatalf("installFirstRunExample: %v", err)
	}
	if installed || called {
		t.Fatalf("non-empty server: installed=%v handlerCalled=%v", installed, called)
	}
}

func newFirstRunTestStore(t *testing.T) *db.Store {
	t.Helper()
	return dbtest.New(t)
}
