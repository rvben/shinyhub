package api_test

import (
	"archive/zip"
	"bytes"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/rvben/shinyhub/internal/admission"
	"github.com/rvben/shinyhub/internal/api"
	"github.com/rvben/shinyhub/internal/auth"
	"github.com/rvben/shinyhub/internal/config"
	"github.com/rvben/shinyhub/internal/db"
	"github.com/rvben/shinyhub/internal/dbtest"
	"github.com/rvben/shinyhub/internal/deploy"
	"github.com/rvben/shinyhub/internal/process"
	"github.com/rvben/shinyhub/internal/proxy"
)

func resInt(v int) *int { return &v }

// newQuotaTestServerWithProxy mirrors newQuotaTestServer (deploy_quota_test.go)
// but also returns the wired *proxy.Proxy, so a test can install a render-
// limiter factory and observe which render_seconds values are applied live.
func newQuotaTestServerWithProxy(t *testing.T, appsDir string) (*api.Server, *db.Store, *proxy.Proxy) {
	t.Helper()
	store := dbtest.New(t)
	cfg := &config.Config{
		Auth: config.AuthConfig{Secret: "test-secret"},
		Storage: config.StorageConfig{
			AppsDir:          appsDir,
			VersionRetention: 5,
		},
	}
	mgr := process.NewManager(appsDir, process.NewNativeRuntime())
	prx := proxy.New()
	srv := api.New(cfg, store, mgr, prx)
	return srv, store, prx
}

// buildBundleUploadFiles zips multiple named files into a deploy upload so a
// test can ship a shinyhub.toml alongside the app source.
func buildBundleUploadFiles(t *testing.T, files map[string]string) (*bytes.Buffer, string) {
	t.Helper()
	var zipBuf bytes.Buffer
	zw := zip.NewWriter(&zipBuf)
	for name, content := range files {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write([]byte(content)); err != nil {
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
	return &body, mw.FormDataContentType()
}

// TestDeployApp_FailedDeployRevertsResourceLimits verifies that a deploy whose
// bundle [app] sets new memory/cpu limits, but which then fails to boot, leaves
// the app's resource columns at their PRE-manifest values so the restored old
// pool runs under the limits it was deployed with — not the failed bundle's.
func TestDeployApp_FailedDeployRevertsResourceLimits(t *testing.T) {
	appsDir := t.TempDir()
	srv, store := newQuotaTestServer(t, appsDir, 0) // quota disabled

	hash, _ := testHashPassword("pass")
	_ = store.CreateUser(db.CreateUserParams{Username: "admin", PasswordHash: hash, Role: "admin"})
	u, _ := store.GetUserByUsername("admin")
	_, _ = store.CreateApp(db.CreateAppParams{Slug: "demo", Name: "Demo", OwnerID: u.ID})
	app, _ := store.GetAppBySlug("demo")

	// Pre-manifest resource policy.
	if _, err := store.ApplyAppManifestSettings(db.ApplyAppManifestSettingsParams{
		AppID: app.ID, Slug: "demo",
		SetMemoryLimitMB: true, MemoryLimitMB: resInt(256),
		SetCPUQuotaPercent: true, CPUQuotaPercent: resInt(50),
	}); err != nil {
		t.Fatal(err)
	}

	// A previous good deployment that exists on disk for restore.
	v1Dir := filepath.Join(appsDir, "demo", "versions", "v1")
	if err := os.MkdirAll(v1Dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateDeployment(db.CreateDeploymentParams{
		AppID: app.ID, Version: "v1", BundleDir: v1Dir,
	}); err != nil {
		t.Fatal(err)
	}

	// The new deploy fails; restoring v1 (a different bundle dir) succeeds.
	srv.SetDeployRunForTest(func(p deploy.Params) (*deploy.PoolResult, error) {
		if p.BundleDir == v1Dir {
			return &deploy.PoolResult{Replicas: []deploy.Result{{Index: 0, PID: 1, Port: 1}}}, nil
		}
		return nil, deploy.ErrBundleRejected
	})

	body, ctype := buildBundleUploadFiles(t, map[string]string{
		"app.py":        "print('hi')\n",
		"shinyhub.toml": "[app]\nmemory_limit_mb = 2048\ncpu_quota_percent = 150\n",
	})
	token, _ := auth.IssueJWT(u.ID, u.Username, u.Role, "test-secret")
	req := httptest.NewRequest("POST", "/api/apps/demo/deploy", body)
	req.Header.Set("Content-Type", ctype)
	req.Header.Set("Authorization", "Bearer "+token)

	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500 on failed deploy, got %d: %s", rec.Code, rec.Body.String())
	}

	got, err := store.GetAppBySlug("demo")
	if err != nil {
		t.Fatal(err)
	}
	if got.MemoryLimitMB == nil || *got.MemoryLimitMB != 256 {
		t.Errorf("memory_limit_mb = %v after failed deploy, want 256 (pre-manifest)", got.MemoryLimitMB)
	}
	if got.CPUQuotaPercent == nil || *got.CPUQuotaPercent != 50 {
		t.Errorf("cpu_quota_percent = %v after failed deploy, want 50 (pre-manifest)", got.CPUQuotaPercent)
	}
}

// TestDeployApp_FailedDeployRevertsAutoscale verifies that a deploy whose bundle
// [app] declares a new autoscale policy, but which then fails to boot, leaves the
// app's autoscale_* columns at their PRE-manifest values — so the restored old
// pool keeps the policy it was deployed with, not the failed bundle's.
func TestDeployApp_FailedDeployRevertsAutoscale(t *testing.T) {
	appsDir := t.TempDir()
	srv, store := newQuotaTestServer(t, appsDir, 0) // quota disabled

	hash, _ := testHashPassword("pass")
	_ = store.CreateUser(db.CreateUserParams{Username: "admin", PasswordHash: hash, Role: "admin"})
	u, _ := store.GetUserByUsername("admin")
	_, _ = store.CreateApp(db.CreateAppParams{Slug: "demo", Name: "Demo", OwnerID: u.ID})
	app, _ := store.GetAppBySlug("demo")

	// Pre-manifest autoscale policy: enabled, bounds [1,2], target 0.5.
	if _, err := store.ApplyAppManifestSettings(db.ApplyAppManifestSettingsParams{
		AppID: app.ID, Slug: "demo",
		SetAutoscale: true, AutoscaleEnabled: true,
		AutoscaleMinReplicas: 1, AutoscaleMaxReplicas: 2, AutoscaleTarget: 0.5,
	}); err != nil {
		t.Fatal(err)
	}

	// A previous good deployment that exists on disk for restore.
	v1Dir := filepath.Join(appsDir, "demo", "versions", "v1")
	if err := os.MkdirAll(v1Dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateDeployment(db.CreateDeploymentParams{
		AppID: app.ID, Version: "v1", BundleDir: v1Dir,
	}); err != nil {
		t.Fatal(err)
	}

	// The new deploy fails; restoring v1 (a different bundle dir) succeeds.
	srv.SetDeployRunForTest(func(p deploy.Params) (*deploy.PoolResult, error) {
		if p.BundleDir == v1Dir {
			return &deploy.PoolResult{Replicas: []deploy.Result{{Index: 0, PID: 1, Port: 1}}}, nil
		}
		return nil, deploy.ErrBundleRejected
	})

	// The failing bundle declares a DIFFERENT autoscale policy (bounds [1,8],
	// target 0.9) that must NOT survive the failed deploy.
	body, ctype := buildBundleUploadFiles(t, map[string]string{
		"app.py":        "print('hi')\n",
		"shinyhub.toml": "[app]\nautoscale = { enabled = true, min_replicas = 1, max_replicas = 8, target = 0.9 }\n",
	})
	token, _ := auth.IssueJWT(u.ID, u.Username, u.Role, "test-secret")
	req := httptest.NewRequest("POST", "/api/apps/demo/deploy", body)
	req.Header.Set("Content-Type", ctype)
	req.Header.Set("Authorization", "Bearer "+token)

	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500 on failed deploy, got %d: %s", rec.Code, rec.Body.String())
	}

	got, err := store.GetAppBySlug("demo")
	if err != nil {
		t.Fatal(err)
	}
	if !got.AutoscaleEnabled {
		t.Errorf("autoscale disabled after failed deploy, want enabled (pre-manifest)")
	}
	if got.AutoscaleMaxReplicas != 2 {
		t.Errorf("autoscale max_replicas = %d after failed deploy, want 2 (pre-manifest, not the bundle's 8)", got.AutoscaleMaxReplicas)
	}
	if got.AutoscaleTarget != 0.5 {
		t.Errorf("autoscale target = %v after failed deploy, want 0.5 (pre-manifest)", got.AutoscaleTarget)
	}
}

// TestDeployApp_FailedDeployRevertsRenderSeconds verifies that a deploy whose
// bundle [app] sets a new render_seconds, but which then fails to boot, leaves
// the app's render_seconds column at its PRE-manifest value and re-applies that
// value live to the proxy - so the restored old pool is paced under the
// settings it was deployed with, not the failed bundle's.
func TestDeployApp_FailedDeployRevertsRenderSeconds(t *testing.T) {
	appsDir := t.TempDir()
	srv, store, prx := newQuotaTestServerWithProxy(t, appsDir)

	var applied []float64
	prx.SetRenderLimiterFactory(func(rs float64) *admission.AppLimiter {
		applied = append(applied, rs)
		return nil
	})

	hash, _ := testHashPassword("pass")
	_ = store.CreateUser(db.CreateUserParams{Username: "admin", PasswordHash: hash, Role: "admin"})
	u, _ := store.GetUserByUsername("admin")
	_, _ = store.CreateApp(db.CreateAppParams{Slug: "demo", Name: "Demo", OwnerID: u.ID})
	app, _ := store.GetAppBySlug("demo")

	// Pre-manifest render pacing.
	if _, err := store.ApplyAppManifestSettings(db.ApplyAppManifestSettingsParams{
		AppID: app.ID, Slug: "demo",
		SetRenderSeconds: true, RenderSeconds: 0.8,
	}); err != nil {
		t.Fatal(err)
	}

	// A previous good deployment that exists on disk for restore.
	v1Dir := filepath.Join(appsDir, "demo", "versions", "v1")
	if err := os.MkdirAll(v1Dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateDeployment(db.CreateDeploymentParams{
		AppID: app.ID, Version: "v1", BundleDir: v1Dir,
	}); err != nil {
		t.Fatal(err)
	}

	// The new deploy fails; restoring v1 (a different bundle dir) succeeds.
	srv.SetDeployRunForTest(func(p deploy.Params) (*deploy.PoolResult, error) {
		if p.BundleDir == v1Dir {
			return &deploy.PoolResult{Replicas: []deploy.Result{{Index: 0, PID: 1, Port: 1}}}, nil
		}
		return nil, deploy.ErrBundleRejected
	})

	// The failing bundle declares a DIFFERENT render_seconds that must NOT
	// survive the failed deploy.
	body, ctype := buildBundleUploadFiles(t, map[string]string{
		"app.py":        "print('hi')\n",
		"shinyhub.toml": "[app]\nrender_seconds = 4.2\n",
	})
	token, _ := auth.IssueJWT(u.ID, u.Username, u.Role, "test-secret")
	req := httptest.NewRequest("POST", "/api/apps/demo/deploy", body)
	req.Header.Set("Content-Type", ctype)
	req.Header.Set("Authorization", "Bearer "+token)

	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500 on failed deploy, got %d: %s", rec.Code, rec.Body.String())
	}

	got, err := store.GetAppBySlug("demo")
	if err != nil {
		t.Fatal(err)
	}
	if got.RenderSeconds != 0.8 {
		t.Errorf("render_seconds = %v after failed deploy, want 0.8 (pre-manifest)", got.RenderSeconds)
	}

	// The live proxy must have seen the failed bundle's 4.2 applied (Phase A)
	// and then reverted back to the pre-manifest 0.8 on failure.
	if len(applied) < 2 {
		t.Fatalf("expected at least 2 live render-pacing applications (phase A + revert), got %v", applied)
	}
	if applied[0] != 4.2 {
		t.Errorf("first live-applied render_seconds = %v, want the failed bundle's 4.2", applied[0])
	}
	if last := applied[len(applied)-1]; last != 0.8 {
		t.Errorf("last live-applied render_seconds = %v, want 0.8 (pre-manifest revert)", last)
	}
}
