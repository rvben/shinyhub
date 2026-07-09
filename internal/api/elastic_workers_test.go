package api_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/rvben/shinyhub/internal/auth"
	"github.com/rvben/shinyhub/internal/config"
	"github.com/rvben/shinyhub/internal/db"
	"github.com/rvben/shinyhub/internal/process"
	"github.com/rvben/shinyhub/internal/proxy"
)

// seedElasticPool wires a grouped pool (2 sessions/worker, max 5) with the
// real admission path: two cold clients pack slot 0 to its cap, a third
// allocates slot 1 (still booting). Slot 0 is then registered ready and given
// a manager entry with pid/port; slot 1 has no process yet.
func seedElasticPool(t *testing.T, prx *proxy.Proxy, mgr *process.Manager, slug string) {
	t.Helper()
	prx.SetPoolMode(slug, config.IsolationGrouped, 2, 5)
	prx.SetSpawnFunc(func(string, int) {})
	for i := 0; i < 3; i++ {
		req := httptest.NewRequest("GET", "/app/"+slug+"/", nil)
		rec := httptest.NewRecorder()
		prx.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("cold client %d: want 200 splash, got %d", i, rec.Code)
		}
	}
	if err := prx.RegisterElasticWorker(slug, 0, "http://127.0.0.1:1", nil, 7); err != nil {
		t.Fatalf("RegisterElasticWorker: %v", err)
	}
	mgr.ForceEntry(slug, process.ProcessInfo{Slug: slug, Index: 0, PID: 4321, Port: 20101, Status: process.StatusRunning})
}

type workerPoolResp struct {
	Mode              string `json:"mode"`
	SessionsPerWorker int    `json:"sessions_per_worker"`
	MaxWorkers        int    `json:"max_workers"`
	Ceiling           int    `json:"ceiling"`
	Workers           []struct {
		SlotID   int    `json:"slot_id"`
		Status   string `json:"status"`
		Sessions int    `json:"sessions"`
		PID      int    `json:"pid"`
		Port     int    `json:"port"`
	} `json:"workers"`
}

// TestAppsAPI_GetIncludesWorkerPool verifies the app envelope carries the
// live per-worker capacity view for elastic apps: slot, routing status,
// bound sessions, and pid/port merged from the process manager.
func TestAppsAPI_GetIncludesWorkerPool(t *testing.T) {
	srv, store, mgr, prx := newMetricsTestServerWithProxy(t)
	hash, _ := testHashPassword("pass")
	store.CreateUser(db.CreateUserParams{Username: "owner", PasswordHash: hash, Role: "developer"})
	u, _ := store.GetUserByUsername("owner")
	store.CreateApp(db.CreateAppParams{Slug: "grpapp", Name: "Grp", OwnerID: u.ID})

	seedElasticPool(t, prx, mgr, "grpapp")

	token, _ := auth.IssueJWT(u.ID, "owner", "developer", "test-secret")
	req := authedRequest(t, "GET", "/api/apps/grpapp", nil, token)
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var resp struct {
		WorkerPool *workerPoolResp `json:"worker_pool"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	wp := resp.WorkerPool
	if wp == nil {
		t.Fatal("worker_pool missing from the app envelope for an elastic app")
	}
	if wp.Mode != "grouped" || wp.SessionsPerWorker != 2 || wp.MaxWorkers != 5 || wp.Ceiling != 10 {
		t.Errorf("pool = %+v, want grouped 2/worker max 5 ceiling 10", wp)
	}
	if len(wp.Workers) != 2 {
		t.Fatalf("workers = %+v, want 2 entries", wp.Workers)
	}
	w0, w1 := wp.Workers[0], wp.Workers[1]
	if w0.SlotID != 0 || w0.Status != "running" || w0.Sessions != 2 || w0.PID != 4321 || w0.Port != 20101 {
		t.Errorf("worker 0 = %+v, want slot 0 running sessions 2 pid 4321 port 20101", w0)
	}
	if w1.SlotID != 1 || w1.Status != "booting" || w1.Sessions != 1 {
		t.Errorf("worker 1 = %+v, want slot 1 booting sessions 1", w1)
	}
	if w1.PID != 0 || w1.Port != 0 {
		t.Errorf("worker 1 has pid/port %d/%d; a not-yet-started worker must not fabricate them", w1.PID, w1.Port)
	}
}

// TestAppsAPI_GetOmitsWorkerPoolForMultiplex pins that multiplex apps carry
// no worker_pool key: absence of the capacity view must be distinguishable
// from an elastic pool with zero workers.
func TestAppsAPI_GetOmitsWorkerPoolForMultiplex(t *testing.T) {
	srv, store, _, prx := newMetricsTestServerWithProxy(t)
	hash, _ := testHashPassword("pass")
	store.CreateUser(db.CreateUserParams{Username: "owner", PasswordHash: hash, Role: "developer"})
	u, _ := store.GetUserByUsername("owner")
	store.CreateApp(db.CreateAppParams{Slug: "muxapp", Name: "Mux", OwnerID: u.ID})
	prx.SetPoolSize("muxapp", 1)

	token, _ := auth.IssueJWT(u.ID, "owner", "developer", "test-secret")
	req := authedRequest(t, "GET", "/api/apps/muxapp", nil, token)
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var resp map[string]json.RawMessage
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if _, present := resp["worker_pool"]; present {
		t.Error("worker_pool must be omitted for multiplex apps")
	}
}

// TestGetMetrics_ElasticSessionsAndBootingSlots verifies the metrics poll
// reports real per-worker session counts for elastic pools (not the
// multiplex -1), surfaces proxy-only booting slots that have no process yet,
// and carries the pool's isolation mode and ceiling inputs.
func TestGetMetrics_ElasticSessionsAndBootingSlots(t *testing.T) {
	srv, store, mgr, prx := newMetricsTestServerWithProxy(t)
	hash, _ := testHashPassword("pass")
	store.CreateUser(db.CreateUserParams{Username: "owner", PasswordHash: hash, Role: "developer"})
	u, _ := store.GetUserByUsername("owner")
	store.CreateApp(db.CreateAppParams{Slug: "grpapp", Name: "Grp", OwnerID: u.ID})

	seedElasticPool(t, prx, mgr, "grpapp")
	// A terminated worker leaves a stopped manager entry behind, but its slot
	// is gone from the pool. Elastic slot IDs are never reused, so keeping
	// such rows would grow the table forever under worker churn.
	mgr.ForceEntry("grpapp", process.ProcessInfo{Slug: "grpapp", Index: 2, Status: process.StatusStopped})
	srv.SetSampler(fakeMetricsSampler{stats: process.Stats{CPUPercent: 1.5, RSSBytes: 1 << 20}})

	token, _ := auth.IssueJWT(u.ID, "owner", "developer", "test-secret")
	req := authedRequest(t, "GET", "/api/apps/grpapp/metrics", nil, token)
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var resp struct {
		SessionsCap     int    `json:"sessions_cap"`
		WorkerIsolation string `json:"worker_isolation"`
		MaxWorkers      int    `json:"max_workers"`
		Replicas        []struct {
			Index    int    `json:"index"`
			Status   string `json:"status"`
			PID      int    `json:"pid"`
			Sessions int64  `json:"sessions"`
			RSSBytes int64  `json:"rss_bytes"`
		} `json:"replicas"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.SessionsCap != 2 {
		t.Errorf("sessions_cap = %d, want 2 (per-worker cap)", resp.SessionsCap)
	}
	if resp.WorkerIsolation != "grouped" || resp.MaxWorkers != 5 {
		t.Errorf("worker_isolation/max_workers = %q/%d, want grouped/5", resp.WorkerIsolation, resp.MaxWorkers)
	}
	if len(resp.Replicas) != 2 {
		t.Fatalf("replicas = %+v, want exactly 2 rows (live slots 0 and 1; the terminated slot 2 must not linger)", resp.Replicas)
	}
	r0, r1 := resp.Replicas[0], resp.Replicas[1]
	if r0.Index != 0 || r0.Status != "running" || r0.Sessions != 2 || r0.PID != 4321 {
		t.Errorf("row 0 = %+v, want index 0 running sessions 2 pid 4321", r0)
	}
	if r0.RSSBytes != 1<<20 {
		t.Errorf("row 0 rss = %d, want %d (sampled)", r0.RSSBytes, 1<<20)
	}
	if r1.Index != 1 || r1.Status != "booting" || r1.Sessions != 1 {
		t.Errorf("row 1 = %+v, want index 1 booting sessions 1", r1)
	}
	if r1.PID != 0 {
		t.Errorf("row 1 pid = %d, want 0 (no process yet)", r1.PID)
	}
}
