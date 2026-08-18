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
	"github.com/rvben/shinyhub/internal/proxy"
	"github.com/rvben/shinyhub/internal/worker"
)

// newElasticStatusServer builds a server with a live proxy, an admin token,
// and one desired-running grouped app whose pool is registered but empty:
// exactly what a grouped app looks like right after a successful deploy.
func newElasticStatusServer(t *testing.T) (*api.Server, *db.Store, *proxy.Proxy, string) {
	t.Helper()
	store := dbtest.New(t)
	cfg := &config.Config{
		Auth:    config.AuthConfig{Secret: "test-secret"},
		Storage: config.StorageConfig{AppsDir: t.TempDir(), AppDataDir: t.TempDir()},
	}
	prx := proxy.New()
	srv := api.New(cfg, store, nil, prx)
	reg, err := worker.NewRegistry(store)
	if err != nil {
		t.Fatalf("registry: %v", err)
	}
	srv.SetWorkerRegistry(reg)

	hash, _ := testHashPassword("pass")
	store.CreateUser(db.CreateUserParams{Username: "admin", PasswordHash: hash, Role: "admin"})
	admin, _ := store.GetUserByUsername("admin")
	tok, _ := auth.IssueJWT(admin.ID, "admin", "admin", "test-secret")

	if _, err := store.CreateApp(db.CreateAppParams{Slug: "grouped", Name: "Grouped", OwnerID: admin.ID}); err != nil {
		t.Fatal(err)
	}
	if err := store.UpdateAppStatus(db.UpdateAppStatusParams{Slug: "grouped", Status: "running"}); err != nil {
		t.Fatal(err)
	}
	prx.SetPoolMode("grouped", config.IsolationGrouped, 4, 3)
	return srv, store, prx, tok
}

type appStatusView struct {
	Status         string `json:"status"`
	DesiredStatus  string `json:"desired_status"`
	WorkersRunning int    `json:"workers_running"`
}

func getAppStatus(t *testing.T, srv *api.Server, tok, slug string) appStatusView {
	t.Helper()
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, authedRequest(t, "GET", "/api/apps/"+slug, nil, tok))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/apps/%s: %d: %s", slug, rec.Code, rec.Body.String())
	}
	var env struct {
		App appStatusView `json:"app"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return env.App
}

// A deployed grouped app with no worker yet is idle (its first request boots
// a worker), and desired_status keeps the stored intent. This is the shape
// that the CLI readiness gate must accept as healthy.
func TestGetApp_ElasticEmptyPoolIsIdle(t *testing.T) {
	srv, _, _, tok := newElasticStatusServer(t)
	got := getAppStatus(t, srv, tok, "grouped")
	if got.Status != "idle" || got.DesiredStatus != "running" || got.WorkersRunning != 0 {
		t.Fatalf("got %+v, want status=idle desired_status=running workers_running=0", got)
	}
}

// A frozen warm spare (worker.warm_spares with snapshot freeze) is registered
// suspended: ready to resume on the first request, but not running. The app is
// idle, not perpetually starting, and workers_running stays strict.
func TestGetApp_ElasticFrozenSpareIsIdle(t *testing.T) {
	srv, _, prx, tok := newElasticStatusServer(t)
	if err := prx.RegisterSuspendedElasticWorker("grouped", 0, "http://127.0.0.1:1", nil, 1); err != nil {
		t.Fatalf("RegisterSuspendedElasticWorker: %v", err)
	}
	got := getAppStatus(t, srv, tok, "grouped")
	if got.Status != "idle" || got.WorkersRunning != 0 {
		t.Fatalf("got %+v, want status=idle workers_running=0", got)
	}
}

// The positive control for the two tests above: a live worker makes the app
// running with workers_running counted, so idle is not a constant.
func TestGetApp_ElasticLiveWorkerIsRunning(t *testing.T) {
	srv, _, prx, tok := newElasticStatusServer(t)
	if err := prx.RegisterElasticWorker("grouped", 0, "http://127.0.0.1:1", nil, 1); err != nil {
		t.Fatalf("RegisterElasticWorker: %v", err)
	}
	got := getAppStatus(t, srv, tok, "grouped")
	if got.Status != "running" || got.WorkersRunning != 1 {
		t.Fatalf("got %+v, want status=running workers_running=1", got)
	}
}

// Fleet health buckets idle elastic apps explicitly. Before the bucket existed
// an idle app fell through every case, so a fleet of healthy grouped apps
// reported "N apps, 0 running" with nothing accounting for the difference.
func TestFleetHealth_CountsIdleElasticApps(t *testing.T) {
	srv, _, _, tok := newElasticStatusServer(t)
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, authedRequest(t, "GET", "/api/fleet/health", nil, tok))
	if rec.Code != http.StatusOK {
		t.Fatalf("fleet health: %d: %s", rec.Code, rec.Body.String())
	}
	var got struct {
		Apps struct {
			Total   int `json:"total"`
			Running int `json:"running"`
			Idle    int `json:"idle"`
		} `json:"apps"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.Apps.Total != 1 || got.Apps.Running != 0 || got.Apps.Idle != 1 {
		t.Fatalf("apps = %+v, want total=1 running=0 idle=1", got.Apps)
	}
}
