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
	if _, err := reg.Register(worker.RegisterParams{Name: "w1", AdvertiseAddr: "w:8443", Tier: "remote", Fingerprint: "fp", Version: "v1"}); err != nil {
		t.Fatalf("register worker: %v", err)
	}
	if r := get(); r.Reason != "" {
		t.Errorf("lost replica with healthy worker present: reason = %q, want empty", r.Reason)
	}
}
