package api_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/rvben/shinyhub/internal/api"
	"github.com/rvben/shinyhub/internal/auth"
	"github.com/rvben/shinyhub/internal/config"
	"github.com/rvben/shinyhub/internal/db"
	"github.com/rvben/shinyhub/internal/dbtest"
	"github.com/rvben/shinyhub/internal/worker"
)

type fleetHealthEnvelope struct {
	ServerVersion string `json:"server_version"`
	Apps          struct {
		Total    int `json:"total"`
		Degraded int `json:"degraded"`
	} `json:"apps"`
	Replicas struct {
		Running int `json:"running"`
		Lost    int `json:"lost"`
	} `json:"replicas"`
	Workers struct {
		Total   int `json:"total"`
		Up      int `json:"up"`
		Down    int `json:"down"`
		Joining int `json:"joining"`
		Revoked int `json:"revoked"`
	} `json:"workers"`
	Tiers []struct {
		Tier            string `json:"tier"`
		Runtime         string `json:"runtime"`
		ReplicasRunning int    `json:"replicas_running"`
		ReplicasLost    int    `json:"replicas_lost"`
	} `json:"tiers"`
	DegradedApps []struct {
		Slug   string `json:"slug"`
		Tier   string `json:"tier"`
		Lost   int    `json:"lost"`
		Reason string `json:"reason"`
	} `json:"degraded_apps"`
}

func newFleetHealthServer(t *testing.T) (*api.Server, *db.Store) {
	t.Helper()
	store := dbtest.New(t)
	cfg := &config.Config{
		Auth:    config.AuthConfig{Secret: "test-secret"},
		Storage: config.StorageConfig{AppsDir: t.TempDir(), AppDataDir: t.TempDir()},
		Runtime: config.RuntimeConfig{Tiers: []config.TierConfig{
			{Name: "local", Runtime: "native"},
			{Name: "remote", Runtime: "remote_docker"},
		}},
	}
	srv := api.New(cfg, store, nil, nil)
	srv.SetVersion("9.9.9")
	reg, err := worker.NewRegistry(store)
	if err != nil {
		t.Fatalf("registry: %v", err)
	}
	srv.SetWorkerRegistry(reg)
	return srv, store
}

// GET /api/fleet/health aggregates app/replica/worker health across backends.
func TestFleetHealth_AggregatesAcrossBackends(t *testing.T) {
	srv, store := newFleetHealthServer(t)

	hash, _ := auth.HashPassword("pass")
	store.CreateUser(db.CreateUserParams{Username: "admin", PasswordHash: hash, Role: "admin"})
	admin, _ := store.GetUserByUsername("admin")
	adminTok, _ := auth.IssueJWT(admin.ID, "admin", "admin", "test-secret")

	// ops: healthy, one running replica on the local/native tier.
	store.CreateApp(db.CreateAppParams{Slug: "ops", Name: "Ops", OwnerID: admin.ID})
	ops, _ := store.GetAppBySlug("ops")
	store.UpsertReplica(db.UpsertReplicaParams{AppID: ops.ID, Index: 0, Status: db.ReplicaStatusRunning, Tier: "local", Provider: "native"})
	// dash: degraded, one lost replica on the remote tier with no healthy worker.
	store.CreateApp(db.CreateAppParams{Slug: "dash", Name: "Dash", OwnerID: admin.ID})
	dash, _ := store.GetAppBySlug("dash")
	store.UpsertReplica(db.UpsertReplicaParams{AppID: dash.ID, Index: 0, Status: db.ReplicaStatusLost, Tier: "remote", Provider: "remote_docker"})

	req := authedRequest(t, "GET", "/api/fleet/health", nil, adminTok)
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var got fleetHealthEnvelope
	if err := json.NewDecoder(rec.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.ServerVersion != "9.9.9" {
		t.Errorf("server_version = %q, want 9.9.9", got.ServerVersion)
	}
	if got.Apps.Total != 2 {
		t.Errorf("apps.total = %d, want 2", got.Apps.Total)
	}
	if got.Apps.Degraded != 1 {
		t.Errorf("apps.degraded = %d, want 1", got.Apps.Degraded)
	}
	if got.Replicas.Running != 1 || got.Replicas.Lost != 1 {
		t.Errorf("replicas = %+v, want running=1 lost=1", got.Replicas)
	}
	byTier := map[string]struct{ run, lost int }{}
	for _, tr := range got.Tiers {
		byTier[tr.Tier] = struct{ run, lost int }{tr.ReplicasRunning, tr.ReplicasLost}
	}
	if byTier["local"].run != 1 || byTier["local"].lost != 0 {
		t.Errorf("tier local = %+v, want run=1 lost=0", byTier["local"])
	}
	if byTier["remote"].run != 0 || byTier["remote"].lost != 1 {
		t.Errorf("tier remote = %+v, want run=0 lost=1", byTier["remote"])
	}
	if len(got.DegradedApps) != 1 {
		t.Fatalf("degraded_apps len = %d, want 1: %+v", len(got.DegradedApps), got.DegradedApps)
	}
	d := got.DegradedApps[0]
	if d.Slug != "dash" || d.Tier != "remote" || d.Lost != 1 {
		t.Errorf("degraded_apps[0] = %+v, want dash/remote/1", d)
	}
	if d.Reason != "worker unavailable" {
		t.Errorf("degraded reason = %q, want 'worker unavailable'", d.Reason)
	}
}

// TestFleetHealth_JoiningWorkerNotCountedDown asserts a transitional "joining"
// worker (registered, not yet promoted by its first heartbeat) is reported in
// its own bucket, never as "down". Counting it down would make a fleet look
// degraded - workers.down > 0 drives the warning banner - for the moment between
// a worker joining and its first heartbeat.
func TestFleetHealth_JoiningWorkerNotCountedDown(t *testing.T) {
	srv, store := newFleetHealthServer(t)

	hash, _ := auth.HashPassword("pass")
	store.CreateUser(db.CreateUserParams{Username: "admin", PasswordHash: hash, Role: "admin"})
	admin, _ := store.GetUserByUsername("admin")
	adminTok, _ := auth.IssueJWT(admin.ID, "admin", "admin", "test-secret")

	// One worker that completed its handshake (up) and one still joining.
	if err := store.UpsertWorker(db.Worker{NodeID: "up-node", AdvertiseAddr: "a:8443", Tier: "remote", Status: "up"}); err != nil {
		t.Fatal(err)
	}
	if err := store.UpsertWorker(db.Worker{NodeID: "joining-node", AdvertiseAddr: "b:8443", Tier: "remote", Status: "joining"}); err != nil {
		t.Fatal(err)
	}

	req := authedRequest(t, "GET", "/api/fleet/health", nil, adminTok)
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var got fleetHealthEnvelope
	if err := json.NewDecoder(rec.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.Workers.Total != 2 {
		t.Errorf("workers.total = %d, want 2", got.Workers.Total)
	}
	if got.Workers.Up != 1 {
		t.Errorf("workers.up = %d, want 1", got.Workers.Up)
	}
	if got.Workers.Joining != 1 {
		t.Errorf("workers.joining = %d, want 1", got.Workers.Joining)
	}
	if got.Workers.Down != 0 {
		t.Errorf("workers.down = %d, want 0 (a joining worker must not count as down)", got.Workers.Down)
	}
}

// The endpoint is admin-only.
func TestFleetHealth_AdminOnly(t *testing.T) {
	srv, store := newFleetHealthServer(t)
	hash, _ := auth.HashPassword("pass")
	store.CreateUser(db.CreateUserParams{Username: "dev", PasswordHash: hash, Role: "developer"})
	dev, _ := store.GetUserByUsername("dev")
	devTok, _ := auth.IssueJWT(dev.ID, "dev", "developer", "test-secret")

	req := authedRequest(t, "GET", "/api/fleet/health", nil, devTok)
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("non-admin status = %d, want 403", rec.Code)
	}
}
