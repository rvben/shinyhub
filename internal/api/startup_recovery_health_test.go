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
	"github.com/rvben/shinyhub/internal/lifecycle"
	"github.com/rvben/shinyhub/internal/process"
	"github.com/rvben/shinyhub/internal/proxy"
)

// A full control-plane restart can prove that a formerly running native
// process is gone. Recovery deliberately makes the app wakeable rather than
// terminal, so its replica projection must agree: otherwise the API overlays
// the stale crashed row onto the hibernated app and fleet --verify-health
// rejects it immediately instead of treating it as parked.
func TestStartupRecoveryDeadReplicaIsExposedAsHibernated(t *testing.T) {
	appsDir := t.TempDir()
	store := dbtest.New(t)
	cfg := &config.Config{
		Auth:    config.AuthConfig{Secret: "test-secret"},
		Storage: config.StorageConfig{AppsDir: appsDir},
	}
	mgr := process.NewManager(appsDir, process.NewNativeRuntime())
	prx := proxy.New()
	srv := api.New(cfg, store, mgr, prx)

	hash, _ := testHashPassword("pass")
	if err := store.CreateUser(db.CreateUserParams{Username: "owner", PasswordHash: hash, Role: "developer"}); err != nil {
		t.Fatal(err)
	}
	owner, err := store.GetUserByUsername("owner")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateApp(db.CreateAppParams{Slug: "recovered", Name: "Recovered", OwnerID: owner.ID}); err != nil {
		t.Fatal(err)
	}
	app, err := store.GetAppBySlug("recovered")
	if err != nil {
		t.Fatal(err)
	}
	deadPID, port := 99999999, 20001
	if err := store.UpsertReplica(db.UpsertReplicaParams{
		AppID: app.ID, Index: 0, PID: &deadPID, Port: &port,
		Status: db.ReplicaStatusRunning, DesiredState: "running",
		Provider: "native", Tier: "default", EndpointURL: "http://127.0.0.1:20001",
		WorkerID: "old-worker", AppVersion: "v1",
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.UpdateAppStatus(db.UpdateAppStatusParams{Slug: app.Slug, Status: "running"}); err != nil {
		t.Fatal(err)
	}

	lifecycle.RecoverProcesses(store, mgr, prx, 0, false, "")

	token, err := auth.IssueJWT(owner.ID, owner.Username, owner.Role, "test-secret")
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, authedRequest(t, http.MethodGet, "/api/apps", nil, token))
	if rec.Code != http.StatusOK {
		t.Fatalf("list apps: %d: %s", rec.Code, rec.Body.String())
	}
	var got struct {
		Items []db.App `json:"items"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	if len(got.Items) != 1 {
		t.Fatalf("apps = %d, want 1", len(got.Items))
	}
	if got.Items[0].Status != "hibernated" || got.Items[0].DesiredStatus != "hibernated" {
		t.Fatalf("status/desired_status = %q/%q, want hibernated/hibernated", got.Items[0].Status, got.Items[0].DesiredStatus)
	}
	replicas, err := store.ListReplicas(app.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(replicas) != 1 {
		t.Fatalf("replicas = %d, want 1", len(replicas))
	}
	rep := replicas[0]
	if rep.Status != "stopped" || rep.DesiredState != "stopped" || rep.PID != nil || rep.Port != nil {
		t.Errorf("recovered replica = status %q, desired %q, pid %v, port %v; want stopped/stopped with no runtime identity",
			rep.Status, rep.DesiredState, rep.PID, rep.Port)
	}
	if rep.EndpointURL != "" || rep.WorkerID != "" {
		t.Errorf("ephemeral routing identity survived recovery: endpoint=%q worker=%q", rep.EndpointURL, rep.WorkerID)
	}
	if rep.Provider != "native" || rep.Tier != "default" || rep.AppVersion != "v1" {
		t.Errorf("durable placement provenance was lost: provider=%q tier=%q version=%q",
			rep.Provider, rep.Tier, rep.AppVersion)
	}
	if rep.LastExit == nil || rep.LastExit.Reason != "process not alive" || rep.LastExit.CrashCount != 1 {
		t.Errorf("last_exit = %+v, want preserved process-not-alive diagnostic", rep.LastExit)
	}
}
