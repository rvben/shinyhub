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
// max_workers capacity check is exercised. The runtime floor is explicitly
// disabled so the static budget guard alone decides the unguarded warning
// (with the floor at its default the warning would never fire).
func newWorkerBudgetServer(t *testing.T, hostBudgetMB int) (*api.Server, *db.Store) {
	t.Helper()
	store := dbtest.New(t)
	floorOff := 0
	cfg := &config.Config{
		Auth:    config.AuthConfig{Secret: "test-secret"},
		Storage: config.StorageConfig{AppsDir: t.TempDir(), AppDataDir: t.TempDir()},
		Server:  config.ServerConfig{HostBudgetMB: hostBudgetMB, MinAvailableMemoryMB: &floorOff},
	}
	return api.New(cfg, store, nil, nil), store
}

// seedWorkerApp creates a single admin user and app, returning the app slug and
// a valid JWT for that user.
func seedWorkerApp(t *testing.T, store *db.Store) (slug, token string) {
	t.Helper()
	hash, _ := testHashPassword("pass")
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

// TestPatchApp_WorkerPartialPatchFallbackAllowsMaxWorkersUpdate proves the
// orString/orInt fallback path end-to-end: after establishing grouped isolation
// with a valid grouped_size, a subsequent PATCH that sets only max_workers must
// succeed because the validator sees the stored "grouped" + grouped_size via
// fallback and considers the dial consistent.
func TestPatchApp_WorkerPartialPatchFallbackAllowsMaxWorkersUpdate(t *testing.T) {
	srv, store := newWorkerPatchServer(t)
	_, token := seedWorkerApp(t, store)

	// Establish grouped isolation with grouped_size=4 and max_workers=10.
	rec := patchWorkerApp(t, srv, token, []byte(
		`{"worker_isolation":"grouped","worker_grouped_size":4,"worker_max_workers":10}`))
	if rec.Code != http.StatusOK {
		t.Fatalf("initial grouped PATCH: expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	// Partial PATCH: change only max_workers. The validator must use the stored
	// "grouped" + grouped_size=4 via fallback and accept the new value.
	rec = patchWorkerApp(t, srv, token, []byte(`{"worker_max_workers":999}`))
	if rec.Code != http.StatusOK {
		t.Fatalf("partial max_workers PATCH: expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	app, _ := store.GetAppBySlug("wapp")
	if app.WorkerIsolation != "grouped" {
		t.Errorf("WorkerIsolation = %q after partial PATCH, want grouped", app.WorkerIsolation)
	}
	if app.WorkerGroupedSize != 4 {
		t.Errorf("WorkerGroupedSize = %d after partial PATCH, want 4", app.WorkerGroupedSize)
	}
	if app.WorkerMaxWorkers != 999 {
		t.Errorf("WorkerMaxWorkers = %d after partial PATCH, want 999", app.WorkerMaxWorkers)
	}
}

// TestPatchApp_WorkerPartialPatchFallbackRejectsZeroGroupedSize verifies that
// a partial PATCH setting grouped_size=0 on an app already using grouped
// isolation is rejected: the validator sees the stored "grouped" isolation via
// fallback and fails the grouped_size >= 1 check.
func TestPatchApp_WorkerPartialPatchFallbackRejectsZeroGroupedSize(t *testing.T) {
	srv, store := newWorkerPatchServer(t)
	_, token := seedWorkerApp(t, store)

	// Establish grouped isolation with a valid grouped_size.
	rec := patchWorkerApp(t, srv, token, []byte(
		`{"worker_isolation":"grouped","worker_grouped_size":4,"worker_max_workers":10}`))
	if rec.Code != http.StatusOK {
		t.Fatalf("initial grouped PATCH: expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	// Partial PATCH: set grouped_size=0 only. The stored "grouped" isolation plus
	// the new size=0 must fail validation.
	rec = patchWorkerApp(t, srv, token, []byte(`{"worker_grouped_size":0}`))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("zero grouped_size PATCH: expected 400, got %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "grouped_size") {
		t.Errorf("expected error to mention grouped_size, got: %s", rec.Body.String())
	}
}

// newWorkerFloorServer builds a server whose runtime memory floor
// (min_available_memory_mb) is EXPLICITLY set: a positive value arms the
// runtime guard at that floor, 0 disables it (distinct from an unset config,
// which applies the safe default).
func newWorkerFloorServer(t *testing.T, minAvailableMB int) (*api.Server, *db.Store) {
	t.Helper()
	store := dbtest.New(t)
	cfg := &config.Config{
		Auth:    config.AuthConfig{Secret: "test-secret"},
		Storage: config.StorageConfig{AppsDir: t.TempDir(), AppDataDir: t.TempDir()},
		Server:  config.ServerConfig{MinAvailableMemoryMB: &minAvailableMB},
	}
	return api.New(cfg, store, nil, nil), store
}

// TestPatchApp_ElasticIsolationWarnsWithFloorExplicitlyDisabled verifies that
// switching an app to elastic isolation on a server whose runtime floor was
// EXPLICITLY disabled (min_available_memory_mb: 0) and that has no static
// budget guard succeeds but attaches the X-ShinyHub-Warning header, so the
// operator who opted out learns the host is unprotected.
func TestPatchApp_ElasticIsolationWarnsWithFloorExplicitlyDisabled(t *testing.T) {
	srv, store := newWorkerFloorServer(t, 0)
	_, token := seedWorkerApp(t, store)

	body := []byte(`{"worker_isolation":"per_session","worker_max_workers":2}`)
	rec := patchWorkerApp(t, srv, token, body)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	warn := rec.Header().Get("X-ShinyHub-Warning")
	if warn == "" {
		t.Fatal("expected X-ShinyHub-Warning when the floor is explicitly disabled and no static guard exists")
	}
	if !strings.Contains(warn, "memory guard") {
		t.Errorf("warning should name the missing memory guard, got %q", warn)
	}
}

// TestPatchApp_ElasticIsolationSilentWithDefaultFloor pins the default-on
// behavior: an UNSET min_available_memory_mb applies the built-in floor, so
// elastic isolation on an otherwise-unconfigured server is guarded and must
// not warn.
func TestPatchApp_ElasticIsolationSilentWithDefaultFloor(t *testing.T) {
	srv, store := newWorkerPatchServer(t)
	_, token := seedWorkerApp(t, store)

	body := []byte(`{"worker_isolation":"per_session","worker_max_workers":2}`)
	rec := patchWorkerApp(t, srv, token, body)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if warn := rec.Header().Get("X-ShinyHub-Warning"); warn != "" {
		t.Errorf("expected no warning with the default floor active, got %q", warn)
	}
}

// TestPatchApp_ElasticIsolationSilentWithRuntimeFloor verifies that the
// warning is suppressed when the runtime available-memory floor is configured.
func TestPatchApp_ElasticIsolationSilentWithRuntimeFloor(t *testing.T) {
	srv, store := newWorkerFloorServer(t, 1024)
	_, token := seedWorkerApp(t, store)

	body := []byte(`{"worker_isolation":"per_session","worker_max_workers":2}`)
	rec := patchWorkerApp(t, srv, token, body)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if warn := rec.Header().Get("X-ShinyHub-Warning"); warn != "" {
		t.Errorf("expected no warning with the runtime floor set, got %q", warn)
	}
}

// TestPatchApp_MultiplexNeverWarnsAboutMemoryGuard pins that the warning is
// scoped to elastic isolation: reverting to multiplex is always silent.
func TestPatchApp_MultiplexNeverWarnsAboutMemoryGuard(t *testing.T) {
	srv, store := newWorkerPatchServer(t)
	_, token := seedWorkerApp(t, store)

	body := []byte(`{"worker_isolation":"multiplex"}`)
	rec := patchWorkerApp(t, srv, token, body)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if warn := rec.Header().Get("X-ShinyHub-Warning"); warn != "" {
		t.Errorf("expected no warning for multiplex, got %q", warn)
	}
}

// TestPatchApp_WarningTracksMemoryLimitInSamePatch pins that the memory-guard
// warning is decided against the POST-patch memory limit when one request
// changes both: clearing the limit disarms the static budget guard (warn),
// while setting a limit on a budget-configured server arms it (silent).
func TestPatchApp_WarningTracksMemoryLimitInSamePatch(t *testing.T) {
	srv, store := newWorkerBudgetServer(t, 8192)
	_, token := seedWorkerApp(t, store)

	// Arm the static guard in the same request that turns on elastic
	// isolation: budget + limit are both active, so no warning.
	rec := patchWorkerApp(t, srv, token,
		[]byte(`{"memory_limit_mb":512,"worker_isolation":"per_session","worker_max_workers":2}`))
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if warn := rec.Header().Get("X-ShinyHub-Warning"); warn != "" {
		t.Errorf("limit set in the same patch arms the guard; expected no warning, got %q", warn)
	}

	// Clearing the limit in the same request disarms the guard: the warning
	// must reflect the post-patch state, not the stored 512.
	rec = patchWorkerApp(t, srv, token,
		[]byte(`{"memory_limit_mb":null,"worker_max_workers":3}`))
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if warn := rec.Header().Get("X-ShinyHub-Warning"); warn == "" {
		t.Error("clearing the memory limit disarms the static guard; expected a warning")
	}
}

// TestPatchApp_MemoryLimitChangeRevalidatesElasticApp pins that changing ONLY
// the memory limit on an app already in elastic isolation re-runs the worker
// budget math: a raise that busts the host budget is rejected, and clearing
// the limit (disarming the static guard) warns.
func TestPatchApp_MemoryLimitChangeRevalidatesElasticApp(t *testing.T) {
	srv, store := newWorkerBudgetServer(t, 2000)
	_, token := seedWorkerApp(t, store)

	// 2 workers x (512 + 150) = 1324 <= 2000: accepted, guard armed.
	rec := patchWorkerApp(t, srv, token,
		[]byte(`{"memory_limit_mb":512,"worker_isolation":"per_session","worker_max_workers":2}`))
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	// 2 workers x (1500 + 150) = 3300 > 2000: the raise alone must be rejected.
	rec = patchWorkerApp(t, srv, token, []byte(`{"memory_limit_mb":1500}`))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("limit raise busting the budget: expected 400, got %d: %s", rec.Code, rec.Body.String())
	}

	// Clearing the limit alone disarms the static guard: accepted with warning.
	rec = patchWorkerApp(t, srv, token, []byte(`{"memory_limit_mb":null}`))
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if warn := rec.Header().Get("X-ShinyHub-Warning"); warn == "" {
		t.Error("clearing the limit on an elastic app disarms the guard; expected a warning")
	}
}

// TestPatchApp_MemoryLimitChangeOnMultiplexStaysSilent pins that the
// revalidation is invisible for multiplex apps: memory limit changes neither
// fail worker validation nor warn.
func TestPatchApp_MemoryLimitChangeOnMultiplexStaysSilent(t *testing.T) {
	srv, store := newWorkerBudgetServer(t, 100)
	_, token := seedWorkerApp(t, store)

	// Even a limit far above the budget is fine on multiplex (the worker
	// budget math only applies to elastic isolation).
	rec := patchWorkerApp(t, srv, token, []byte(`{"memory_limit_mb":4096}`))
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if warn := rec.Header().Get("X-ShinyHub-Warning"); warn != "" {
		t.Errorf("expected no warning for multiplex, got %q", warn)
	}
}

// newWorkerFleetDefaultServer builds a server whose FLEET default isolation is
// elastic, with a host budget: apps with empty stored isolation inherit the
// elastic mode at runtime, so the budget math must treat them as elastic too.
// The runtime floor is explicitly disabled so the static guard alone decides
// the unguarded warning.
func newWorkerFleetDefaultServer(t *testing.T, isolation string, hostBudgetMB int) (*api.Server, *db.Store) {
	t.Helper()
	store := dbtest.New(t)
	floorOff := 0
	cfg := &config.Config{
		Auth:    config.AuthConfig{Secret: "test-secret"},
		Storage: config.StorageConfig{AppsDir: t.TempDir(), AppDataDir: t.TempDir()},
		Server:  config.ServerConfig{HostBudgetMB: hostBudgetMB, MinAvailableMemoryMB: &floorOff},
		Runtime: config.RuntimeConfig{DefaultWorkerIsolation: isolation},
	}
	return api.New(cfg, store, nil, nil), store
}

// TestPatchApp_InheritedElasticIsolationGuardsMemoryLimit pins that the
// memory-guard math resolves isolation through the fleet default: an app
// whose stored worker_isolation is empty but inherits per_session runs the
// budget check and unguarded warning like an explicitly elastic app.
func TestPatchApp_InheritedElasticIsolationGuardsMemoryLimit(t *testing.T) {
	srv, store := newWorkerFleetDefaultServer(t, "per_session", 2000)
	_, token := seedWorkerApp(t, store)

	// Clear the isolation to "" (inherit the fleet default) with a valid dial:
	// resolved as per_session, 2 x (512 + 150) = 1324 <= 2000.
	rec := patchWorkerApp(t, srv, token,
		[]byte(`{"worker_isolation":"","worker_max_workers":2,"memory_limit_mb":512}`))
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if app, _ := store.GetAppBySlug("wapp"); app.WorkerIsolation != "" {
		t.Fatalf("expected stored isolation to be empty (inherit), got %q", app.WorkerIsolation)
	}

	// A memory-only raise busting the budget must be rejected even though the
	// stored isolation is empty: 2 x (1500 + 150) = 3300 > 2000.
	rec = patchWorkerApp(t, srv, token, []byte(`{"memory_limit_mb":1500}`))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("inherited elastic app, budget-busting raise: expected 400, got %d: %s", rec.Code, rec.Body.String())
	}

	// Clearing the limit disarms the static guard: accepted with warning.
	rec = patchWorkerApp(t, srv, token, []byte(`{"memory_limit_mb":null}`))
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if warn := rec.Header().Get("X-ShinyHub-Warning"); warn == "" {
		t.Error("inherited elastic app with guard disarmed: expected a warning")
	}
}
