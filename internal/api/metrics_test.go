package api_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/rvben/shinyhub/internal/auth"
	"github.com/rvben/shinyhub/internal/db"
	"github.com/rvben/shinyhub/internal/process"
)

// fakeMetricsSampler implements process.Sampler for metrics handler tests.
type fakeMetricsSampler struct {
	stats process.Stats
	err   error
}

func (f fakeMetricsSampler) Sample(_ int) (process.Stats, error) {
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

	// Without a manager entry, we expect {"status":"unknown"} with 200.
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var resp map[string]any
	json.NewDecoder(rec.Body).Decode(&resp)
	if resp["status"] != "unknown" {
		t.Errorf("expected status=unknown, got %v", resp["status"])
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
