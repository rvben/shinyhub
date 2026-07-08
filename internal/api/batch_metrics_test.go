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

// GET /api/apps/metrics returns the requested apps' metrics keyed by slug in one
// response, skipping apps the caller cannot view and unknown slugs - so the
// dashboard populates every card with a single request.
func TestBatchMetrics_RequestedVisibleAppsKeyedBySlug(t *testing.T) {
	srv, store := newTestServer(t)
	hash, _ := testHashPassword("pass")
	store.CreateUser(db.CreateUserParams{Username: "owner", PasswordHash: hash, Role: "developer"})
	owner, _ := store.GetUserByUsername("owner")
	store.CreateUser(db.CreateUserParams{Username: "other", PasswordHash: hash, Role: "developer"})
	other, _ := store.GetUserByUsername("other")

	store.CreateApp(db.CreateAppParams{Slug: "mine1", Name: "Mine 1", OwnerID: owner.ID})
	store.CreateApp(db.CreateAppParams{Slug: "mine2", Name: "Mine 2", OwnerID: owner.ID})
	store.CreateApp(db.CreateAppParams{Slug: "secret", Name: "Secret", OwnerID: other.ID}) // private, not visible to owner

	token, _ := auth.IssueJWT(owner.ID, "owner", "developer", "test-secret")
	req := authedRequest(t, "GET", "/api/apps/metrics?slugs=mine1,mine2,secret,ghost", nil, token)
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
	var body struct {
		Metrics map[string]struct {
			Status string `json:"status"`
		} `json:"metrics"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode (route should hit the batch handler, not handleGetApp): %v", err)
	}
	if _, ok := body.Metrics["mine1"]; !ok {
		t.Error("mine1 missing from batch metrics")
	}
	if _, ok := body.Metrics["mine2"]; !ok {
		t.Error("mine2 missing from batch metrics")
	}
	if _, ok := body.Metrics["secret"]; ok {
		t.Error("another user's private app must not appear in batch metrics")
	}
	if _, ok := body.Metrics["ghost"]; ok {
		t.Error("an unknown slug must not appear in batch metrics")
	}
	if body.Metrics["mine1"].Status != "stopped" {
		t.Errorf("mine1 status = %q, want stopped (never deployed)", body.Metrics["mine1"].Status)
	}
}

// With no ?slugs=, the batch endpoint reports every app visible to the caller.
func TestBatchMetrics_NoSlugsReturnsAllVisible(t *testing.T) {
	srv, store := newTestServer(t)
	hash, _ := testHashPassword("pass")
	store.CreateUser(db.CreateUserParams{Username: "owner", PasswordHash: hash, Role: "developer"})
	owner, _ := store.GetUserByUsername("owner")
	store.CreateApp(db.CreateAppParams{Slug: "a", Name: "A", OwnerID: owner.ID})
	store.CreateApp(db.CreateAppParams{Slug: "b", Name: "B", OwnerID: owner.ID})

	token, _ := auth.IssueJWT(owner.ID, "owner", "developer", "test-secret")
	req := authedRequest(t, "GET", "/api/apps/metrics", nil, token)
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
	var body struct {
		Metrics map[string]json.RawMessage `json:"metrics"`
	}
	json.NewDecoder(rec.Body).Decode(&body)
	if len(body.Metrics) != 2 {
		t.Fatalf("metrics count = %d, want 2 (all visible apps)", len(body.Metrics))
	}
}

// TestBatchMetrics_SurfacesLostReplicaAndAutoscale proves the batched replicas
// and latest autoscale event (fetched in bulk, not per card) flow through to
// each app's metrics: a lost replica shows "lost" + reason, and the autoscale
// status reflects the latest scale event. Guards the batch wiring into
// buildAppMetricsFrom, not just the status field.
func TestBatchMetrics_SurfacesLostReplicaAndAutoscale(t *testing.T) {
	srv, store, _ := newManagerTestServer(t)
	reg, err := worker.NewRegistry(store)
	if err != nil {
		t.Fatalf("new registry: %v", err)
	}
	srv.SetWorkerRegistry(reg)

	hash, _ := testHashPassword("pass")
	store.CreateUser(db.CreateUserParams{Username: "owner", PasswordHash: hash, Role: "developer"})
	owner, _ := store.GetUserByUsername("owner")
	store.CreateApp(db.CreateAppParams{Slug: "demo", Name: "Demo", OwnerID: owner.ID})
	app, _ := store.GetAppBySlug("demo")
	store.UpsertReplica(db.UpsertReplicaParams{AppID: app.ID, Index: 0, Status: db.ReplicaStatusLost, Tier: "remote"})
	store.LogAuditEvent(db.AuditEventParams{UserID: &owner.ID, Action: "autoscale_scale_up", ResourceType: "app", ResourceID: "demo", Detail: "1->2"})

	token, _ := auth.IssueJWT(owner.ID, "owner", "developer", "test-secret")
	req := authedRequest(t, "GET", "/api/apps/metrics?slugs=demo", nil, token)
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
	var body struct {
		Metrics map[string]struct {
			Replicas []struct {
				Index  int    `json:"index"`
				Status string `json:"status"`
				Reason string `json:"reason"`
			} `json:"replicas"`
			AutoscaleStatus *struct {
				LastAction string `json:"last_action"`
			} `json:"autoscale_status"`
		} `json:"metrics"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	demo, ok := body.Metrics["demo"]
	if !ok {
		t.Fatalf("demo missing from batch metrics: %s", rec.Body.String())
	}
	foundLost := false
	for _, r := range demo.Replicas {
		if r.Index == 0 {
			foundLost = true
			if r.Status != "lost" || r.Reason != "worker unavailable" {
				t.Errorf("batched replica: status=%q reason=%q, want lost/worker unavailable", r.Status, r.Reason)
			}
		}
	}
	if !foundLost {
		t.Errorf("batched lost replica missing: %s", rec.Body.String())
	}
	if demo.AutoscaleStatus == nil || demo.AutoscaleStatus.LastAction != "up" {
		t.Errorf("batched autoscale status = %+v, want last_action up", demo.AutoscaleStatus)
	}
}

// Unauthenticated callers are rejected.
func TestBatchMetrics_RequiresAuth(t *testing.T) {
	srv, _ := newTestServer(t)
	req := httptest.NewRequest("GET", "/api/apps/metrics", nil)
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401 for an unauthenticated batch request", rec.Code)
	}
}
