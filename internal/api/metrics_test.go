package api_test

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/rvben/shinyhub/internal/api"
	"github.com/rvben/shinyhub/internal/auth"
	"github.com/rvben/shinyhub/internal/config"
	"github.com/rvben/shinyhub/internal/db"
	"github.com/rvben/shinyhub/internal/process"
)

// fakeMetricsSampler implements process.Sampler for metrics handler tests.
type fakeMetricsSampler struct {
	stats process.Stats
	err   error
}

func (f fakeMetricsSampler) Sample(_ process.RunHandle) (process.Stats, error) {
	return f.stats, f.err
}

func TestGetMetrics_Running(t *testing.T) {
	srv, store := newTestServer(t)
	hash, _ := auth.HashPassword("pass")
	store.CreateUser(db.CreateUserParams{Username: "owner", PasswordHash: hash, Role: "developer"})
	u, _ := store.GetUserByUsername("owner")
	store.CreateApp(db.CreateAppParams{Slug: "myapp", Name: "My App", OwnerID: u.ID})

	// Inject a fake sampler with known stats.
	srv.SetSampler(fakeMetricsSampler{stats: process.Stats{CPUPercent: 2.5, RSSBytes: 134217728}})

	token, _ := auth.IssueJWT(u.ID, "owner", "developer", "test-secret")
	req := authedRequest(t, "GET", "/api/apps/myapp/metrics", nil, token)
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)

	// Without a manager entry, the DB status is returned ("stopped" for a
	// newly created, never-deployed app).
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var resp map[string]any
	json.NewDecoder(rec.Body).Decode(&resp)
	if resp["status"] != "stopped" {
		t.Errorf("expected status=stopped, got %v", resp["status"])
	}
}

func TestGetMetrics_NotFound(t *testing.T) {
	srv, _ := newTestServer(t)
	token, _ := auth.IssueJWT(1, "alice", "admin", "test-secret")
	req := authedRequest(t, "GET", "/api/apps/nonexistent/metrics", nil, token)
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Errorf("expected 404, got %d", rec.Code)
	}
}

func newMetricsTestServer(t *testing.T) (*api.Server, *db.Store, *process.Manager) {
	t.Helper()
	appsDir := t.TempDir()
	store, err := db.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Migrate(); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{
		Auth:    config.AuthConfig{Secret: "test-secret"},
		Storage: config.StorageConfig{AppsDir: appsDir},
	}
	mgr := process.NewManager(appsDir, process.NewNativeRuntime())
	srv := api.New(cfg, store, mgr, nil)
	t.Cleanup(func() { store.Close() })
	return srv, store, mgr
}

func TestGetMetrics_NotRunning(t *testing.T) {
	srv, store, mgr := newMetricsTestServer(t)
	hash, _ := auth.HashPassword("pass")
	store.CreateUser(db.CreateUserParams{Username: "owner", PasswordHash: hash, Role: "developer"})
	u, _ := store.GetUserByUsername("owner")
	store.CreateApp(db.CreateAppParams{Slug: "myapp", Name: "My App", OwnerID: u.ID})

	// Inject a stopped entry (no real process needed).
	mgr.ForceEntry("myapp", process.ProcessInfo{Slug: "myapp", Status: process.StatusStopped})

	token, _ := auth.IssueJWT(u.ID, "owner", "developer", "test-secret")
	req := authedRequest(t, "GET", "/api/apps/myapp/metrics", nil, token)
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var resp map[string]any
	json.NewDecoder(rec.Body).Decode(&resp)
	if resp["status"] != "stopped" {
		t.Errorf("expected status=stopped, got %v", resp["status"])
	}
}

func TestGetMetrics_SamplerError(t *testing.T) {
	srv, store, mgr := newMetricsTestServer(t)
	hash, _ := auth.HashPassword("pass")
	store.CreateUser(db.CreateUserParams{Username: "owner", PasswordHash: hash, Role: "developer"})
	u, _ := store.GetUserByUsername("owner")
	store.CreateApp(db.CreateAppParams{Slug: "myapp", Name: "My App", OwnerID: u.ID})

	// Inject a running entry, then make the sampler error.
	mgr.ForceEntry("myapp", process.ProcessInfo{Slug: "myapp", PID: 99999, Status: process.StatusRunning})
	srv.SetSampler(fakeMetricsSampler{err: errors.New("process gone")})

	token, _ := auth.IssueJWT(u.ID, "owner", "developer", "test-secret")
	req := authedRequest(t, "GET", "/api/apps/myapp/metrics", nil, token)
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var resp map[string]any
	json.NewDecoder(rec.Body).Decode(&resp)
	if resp["status"] != "stopped" {
		t.Errorf("expected status=stopped on sampler error, got %v", resp["status"])
	}
}

func TestGetMetrics_RunningWithStats(t *testing.T) {
	srv, store, mgr := newMetricsTestServer(t)
	hash, _ := auth.HashPassword("pass")
	store.CreateUser(db.CreateUserParams{Username: "owner", PasswordHash: hash, Role: "developer"})
	u, _ := store.GetUserByUsername("owner")
	store.CreateApp(db.CreateAppParams{Slug: "myapp", Name: "My App", OwnerID: u.ID})

	// Inject a running entry and a fake sampler with known stats.
	mgr.ForceEntry("myapp", process.ProcessInfo{Slug: "myapp", PID: 1234, Status: process.StatusRunning})
	srv.SetSampler(fakeMetricsSampler{stats: process.Stats{CPUPercent: 2.5, RSSBytes: 1 << 20}})

	token, _ := auth.IssueJWT(u.ID, "owner", "developer", "test-secret")
	req := authedRequest(t, "GET", "/api/apps/myapp/metrics", nil, token)
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var resp map[string]any
	json.NewDecoder(rec.Body).Decode(&resp)
	if resp["status"] != "running" {
		t.Errorf("expected status=running, got %v", resp["status"])
	}
	if resp["cpu_percent"] != 2.5 {
		t.Errorf("expected cpu_percent=2.5, got %v", resp["cpu_percent"])
	}
	if int64(resp["rss_bytes"].(float64)) != 1<<20 {
		t.Errorf("expected rss_bytes=%d, got %v", 1<<20, resp["rss_bytes"])
	}
}
