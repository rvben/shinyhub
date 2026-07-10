package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/rvben/shinyhub/internal/auth"
	"github.com/rvben/shinyhub/internal/config"
	"github.com/rvben/shinyhub/internal/db"
	"github.com/rvben/shinyhub/internal/dbtest"
)

// appDeploying gates the dashboard "deploying" badge. Unlike the proxy's
// MissStatus (row-only for non-terminal statuses, so clustered standbys keep
// serving the deploying wait page), the badge renders even while a pool is
// live and healthy, so a false positive would pin "Deploying" on a running
// app after a PromoteDeployment failure. It therefore requires BOTH the
// pending row and the locally held deploy lock, for every status.
func TestAppDeploying_RequiresPendingRowAndHeldLock(t *testing.T) {
	srv := New(&config.Config{
		Auth:    config.AuthConfig{Secret: "test-secret"},
		Storage: config.StorageConfig{AppsDir: t.TempDir()},
	}, dbtest.New(t), nil, nil)

	app := &db.App{Slug: "demo", Status: "running", LastDeploymentStatus: db.DeploymentPending}

	if srv.appDeploying(app) {
		t.Fatal("stale pending row (lock free) must not report deploying")
	}
	release := srv.acquireDeployLock("demo")
	if !srv.appDeploying(app) {
		t.Fatal("pending row + held lock must report deploying")
	}
	noPending := &db.App{Slug: "demo", Status: "running", LastDeploymentStatus: db.DeploymentSucceeded}
	if srv.appDeploying(noPending) {
		t.Fatal("held lock without a pending row (restart/stop) must not report deploying")
	}
	release()
	if srv.appDeploying(app) {
		t.Fatal("released lock must clear deploying")
	}
}

// The dashboard consumes the deploying flag from two payloads: the apps list
// (initial card render) and the batch metrics poll (live badge updates). Both
// must carry it while a deploy executes and drop/clear it afterwards.
func TestDeployingFlag_InListAndBatchMetricsPayloads(t *testing.T) {
	store := dbtest.New(t)
	srv := New(&config.Config{
		Auth:    config.AuthConfig{Secret: "test-secret"},
		Storage: config.StorageConfig{AppsDir: t.TempDir()},
	}, store, nil, nil)

	if err := store.CreateUser(db.CreateUserParams{Username: "admin", PasswordHash: "hash", Role: "admin"}); err != nil {
		t.Fatal(err)
	}
	u, err := store.GetUserByUsername("admin")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.CreateApp(db.CreateAppParams{Slug: "demo", Name: "Demo", OwnerID: u.ID}); err != nil {
		t.Fatal(err)
	}
	app, err := store.GetAppBySlug("demo")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.BeginDeployment(app.ID, "v1", t.TempDir()); err != nil {
		t.Fatal(err)
	}
	token, err := auth.IssueJWT(u.ID, u.Username, u.Role, "test-secret")
	if err != nil {
		t.Fatal(err)
	}

	listDeploying := func() bool {
		req := httptest.NewRequest("GET", "/api/apps", nil)
		req.Header.Set("Authorization", "Bearer "+token)
		rec := httptest.NewRecorder()
		srv.Router().ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("list apps: status %d: %s", rec.Code, rec.Body.String())
		}
		var body struct {
			Items []struct {
				Slug      string `json:"slug"`
				Deploying bool   `json:"deploying"`
			} `json:"items"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatalf("list apps: %v", err)
		}
		for _, it := range body.Items {
			if it.Slug == "demo" {
				return it.Deploying
			}
		}
		t.Fatal("demo not in list payload")
		return false
	}
	metricsDeploying := func() bool {
		req := httptest.NewRequest("GET", "/api/apps/metrics?slugs=demo", nil)
		req.Header.Set("Authorization", "Bearer "+token)
		rec := httptest.NewRecorder()
		srv.Router().ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("batch metrics: status %d: %s", rec.Code, rec.Body.String())
		}
		var body struct {
			Metrics map[string]struct {
				Deploying bool `json:"deploying"`
			} `json:"metrics"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatalf("batch metrics: %v", err)
		}
		m, ok := body.Metrics["demo"]
		if !ok {
			t.Fatal("demo not in metrics payload")
		}
		return m.Deploying
	}

	// Pending row but no lock held: a stale row must not light the badge.
	if listDeploying() {
		t.Error("list payload reports deploying for a stale pending row")
	}
	if metricsDeploying() {
		t.Error("metrics payload reports deploying for a stale pending row")
	}

	// Lock held (a deploy is executing): both payloads light the badge.
	release := srv.acquireDeployLock("demo")
	if !listDeploying() {
		t.Error("list payload missing deploying=true during an executing deploy")
	}
	if !metricsDeploying() {
		t.Error("metrics payload missing deploying=true during an executing deploy")
	}
	release()

	// Lock released: the badge clears again.
	if listDeploying() {
		t.Error("list payload still deploying after the lock was released")
	}
	if metricsDeploying() {
		t.Error("metrics payload still deploying after the lock was released")
	}
}
