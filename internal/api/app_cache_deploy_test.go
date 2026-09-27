package api

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/rvben/shinyhub/internal/config"
	"github.com/rvben/shinyhub/internal/db"
	"github.com/rvben/shinyhub/internal/lifecycle/scheduler"
	"github.com/rvben/shinyhub/internal/storage"
)

// A redeploy starts the new activation with its own namespace and releases
// the one the replaced activation used once nothing runs it any more.
func TestDeploy_ReleasesTheReplacedActivationsCache(t *testing.T) {
	srv, store, token, mgr, _, _ := buildManifestE2EServer(t, config.RuntimeConfig{})
	srv.SetJobs(nil, scheduler.New(nil, store, time.UTC))
	root := filepath.Join(t.TempDir(), "cache")
	srv.cfg.Storage.AppCacheDir = root
	if err := mgr.SetAppCache(root, 64); err != nil {
		t.Fatal(err)
	}
	admin, _ := store.GetUserByUsername("admin")
	if _, err := store.CreateApp(db.CreateAppParams{Slug: "cached", Name: "Cached", OwnerID: admin.ID}); err != nil {
		t.Fatal(err)
	}

	deployOnce := func(src string) int64 {
		t.Helper()
		body, ctype := buildMultiFileBundleUpload(t, map[string]string{"app.py": src})
		req := httptest.NewRequest("POST", "/api/apps/cached/deploy", body)
		req.Header.Set("Content-Type", ctype)
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("X-ShinyHub-Allow-Downtime", "1")
		rec := httptest.NewRecorder()
		srv.Router().ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("deploy status %d: %s", rec.Code, rec.Body.String())
		}
		var active int64
		if err := store.DB().QueryRow(`SELECT active_deployment_id FROM apps WHERE slug = ?`, "cached").Scan(&active); err != nil {
			t.Fatalf("no active deployment after deploy: %v", err)
		}
		return active
	}

	first := deployOnce("print(1)\n")
	if _, err := os.Stat(storage.CacheNamespace(root, "cached", first)); err != nil {
		t.Fatalf("the first activation got no namespace: %v", err)
	}
	second := deployOnce("print(2)\n")
	if second == first {
		t.Fatal("the redeploy did not create a new activation")
	}
	if _, err := os.Stat(storage.CacheNamespace(root, "cached", second)); err != nil {
		t.Fatalf("the new activation got no namespace: %v", err)
	}

	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, err := os.Stat(storage.CacheNamespace(root, "cached", first)); os.IsNotExist(err) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the replaced activation's namespace was never released")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if _, err := os.Stat(storage.CacheNamespace(root, "cached", second)); err != nil {
		t.Fatalf("releasing the old namespace removed the active one: %v", err)
	}
}
