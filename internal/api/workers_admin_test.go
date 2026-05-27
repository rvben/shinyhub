package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/rvben/shinyhub/internal/auth"
	"github.com/rvben/shinyhub/internal/config"
	"github.com/rvben/shinyhub/internal/db"
	"github.com/rvben/shinyhub/internal/proxy"
	"github.com/rvben/shinyhub/internal/worker"
)

func newWorkerAdminServer(t *testing.T) (*Server, *worker.Registry, *db.Store) {
	t.Helper()
	store := newTestStore(t)
	cfg := &config.Config{
		Auth:    config.AuthConfig{Secret: "test-secret"},
		Storage: config.StorageConfig{AppsDir: t.TempDir()},
	}
	srv := New(cfg, store, nil, nil)
	reg, err := worker.NewRegistry(store)
	if err != nil {
		t.Fatalf("registry: %v", err)
	}
	srv.SetWorkerRegistry(reg)
	return srv, reg, store
}

// ctxUser creates a real user row (so audit-event foreign keys resolve) and
// returns a matching request context user.
func ctxUser(t *testing.T, store *db.Store, username, role string) *auth.ContextUser {
	t.Helper()
	if err := store.CreateUser(db.CreateUserParams{Username: username, PasswordHash: "h", Role: role}); err != nil {
		t.Fatalf("create user %q: %v", username, err)
	}
	u, err := store.GetUserByUsername(username)
	if err != nil {
		t.Fatalf("get user %q: %v", username, err)
	}
	return &auth.ContextUser{ID: u.ID, Username: u.Username, Role: u.Role}
}

// TestHandleListWorkers asserts admins can list the fleet (including revoked
// nodes with their revocation flag) and non-admins are forbidden.
func TestHandleListWorkers(t *testing.T) {
	srv, reg, store := newWorkerAdminServer(t)
	adminUser := ctxUser(t, store, "ops", "admin")
	devUser := ctxUser(t, store, "dev", "developer")
	a, _ := reg.Register(worker.RegisterParams{Tier: "burst", AdvertiseAddr: "10.0.0.5:8443"})
	reg.Register(worker.RegisterParams{Tier: "base", AdvertiseAddr: "10.0.0.6:8443"})
	if err := reg.Revoke(a.NodeID); err != nil {
		t.Fatalf("revoke: %v", err)
	}

	// Non-admin: forbidden.
	req := httptest.NewRequest(http.MethodGet, "/api/workers", http.NoBody)
	req = req.WithContext(auth.WithUser(req.Context(), devUser))
	w := httptest.NewRecorder()
	srv.handleListWorkers(w, req)
	if w.Code != http.StatusForbidden {
		t.Fatalf("non-admin list status = %d, want 403", w.Code)
	}

	// Admin: lists both workers, with the revoked flag set on the revoked one.
	req = httptest.NewRequest(http.MethodGet, "/api/workers", http.NoBody)
	req = req.WithContext(auth.WithUser(req.Context(), adminUser))
	w = httptest.NewRecorder()
	srv.handleListWorkers(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("admin list status = %d, want 200: %s", w.Code, w.Body.String())
	}
	var got []struct {
		NodeID  string `json:"node_id"`
		Status  string `json:"status"`
		Revoked bool   `json:"revoked"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v (%s)", err, w.Body.String())
	}
	if len(got) != 2 {
		t.Fatalf("listed %d workers, want 2", len(got))
	}
	for _, g := range got {
		if g.NodeID == a.NodeID && (!g.Revoked || g.Status != "down") {
			t.Errorf("revoked worker view wrong: %+v", g)
		}
	}
}

// TestHandleListWorkers_ReportsFreshHeartbeat asserts the fleet view reports the
// authoritative last_heartbeat after a worker heartbeats. The registry's
// in-memory row is not updated with the heartbeat timestamp (the store writes it
// server-side), so listing from the stale in-memory snapshot would show an empty
// last_heartbeat until a control-plane restart reloaded the table.
func TestHandleListWorkers_ReportsFreshHeartbeat(t *testing.T) {
	srv, reg, store := newWorkerAdminServer(t)
	adminUser := ctxUser(t, store, "ops", "admin")

	node, err := reg.Register(worker.RegisterParams{Tier: "burst", AdvertiseAddr: "10.0.0.5:8443"})
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	if err := reg.Heartbeat(node.NodeID, "fp-1"); err != nil {
		t.Fatalf("heartbeat: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/workers", http.NoBody)
	req = req.WithContext(auth.WithUser(req.Context(), adminUser))
	w := httptest.NewRecorder()
	srv.handleListWorkers(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("list status = %d, want 200: %s", w.Code, w.Body.String())
	}
	var got []struct {
		NodeID        string `json:"node_id"`
		LastHeartbeat string `json:"last_heartbeat"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v (%s)", err, w.Body.String())
	}
	if len(got) != 1 {
		t.Fatalf("listed %d workers, want 1", len(got))
	}
	if got[0].LastHeartbeat == "" {
		t.Fatalf("fleet view reported empty last_heartbeat after a heartbeat; want the stored timestamp")
	}
}

// TestHandleRevokeWorker asserts an admin can revoke a worker (excluding it from
// routing and recording an audit event), non-admins are forbidden, and an
// unknown node yields 404.
func TestHandleRevokeWorker(t *testing.T) {
	srv, reg, store := newWorkerAdminServer(t)
	adminUser := ctxUser(t, store, "ops", "admin")
	devUser := ctxUser(t, store, "dev", "developer")
	node, _ := reg.Register(worker.RegisterParams{Tier: "burst", AdvertiseAddr: "10.0.0.5:8443"})

	// Non-admin: forbidden, and the worker stays routable.
	req := httptest.NewRequest(http.MethodPost, "/api/workers/"+node.NodeID+"/revoke", http.NoBody)
	req = withURLParam(req, "node_id", node.NodeID)
	req = req.WithContext(auth.WithUser(req.Context(), devUser))
	w := httptest.NewRecorder()
	srv.handleRevokeWorker(w, req)
	if w.Code != http.StatusForbidden {
		t.Fatalf("non-admin revoke status = %d, want 403", w.Code)
	}
	if _, ok := reg.WorkerForTier("burst"); !ok {
		t.Fatal("worker wrongly revoked by a non-admin request")
	}

	// Admin: revokes the worker.
	req = httptest.NewRequest(http.MethodPost, "/api/workers/"+node.NodeID+"/revoke", http.NoBody)
	req = withURLParam(req, "node_id", node.NodeID)
	req = req.WithContext(auth.WithUser(req.Context(), adminUser))
	w = httptest.NewRecorder()
	srv.handleRevokeWorker(w, req)
	if w.Code != http.StatusNoContent {
		t.Fatalf("admin revoke status = %d, want 204: %s", w.Code, w.Body.String())
	}
	if _, ok := reg.WorkerForTier("burst"); ok {
		t.Fatal("worker still routable after revoke")
	}
	if wk, _ := reg.Worker(node.NodeID); !wk.Revoked() {
		t.Fatal("worker not revoked in registry after handler")
	}

	// Audit event recorded.
	events, err := store.ListAuditEvents("revoke_worker", 10, 0)
	if err != nil {
		t.Fatalf("list audit: %v", err)
	}
	found := false
	for _, e := range events {
		if e.Action == "revoke_worker" && e.ResourceID == node.NodeID {
			found = true
		}
	}
	if !found {
		t.Errorf("no revoke_worker audit event recorded; got %+v", events)
	}

	// Unknown node: 404.
	req = httptest.NewRequest(http.MethodPost, "/api/workers/ghost/revoke", http.NoBody)
	req = withURLParam(req, "node_id", "ghost")
	req = req.WithContext(auth.WithUser(req.Context(), adminUser))
	w = httptest.NewRecorder()
	srv.handleRevokeWorker(w, req)
	if w.Code != http.StatusNotFound {
		t.Fatalf("unknown-node revoke status = %d, want 404", w.Code)
	}
}

// TestHandleRevokeWorkerEvictsReplicas asserts that revoking a worker that owns
// running replicas transitions those replicas to lost and removes them from
// proxy routing, so user traffic stops flowing to the revoked node immediately
// rather than waiting for the down-timeout sweep (which skips already-down
// workers).
func TestHandleRevokeWorkerEvictsReplicas(t *testing.T) {
	store := newTestStore(t)
	cfg := &config.Config{
		Auth:    config.AuthConfig{Secret: "test-secret"},
		Storage: config.StorageConfig{AppsDir: t.TempDir()},
	}
	prx := proxy.New()
	srv := New(cfg, store, nil, prx)
	reg, err := worker.NewRegistry(store)
	if err != nil {
		t.Fatalf("registry: %v", err)
	}
	srv.SetWorkerRegistry(reg)

	adminUser := ctxUser(t, store, "ops", "admin")
	owner := ctxUser(t, store, "owner", "developer")
	if err := store.CreateApp(db.CreateAppParams{Slug: "demo", Name: "demo", OwnerID: owner.ID, Access: "private"}); err != nil {
		t.Fatalf("create app: %v", err)
	}
	app, err := store.GetAppBySlug("demo")
	if err != nil {
		t.Fatalf("get app: %v", err)
	}
	node, _ := reg.Register(worker.RegisterParams{Tier: "remote", AdvertiseAddr: "10.0.0.9:8443"})
	if err := store.UpsertReplica(db.UpsertReplicaParams{
		AppID: app.ID, Index: 0, Status: db.ReplicaStatusRunning,
		Provider: "remote_docker", Tier: "remote", WorkerID: node.NodeID,
		EndpointURL: "https://10.0.0.9:8443/v1/data/tok",
	}); err != nil {
		t.Fatalf("upsert replica: %v", err)
	}
	prx.SetPoolSize("demo", 1)
	if err := prx.RegisterReplica("demo", 0, "https://10.0.0.9:8443/v1/data/tok", nil); err != nil {
		t.Fatalf("register replica route: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/api/workers/"+node.NodeID+"/revoke", http.NoBody)
	req = withURLParam(req, "node_id", node.NodeID)
	req = req.WithContext(auth.WithUser(req.Context(), adminUser))
	w := httptest.NewRecorder()
	srv.handleRevokeWorker(w, req)
	if w.Code != http.StatusNoContent {
		t.Fatalf("revoke status = %d, want 204: %s", w.Code, w.Body.String())
	}

	reps, _ := store.ListReplicas(app.ID)
	if len(reps) != 1 || reps[0].Status != db.ReplicaStatusLost {
		t.Fatalf("replica not transitioned to lost after revoke: %+v", reps)
	}
	if got := prx.ReplicaTargetURL("demo", 0); got != "" {
		t.Fatalf("revoked worker's replica still routable: %q", got)
	}
}
