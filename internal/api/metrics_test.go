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
	"github.com/rvben/shinyhub/internal/proxy"
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
	srv, store := newTestServer(t)
	token, _ := seedUserAndJWT(t, store, "alice", "admin")
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

// newMetricsTestServerWithProxy mirrors newMetricsTestServer but also wires a
// real Proxy into the Server so tests can exercise the per-replica metrics
// fan-out (ReplicaSessionCounts + PoolCap).
func newMetricsTestServerWithProxy(t *testing.T) (*api.Server, *db.Store, *process.Manager, *proxy.Proxy) {
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
	prx := proxy.New()
	srv := api.New(cfg, store, mgr, prx)
	t.Cleanup(func() { store.Close() })
	return srv, store, mgr, prx
}

// TestGetMetrics_FansOutAcrossReplicas verifies that the metrics endpoint
// returns a per-replica breakdown, reflects the proxy's session cap, and
// keeps legacy top-level fields mirroring the first running replica.
func TestGetMetrics_FansOutAcrossReplicas(t *testing.T) {
	srv, store, mgr, prx := newMetricsTestServerWithProxy(t)
	hash, _ := auth.HashPassword("pass")
	store.CreateUser(db.CreateUserParams{Username: "owner", PasswordHash: hash, Role: "developer"})
	u, _ := store.GetUserByUsername("owner")
	store.CreateApp(db.CreateAppParams{Slug: "myapp", Name: "My App", OwnerID: u.ID})

	// Inject two running replicas and a fake sampler with known stats.
	mgr.ForceEntry("myapp", process.ProcessInfo{Slug: "myapp", Index: 0, PID: 1001, Status: process.StatusRunning})
	mgr.ForceEntry("myapp", process.ProcessInfo{Slug: "myapp", Index: 1, PID: 1002, Status: process.StatusRunning})
	srv.SetSampler(fakeMetricsSampler{stats: process.Stats{CPUPercent: 3.25, RSSBytes: 1 << 21}})

	// Register the proxy pool with two replicas and a known cap. The
	// backend URLs are placeholders — the metrics endpoint only reads
	// session counters and the cap, never forwards traffic.
	prx.SetPoolSize("myapp", 2)
	prx.SetPoolCap("myapp", 15)
	if err := prx.RegisterReplica("myapp", 0, "http://127.0.0.1:1"); err != nil {
		t.Fatalf("register replica 0: %v", err)
	}
	if err := prx.RegisterReplica("myapp", 1, "http://127.0.0.1:2"); err != nil {
		t.Fatalf("register replica 1: %v", err)
	}

	token, _ := auth.IssueJWT(u.ID, "owner", "developer", "test-secret")
	req := authedRequest(t, "GET", "/api/apps/myapp/metrics", nil, token)
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var resp struct {
		Status      string  `json:"status"`
		SessionsCap int     `json:"sessions_cap"`
		PID         int     `json:"pid"`
		CPUPercent  float64 `json:"cpu_percent"`
		RSSBytes    int64   `json:"rss_bytes"`
		Replicas    []struct {
			Index      int     `json:"index"`
			Status     string  `json:"status"`
			PID        int     `json:"pid"`
			CPUPercent float64 `json:"cpu_percent"`
			RSSBytes   int64   `json:"rss_bytes"`
			Sessions   int64   `json:"sessions"`
		} `json:"replicas"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}

	if resp.Status != "running" {
		t.Errorf("expected top-level status=running, got %q", resp.Status)
	}
	if resp.SessionsCap != 15 {
		t.Errorf("expected sessions_cap=15, got %d", resp.SessionsCap)
	}
	if len(resp.Replicas) != 2 {
		t.Fatalf("expected 2 replicas, got %d", len(resp.Replicas))
	}

	for i, rm := range resp.Replicas {
		if rm.Index != i {
			t.Errorf("replica[%d]: expected index=%d, got %d", i, i, rm.Index)
		}
		if rm.Status != "running" {
			t.Errorf("replica[%d]: expected status=running, got %q", i, rm.Status)
		}
		if rm.CPUPercent != 3.25 {
			t.Errorf("replica[%d]: expected cpu_percent=3.25, got %v", i, rm.CPUPercent)
		}
		if rm.RSSBytes != 1<<21 {
			t.Errorf("replica[%d]: expected rss_bytes=%d, got %d", i, 1<<21, rm.RSSBytes)
		}
		if rm.Sessions != 0 {
			t.Errorf("replica[%d]: expected sessions=0 (no in-flight), got %d", i, rm.Sessions)
		}
	}
	if resp.Replicas[0].PID != 1001 || resp.Replicas[1].PID != 1002 {
		t.Errorf("expected per-replica PIDs (1001, 1002), got (%d, %d)",
			resp.Replicas[0].PID, resp.Replicas[1].PID)
	}

	// Legacy fields must mirror the first running replica.
	if resp.PID != 1001 {
		t.Errorf("expected legacy pid=1001, got %d", resp.PID)
	}
	if resp.CPUPercent != 3.25 {
		t.Errorf("expected legacy cpu_percent=3.25, got %v", resp.CPUPercent)
	}
	if resp.RSSBytes != 1<<21 {
		t.Errorf("expected legacy rss_bytes=%d, got %d", 1<<21, resp.RSSBytes)
	}
}

// TestGetMetrics_StoppedReplicaSlot verifies that a nil replica slot (e.g.
// one replica crashed while the other is still running) is reported as
// "stopped" in the per-replica array without being dropped, and the top-level
// status remains "running" as long as any replica is up.
func TestGetMetrics_StoppedReplicaSlot(t *testing.T) {
	srv, store, mgr, prx := newMetricsTestServerWithProxy(t)
	hash, _ := auth.HashPassword("pass")
	store.CreateUser(db.CreateUserParams{Username: "owner", PasswordHash: hash, Role: "developer"})
	u, _ := store.GetUserByUsername("owner")
	store.CreateApp(db.CreateAppParams{Slug: "myapp", Name: "My App", OwnerID: u.ID})

	// Only replica index 1 is populated; index 0 left nil to simulate a
	// crashed/not-yet-started slot.
	mgr.ForceEntry("myapp", process.ProcessInfo{Slug: "myapp", Index: 1, PID: 2002, Status: process.StatusRunning})
	srv.SetSampler(fakeMetricsSampler{stats: process.Stats{CPUPercent: 1.1, RSSBytes: 1 << 20}})

	prx.SetPoolSize("myapp", 2)
	prx.SetPoolCap("myapp", 0) // uncapped
	if err := prx.RegisterReplica("myapp", 1, "http://127.0.0.1:1"); err != nil {
		t.Fatalf("register replica 1: %v", err)
	}

	token, _ := auth.IssueJWT(u.ID, "owner", "developer", "test-secret")
	req := authedRequest(t, "GET", "/api/apps/myapp/metrics", nil, token)
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var resp struct {
		Status      string `json:"status"`
		SessionsCap int    `json:"sessions_cap"`
		Replicas    []struct {
			Index  int    `json:"index"`
			Status string `json:"status"`
			PID    int    `json:"pid"`
		} `json:"replicas"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}

	if resp.Status != "running" {
		t.Errorf("expected status=running (at least one replica up), got %q", resp.Status)
	}
	if resp.SessionsCap != 0 {
		t.Errorf("expected sessions_cap=0 (uncapped), got %d", resp.SessionsCap)
	}
	if len(resp.Replicas) != 2 {
		t.Fatalf("expected 2 replica slots, got %d", len(resp.Replicas))
	}
	if resp.Replicas[0].Status != "stopped" {
		t.Errorf("expected replica[0] status=stopped, got %q", resp.Replicas[0].Status)
	}
	if resp.Replicas[0].PID != 0 {
		t.Errorf("expected replica[0] pid=0 for empty slot, got %d", resp.Replicas[0].PID)
	}
	if resp.Replicas[1].Status != "running" {
		t.Errorf("expected replica[1] status=running, got %q", resp.Replicas[1].Status)
	}
	if resp.Replicas[1].PID != 2002 {
		t.Errorf("expected replica[1] pid=2002, got %d", resp.Replicas[1].PID)
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
