package api_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/rvben/shinyhub/internal/api"
	"github.com/rvben/shinyhub/internal/auth"
	"github.com/rvben/shinyhub/internal/config"
	"github.com/rvben/shinyhub/internal/db"
	"github.com/rvben/shinyhub/internal/dbtest"
)

// newWorkerPatchServer builds a minimal test server for worker-isolation PATCH tests.
func newWorkerPatchServer(t *testing.T) (*api.Server, *db.Store) {
	t.Helper()
	store := dbtest.New(t)
	cfg := &config.Config{
		Auth:    config.AuthConfig{Secret: "test-secret"},
		Storage: config.StorageConfig{AppsDir: t.TempDir(), AppDataDir: t.TempDir()},
	}
	return api.New(cfg, store, nil, nil), store
}

// newWorkerBudgetServer builds a server with a non-zero host budget so the
// max_workers capacity check is exercised.
func newWorkerBudgetServer(t *testing.T, hostBudgetMB int) (*api.Server, *db.Store) {
	t.Helper()
	store := dbtest.New(t)
	cfg := &config.Config{
		Auth:    config.AuthConfig{Secret: "test-secret"},
		Storage: config.StorageConfig{AppsDir: t.TempDir(), AppDataDir: t.TempDir()},
		Server:  config.ServerConfig{HostBudgetMB: hostBudgetMB},
	}
	return api.New(cfg, store, nil, nil), store
}

// seedWorkerApp creates a single admin user and app, returning the app slug and
// a valid JWT for that user.
func seedWorkerApp(t *testing.T, store *db.Store) (slug, token string) {
	t.Helper()
	hash, _ := auth.HashPassword("pass")
	store.CreateUser(db.CreateUserParams{Username: "wuser", PasswordHash: hash, Role: "admin"})
	u, _ := store.GetUserByUsername("wuser")
	tok, _ := auth.IssueJWT(u.ID, "wuser", "admin", "test-secret")
	store.CreateApp(db.CreateAppParams{Slug: "wapp", Name: "Worker App", OwnerID: u.ID})
	return "wapp", tok
}

// patchWorkerApp sends a PATCH to /api/apps/wapp with the given JSON body.
func patchWorkerApp(t *testing.T, srv *api.Server, token string, body []byte) *httptest.ResponseRecorder {
	t.Helper()
	req := authedRequest(t, "PATCH", "/api/apps/wapp", body, token)
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)
	return rec
}

// TestPatchApp_WorkerIsolationPerSessionSucceeds verifies that a PATCH with a
// valid per_session isolation and max_workers is accepted and persisted.
func TestPatchApp_WorkerIsolationPerSessionSucceeds(t *testing.T) {
	srv, store := newWorkerPatchServer(t)
	_, token := seedWorkerApp(t, store)

	body := []byte(`{"worker_isolation":"per_session","worker_max_workers":2}`)
	rec := patchWorkerApp(t, srv, token, body)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	app, _ := store.GetAppBySlug("wapp")
	if app.WorkerIsolation != "per_session" {
		t.Errorf("WorkerIsolation = %q, want per_session", app.WorkerIsolation)
	}
	if app.WorkerMaxWorkers != 2 {
		t.Errorf("WorkerMaxWorkers = %d, want 2", app.WorkerMaxWorkers)
	}
}

// TestPatchApp_GroupedIsolationRequiresGroupedSize verifies that a PATCH with
// grouped isolation but no grouped_size is rejected with a 400 that mentions
// "grouped_size".
func TestPatchApp_GroupedIsolationRequiresGroupedSize(t *testing.T) {
	srv, store := newWorkerPatchServer(t)
	_, token := seedWorkerApp(t, store)

	// grouped without worker_grouped_size: the current (default) grouped_size is 0,
	// which is < 1, so ValidateWorkerSettings must reject it.
	body := []byte(`{"worker_isolation":"grouped","worker_max_workers":2}`)
	rec := patchWorkerApp(t, srv, token, body)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "grouped_size") {
		t.Errorf("expected error to mention grouped_size, got: %s", rec.Body.String())
	}
}

// TestPatchApp_WorkerMaxWorkersExceedsHostBudget verifies that a PATCH is
// rejected when the requested max_workers would exceed the configured host
// budget given the app's memory limit.
//
// With hostBudgetMB=400, memoryLimitMB=256, baseOverhead=150:
// worst = 1 * (256 + 150) = 406 > 400 -> 400 Bad Request.
func TestPatchApp_WorkerMaxWorkersExceedsHostBudget(t *testing.T) {
	srv, store := newWorkerBudgetServer(t, 400)
	_, token := seedWorkerApp(t, store)

	// Give the app an explicit memory limit so effectiveMemMB is non-zero.
	memLimit := 256
	store.PatchAppSettings(db.PatchAppSettingsParams{ //nolint:errcheck
		Slug:             "wapp",
		SetMemoryLimitMB: true,
		MemoryLimitMB:    &memLimit,
	})

	// 1 worker * (256 + 150) = 406 MiB > 400 MiB budget.
	body := []byte(`{"worker_isolation":"per_session","worker_max_workers":1}`)
	rec := patchWorkerApp(t, srv, token, body)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for budget exceeded, got %d: %s", rec.Code, rec.Body.String())
	}
}

// TestPatchApp_WorkerIsolationRejectedWhenClustered verifies that non-multiplex
// isolation modes are rejected on a clustered (Postgres) server, since elastic
// per-session workers require single-node operation.
func TestPatchApp_WorkerIsolationRejectedWhenClustered(t *testing.T) {
	srv, store := newWorkerPatchServer(t)
	srv.SetCluster("test-instance") // marks s.clustered = true
	_, token := seedWorkerApp(t, store)

	body := []byte(`{"worker_isolation":"per_session","worker_max_workers":2}`)
	rec := patchWorkerApp(t, srv, token, body)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for non-multiplex on clustered, got %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "single-node") {
		t.Errorf("expected error to mention single-node, got: %s", rec.Body.String())
	}
}
