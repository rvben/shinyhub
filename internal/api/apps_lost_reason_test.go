package api_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/rvben/shinyhub/internal/auth"
	"github.com/rvben/shinyhub/internal/db"
	"github.com/rvben/shinyhub/internal/worker"
)

// TestAppsAPI_GetDerivesWorkerUnavailableReason asserts the app-detail envelope
// derives a "worker unavailable" reason for a lost replica whose tier has no
// healthy worker, and drops the reason once a replacement worker joins (mid-heal).
func TestAppsAPI_GetDerivesWorkerUnavailableReason(t *testing.T) {
	srv, store, _ := newManagerTestServer(t)
	reg, err := worker.NewRegistry(store)
	if err != nil {
		t.Fatalf("new registry: %v", err)
	}
	srv.SetWorkerRegistry(reg)

	hash, _ := auth.HashPassword("pass")
	store.CreateUser(db.CreateUserParams{Username: "owner", PasswordHash: hash, Role: "developer"})
	owner, _ := store.GetUserByUsername("owner")
	store.CreateApp(db.CreateAppParams{Slug: "demo", Name: "Demo", OwnerID: owner.ID})
	app, _ := store.GetAppBySlug("demo")

	// A replica lost to a dead worker on the "remote" tier, no replacement yet.
	store.UpsertReplica(db.UpsertReplicaParams{AppID: app.ID, Index: 0, Status: db.ReplicaStatusLost, Tier: "remote"})

	token, _ := auth.IssueJWT(owner.ID, "owner", "developer", "test-secret")
	get := func() *db.Replica {
		t.Helper()
		req := authedRequest(t, "GET", "/api/apps/demo", nil, token)
		rec := httptest.NewRecorder()
		srv.Router().ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
		}
		var body struct {
			ReplicasStatus []*db.Replica `json:"replicas_status"`
		}
		if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if len(body.ReplicasStatus) != 1 {
			t.Fatalf("want 1 replica, got %d", len(body.ReplicasStatus))
		}
		return body.ReplicasStatus[0]
	}

	if r := get(); r.Reason != "worker unavailable" {
		t.Errorf("lost replica, no worker: reason = %q, want %q", r.Reason, "worker unavailable")
	}

	// A replacement worker joins the tier: the lost replica is now mid-heal, so
	// the derived reason must clear (the watchdog will re-place it on the next tick).
	w1, err := reg.Register(worker.RegisterParams{Name: "w1", AdvertiseAddr: "w:8443", Tier: "remote", Fingerprint: "fp", Version: "v1"})
	if err != nil {
		t.Fatalf("register worker: %v", err)
	}
	// A worker is healthy/routable only after its first heartbeat (Register -> joining).
	if err := reg.Heartbeat(w1.NodeID, "fp"); err != nil {
		t.Fatalf("heartbeat worker: %v", err)
	}
	if r := get(); r.Reason != "" {
		t.Errorf("lost replica with healthy worker present: reason = %q, want empty", r.Reason)
	}
}

// TestMetricsPoll_SurfacesLostReason asserts the live metrics poll
// (GET /api/apps/:slug/metrics) reflects a DB lost replica as "lost" with the
// same "worker unavailable" reason the app envelope derives. Without this the
// 10s poll, built from live manager state that has no "lost" concept, would
// revert a lost replica to "stopped" and drop the reason the detail seed showed.
func TestMetricsPoll_SurfacesLostReason(t *testing.T) {
	srv, store, _ := newManagerTestServer(t)
	reg, err := worker.NewRegistry(store)
	if err != nil {
		t.Fatalf("new registry: %v", err)
	}
	srv.SetWorkerRegistry(reg)

	hash, _ := auth.HashPassword("pass")
	store.CreateUser(db.CreateUserParams{Username: "owner", PasswordHash: hash, Role: "developer"})
	owner, _ := store.GetUserByUsername("owner")
	store.CreateApp(db.CreateAppParams{Slug: "demo", Name: "Demo", OwnerID: owner.ID})
	app, _ := store.GetAppBySlug("demo")
	store.UpsertReplica(db.UpsertReplicaParams{AppID: app.ID, Index: 0, Status: db.ReplicaStatusLost, Tier: "remote"})

	token, _ := auth.IssueJWT(owner.ID, "owner", "developer", "test-secret")
	req := authedRequest(t, "GET", "/api/apps/demo/metrics", nil, token)
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var body struct {
		Replicas []struct {
			Index  int    `json:"index"`
			Status string `json:"status"`
			Reason string `json:"reason"`
		} `json:"replicas"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	found := false
	for _, r := range body.Replicas {
		if r.Index != 0 {
			continue
		}
		found = true
		if r.Status != "lost" {
			t.Errorf("status = %q, want lost", r.Status)
		}
		if r.Reason != "worker unavailable" {
			t.Errorf("reason = %q, want %q", r.Reason, "worker unavailable")
		}
	}
	if !found {
		t.Fatalf("lost replica index 0 missing from poll response: %s", rec.Body.String())
	}
}
