package api_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/rvben/shinyhub/internal/auth"
	"github.com/rvben/shinyhub/internal/db"
	"github.com/rvben/shinyhub/internal/history"
)

type historyResp struct {
	WindowSeconds   int64 `json:"window_seconds"`
	IntervalSeconds int64 `json:"interval_seconds"`
	Series          struct {
		TS        []int64   `json:"ts"`
		CPU       []float64 `json:"cpu"`
		RSS       []int64   `json:"rss"`
		Sessions  []int64   `json:"sessions"`
		Instances []int     `json:"instances"`
	} `json:"series"`
}

func seedHistoryApp(t *testing.T, store *db.Store) (token string) {
	t.Helper()
	hash, _ := testHashPassword("pass")
	store.CreateUser(db.CreateUserParams{Username: "owner", PasswordHash: hash, Role: "developer"})
	u, _ := store.GetUserByUsername("owner")
	store.CreateApp(db.CreateAppParams{Slug: "myapp", Name: "My App", OwnerID: u.ID})
	token, _ = auth.IssueJWT(u.ID, "owner", "developer", "test-secret")
	return token
}

func TestGetMetricsHistory_ReturnsSeries(t *testing.T) {
	srv, store := newTestServer(t)
	token := seedHistoryApp(t, store)

	st := history.NewStore(12*time.Hour, 15*time.Second)
	now := time.Now().Unix()
	st.Append("myapp", history.Sample{TS: now - 15, CPU: 10, RSS: 100, Sessions: 1, Instances: 1})
	st.Append("myapp", history.Sample{TS: now, CPU: 20, RSS: 200, Sessions: 2, Instances: 2})
	srv.SetHistory(st)

	req := authedRequest(t, "GET", "/api/apps/myapp/metrics/history", nil, token)
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var resp historyResp
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.WindowSeconds != 43200 {
		t.Errorf("window_seconds = %d, want 43200", resp.WindowSeconds)
	}
	if resp.IntervalSeconds != 15 {
		t.Errorf("interval_seconds = %d, want 15", resp.IntervalSeconds)
	}
	if len(resp.Series.CPU) != 2 || resp.Series.CPU[0] != 10 || resp.Series.CPU[1] != 20 {
		t.Errorf("series.cpu = %v, want [10 20]", resp.Series.CPU)
	}
	if len(resp.Series.Instances) != 2 || resp.Series.Instances[1] != 2 {
		t.Errorf("series.instances = %v, want [...2]", resp.Series.Instances)
	}
}

func TestGetMetricsHistory_KnownAppNoSamplesEmptySeries(t *testing.T) {
	srv, store := newTestServer(t)
	token := seedHistoryApp(t, store)
	srv.SetHistory(history.NewStore(12*time.Hour, 15*time.Second))

	req := authedRequest(t, "GET", "/api/apps/myapp/metrics/history", nil, token)
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	// Empty series must marshal as [] (non-nil) so the JS consumer never sees null.
	if body := rec.Body.String(); !strings.Contains(body, `"cpu":[]`) || !strings.Contains(body, `"ts":[]`) {
		t.Errorf("empty series must marshal arrays as [], got %s", body)
	}
}

func TestGetMetricsHistory_DisabledReturnsEmptySeries(t *testing.T) {
	srv, store := newTestServer(t)
	token := seedHistoryApp(t, store)
	// No SetHistory: collection disabled / not wired.

	req := authedRequest(t, "GET", "/api/apps/myapp/metrics/history", nil, token)
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var resp historyResp
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.WindowSeconds != 0 {
		t.Errorf("window_seconds = %d, want 0 when disabled", resp.WindowSeconds)
	}
	if resp.Series.CPU == nil {
		t.Error("series.cpu must be non-nil ([]) even when disabled")
	}
}

func TestGetMetricsHistory_NotFound(t *testing.T) {
	srv, store := newTestServer(t)
	token, _ := seedUserAndJWT(t, store, "alice", "admin")
	srv.SetHistory(history.NewStore(12*time.Hour, 15*time.Second))

	req := authedRequest(t, "GET", "/api/apps/nonexistent/metrics/history", nil, token)
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Errorf("expected 404 for unknown app, got %d", rec.Code)
	}
}
