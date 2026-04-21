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

	"github.com/rvben/shinyhub/internal/api"
	"github.com/rvben/shinyhub/internal/auth"
	"github.com/rvben/shinyhub/internal/config"
	"github.com/rvben/shinyhub/internal/db"
	"github.com/rvben/shinyhub/internal/deploy"
	"github.com/rvben/shinyhub/internal/process"
	"github.com/rvben/shinyhub/internal/proxy"
)

// newQuotaTestServer wires a Server with a real manager and an explicit
// app_quota_mb so the deploy handler can enforce disk quota end-to-end.
func newQuotaTestServer(t *testing.T, appsDir string, quotaMB int) (*api.Server, *db.Store) {
	t.Helper()
	store, err := db.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Migrate(); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{
		Auth: config.AuthConfig{Secret: "test-secret"},
		Storage: config.StorageConfig{
			AppsDir:          appsDir,
			VersionRetention: 5,
			AppQuotaMB:       quotaMB,
		},
	}
	mgr := process.NewManager(appsDir, process.NewNativeRuntime())
	prx := proxy.New()
	srv := api.New(cfg, store, mgr, prx)
	t.Cleanup(func() { _ = store.Close() })
	return srv, store
}

// newMaxBundleTestServer wires a Server with a real manager and an explicit
// MaxBundleMB cap so the deploy handler can enforce the multipart size limit.
func newMaxBundleTestServer(t *testing.T, appsDir string, maxBundleMB int) (*api.Server, *db.Store) {
	t.Helper()
	store, err := db.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Migrate(); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{
		Auth: config.AuthConfig{Secret: "test-secret"},
		Storage: config.StorageConfig{
			AppsDir:      appsDir,
			AppDataDir:   t.TempDir(),
			MaxBundleMB:  maxBundleMB,
		},
	}
	mgr := process.NewManager(appsDir, process.NewNativeRuntime())
	prx := proxy.New()
	srv := api.New(cfg, store, mgr, prx)
	t.Cleanup(func() { _ = store.Close() })
	return srv, store
}

// buildOversizedBundleUpload produces a multipart request body with a zip that
// contains a large file exceeding the given threshold (in bytes uncompressed).
func buildOversizedBundleUpload(t *testing.T, sizeBytes int) (*bytes.Buffer, string) {
	t.Helper()
	var zipBuf bytes.Buffer
	zw := zip.NewWriter(&zipBuf)
	w, err := zw.CreateHeader(&zip.FileHeader{
		Name:   "blob.bin",
		Method: zip.Store, // no compression so zip size ≈ content size
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write(bytes.Repeat([]byte("x"), sizeBytes)); err != nil {
		t.Fatal(err)
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

// buildBundleUpload produces a multipart request body wrapping a tiny zip that
// contains a single file entry. The returned body plus content-type header are
// ready to attach to an *http.Request.
func buildBundleUpload(t *testing.T, fileName, content string) (*bytes.Buffer, string) {
	t.Helper()
	var zipBuf bytes.Buffer
	zw := zip.NewWriter(&zipBuf)
	w, err := zw.Create(fileName)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write([]byte(content)); err != nil {
		t.Fatal(err)
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

func seedOversizedAppDir(t *testing.T, appsDir, slug string, bytes int) {
	t.Helper()
	path := filepath.Join(appsDir, slug, "versions", "stale-version", "blob.dat")
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, make([]byte, bytes), 0644); err != nil {
		t.Fatal(err)
	}
}

func TestDeployApp_RejectsWhenOverQuota(t *testing.T) {
	appsDir := t.TempDir()
	srv, store := newQuotaTestServer(t, appsDir, 1) // 1 MiB quota

	hash, _ := auth.HashPassword("pass")
	_ = store.CreateUser(db.CreateUserParams{Username: "admin", PasswordHash: hash, Role: "admin"})
	u, _ := store.GetUserByUsername("admin")
	_ = store.CreateApp(db.CreateAppParams{Slug: "big", Name: "Big", OwnerID: u.ID})

	// Pre-populate 2 MiB of stale version data so any further extract pushes
	// the slug above the 1 MiB quota.
	seedOversizedAppDir(t, appsDir, "big", int(2*deploy.MiB))

	body, ctype := buildBundleUpload(t, "app.py", "print('hi')\n")

	token, _ := auth.IssueJWT(u.ID, u.Username, u.Role, "test-secret")
	req := httptest.NewRequest("POST", "/api/apps/big/deploy", body)
	req.Header.Set("Content-Type", ctype)
	req.Header.Set("Authorization", "Bearer "+token)

	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)

	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("expected 413, got %d: %s", rec.Code, rec.Body.String())
	}
	if !bytes.Contains(rec.Body.Bytes(), []byte("quota")) {
		t.Errorf("expected error body to mention quota, got %s", rec.Body.String())
	}

	// The rollback must have removed the new version dir so stale state
	// does not leak into the persistent app tree.
	entries, err := os.ReadDir(filepath.Join(appsDir, "big", "versions"))
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.Name() == "stale-version" {
			continue
		}
		t.Errorf("expected only stale-version dir after rollback, found %q", e.Name())
	}
}

func TestDeployApp_QuotaDisabled_DoesNotReject(t *testing.T) {
	appsDir := t.TempDir()
	srv, store := newQuotaTestServer(t, appsDir, 0) // disabled

	hash, _ := auth.HashPassword("pass")
	_ = store.CreateUser(db.CreateUserParams{Username: "admin", PasswordHash: hash, Role: "admin"})
	u, _ := store.GetUserByUsername("admin")
	_ = store.CreateApp(db.CreateAppParams{Slug: "big", Name: "Big", OwnerID: u.ID})
	seedOversizedAppDir(t, appsDir, "big", int(5*deploy.MiB))

	body, ctype := buildBundleUpload(t, "app.py", "print('hi')\n")
	token, _ := auth.IssueJWT(u.ID, u.Username, u.Role, "test-secret")
	req := httptest.NewRequest("POST", "/api/apps/big/deploy", body)
	req.Header.Set("Content-Type", ctype)
	req.Header.Set("Authorization", "Bearer "+token)

	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)

	// With quota disabled, the handler must not short-circuit on 413. It may
	// still fail later (no uv / health check times out), but not with 413.
	if rec.Code == http.StatusRequestEntityTooLarge {
		t.Fatalf("quota disabled should not return 413: %s", rec.Body.String())
	}
}

// TestDeployApp_RejectsOversizedBundle verifies that a deploy upload exceeding
// MaxBundleMB is rejected at the multipart boundary with 413 before the body
// is fully read or written to disk.
func TestDeployApp_RejectsOversizedBundle(t *testing.T) {
	appsDir := t.TempDir()
	srv, store := newMaxBundleTestServer(t, appsDir, 1) // 1 MiB cap

	hash, _ := auth.HashPassword("pass")
	_ = store.CreateUser(db.CreateUserParams{Username: "admin", PasswordHash: hash, Role: "admin"})
	u, _ := store.GetUserByUsername("admin")
	_ = store.CreateApp(db.CreateAppParams{Slug: "demo", Name: "Demo", OwnerID: u.ID})

	// Build a zip whose raw (stored) payload exceeds 1 MiB so the multipart
	// body size surpasses the cap even before extraction.
	body, ctype := buildOversizedBundleUpload(t, 2*1024*1024)

	token, _ := auth.IssueJWT(u.ID, u.Username, u.Role, "test-secret")
	req := httptest.NewRequest("POST", "/api/apps/demo/deploy", body)
	req.Header.Set("Content-Type", ctype)
	req.Header.Set("Authorization", "Bearer "+token)

	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)

	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want 413; body = %s", rec.Code, rec.Body.String())
	}
	if !bytes.Contains(rec.Body.Bytes(), []byte("MiB")) {
		t.Errorf("expected error body to mention cap size, got: %s", rec.Body.String())
	}
}

// TestDeployApp_MaxBundleDisabled_DoesNotRejectBySize verifies that when
// MaxBundleMB is 0 (no cap), a large upload is not rejected at the boundary.
func TestDeployApp_MaxBundleDisabled_DoesNotRejectBySize(t *testing.T) {
	appsDir := t.TempDir()
	srv, store := newMaxBundleTestServer(t, appsDir, 0) // no cap

	hash, _ := auth.HashPassword("pass")
	_ = store.CreateUser(db.CreateUserParams{Username: "admin", PasswordHash: hash, Role: "admin"})
	u, _ := store.GetUserByUsername("admin")
	_ = store.CreateApp(db.CreateAppParams{Slug: "demo", Name: "Demo", OwnerID: u.ID})

	// A 2 MiB upload that would be rejected with a 1 MiB cap.
	body, ctype := buildOversizedBundleUpload(t, 2*1024*1024)

	token, _ := auth.IssueJWT(u.ID, u.Username, u.Role, "test-secret")
	req := httptest.NewRequest("POST", "/api/apps/demo/deploy", body)
	req.Header.Set("Content-Type", ctype)
	req.Header.Set("Authorization", "Bearer "+token)

	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)

	// With no cap, the upload must not be rejected with 413. It may still fail
	// for other reasons (no runtime, health-check timeout), but not size.
	if rec.Code == http.StatusRequestEntityTooLarge {
		t.Fatalf("cap disabled should not return 413: %s", rec.Body.String())
	}
}
