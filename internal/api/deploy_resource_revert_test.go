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

	"github.com/rvben/shinyhub/internal/auth"
	"github.com/rvben/shinyhub/internal/db"
	"github.com/rvben/shinyhub/internal/deploy"
)

func resInt(v int) *int { return &v }

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

	hash, _ := auth.HashPassword("pass")
	_ = store.CreateUser(db.CreateUserParams{Username: "admin", PasswordHash: hash, Role: "admin"})
	u, _ := store.GetUserByUsername("admin")
	_ = store.CreateApp(db.CreateAppParams{Slug: "demo", Name: "Demo", OwnerID: u.ID})
	app, _ := store.GetAppBySlug("demo")

	// Pre-manifest resource policy.
	if err := store.ApplyAppManifestSettings(db.ApplyAppManifestSettingsParams{
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

	hash, _ := auth.HashPassword("pass")
	_ = store.CreateUser(db.CreateUserParams{Username: "admin", PasswordHash: hash, Role: "admin"})
	u, _ := store.GetUserByUsername("admin")
	_ = store.CreateApp(db.CreateAppParams{Slug: "demo", Name: "Demo", OwnerID: u.ID})
	app, _ := store.GetAppBySlug("demo")

	// Pre-manifest autoscale policy: enabled, bounds [1,2], target 0.5.
	if err := store.ApplyAppManifestSettings(db.ApplyAppManifestSettingsParams{
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
