package api_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/rvben/shinyhub/internal/auth"
	"github.com/rvben/shinyhub/internal/db"
	"github.com/rvben/shinyhub/internal/process"
)

// setupIdempotencyApp creates the standard test user + app and returns the
// developer JWT. The user is always owner id=1.
func setupIdempotencyApp(t *testing.T, store *db.Store, slug string) string {
	t.Helper()
	hash, _ := testHashPassword("pass")
	store.CreateUser(db.CreateUserParams{Username: "owner", PasswordHash: hash, Role: "developer"})
	token, _ := auth.IssueJWT(1, "owner", "developer", "test-secret")
	if err := store.CreateApp(db.CreateAppParams{Slug: slug, Name: "Test App", OwnerID: 1}); err != nil {
		t.Fatalf("create app: %v", err)
	}
	return token
}

// TestStopApp_AlreadyStopped_Returns200 verifies that stopping a never-started
// app succeeds with 200. The handler always performs the stop idempotently
// (the process manager ignores a stop on a non-running process) and returns
// the updated app object.
func TestStopApp_AlreadyStopped_Returns200(t *testing.T) {
	srv, store, _ := newManagerTestServer(t)
	token := setupIdempotencyApp(t, store, "idle-app")

	req := authedRequest(t, "POST", "/api/apps/idle-app/stop", nil, token)
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("stop already-stopped app: expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var body map[string]any
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode stop response: %v", err)
	}
	if status, _ := body["status"].(string); status != "stopped" {
		t.Errorf("status = %q, want stopped", status)
	}
}

// TestRestartApp_WithIfNotRunning_WhenAlreadyRunning_Returns200NoOp verifies
// that POST /api/apps/{slug}/restart?if_not_running=true returns 200 with
// {"status":"running","note":"already running"} when the DB status is "running"
// AND at least one live replica exists in the manager.
func TestRestartApp_WithIfNotRunning_AlreadyDeployed_Returns200NoOp(t *testing.T) {
	srv, store, mgr := newManagerTestServer(t)
	token := setupIdempotencyApp(t, store, "live-app")

	// Mark the app as running in the DB.
	if err := store.UpdateAppStatus(db.UpdateAppStatusParams{Slug: "live-app", Status: "running"}); err != nil {
		t.Fatalf("set running: %v", err)
	}
	// Inject a live replica so the liveness check confirms the process is up.
	mgr.ForceEntry("live-app", process.ProcessInfo{
		Slug:   "live-app",
		Index:  0,
		PID:    99999,
		Status: process.StatusRunning,
	})

	req := authedRequest(t, "POST", "/api/apps/live-app/restart?if_not_running=true", nil, token)
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("start already-running: expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var body map[string]any
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if status, _ := body["status"].(string); status != "running" {
		t.Errorf("status = %q, want running", status)
	}
	if note, _ := body["note"].(string); note != "already running" {
		t.Errorf("note = %q, want already running", note)
	}
}

// TestRestartApp_WithIfNotRunning_StaleRunningStatus_FallsThroughToStart verifies
// that when the DB status is "running" but no live replica exists in the manager
// (the hibernation window: process stopped before DB updated), the no-op branch
// is skipped and the handler falls through to start the app. The test app has no
// prior deployment, so the handler returns 409 "app has no successful deployment" -
// the important thing is it did NOT return the no-op 200 with "already running".
func TestRestartApp_WithIfNotRunning_StaleRunningStatus_FallsThroughToStart(t *testing.T) {
	srv, store, _ := newManagerTestServer(t)
	token := setupIdempotencyApp(t, store, "stale-app")

	// Set DB status to running but leave the manager pool empty (no live process).
	if err := store.UpdateAppStatus(db.UpdateAppStatusParams{Slug: "stale-app", Status: "running"}); err != nil {
		t.Fatalf("set running: %v", err)
	}
	// Manager has no entries for "stale-app", so AllForSlug returns an empty slice.

	req := authedRequest(t, "POST", "/api/apps/stale-app/restart?if_not_running=true", nil, token)
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)

	// Must not return 200 with note="already running" - that would be the stale no-op.
	var body map[string]any
	if err := json.NewDecoder(rec.Body).Decode(&body); err == nil {
		if note, _ := body["note"].(string); note == "already running" {
			t.Errorf("handler returned stale no-op (already running) despite no live process")
		}
	}
	// The handler must have fallen through: with no deployment it hits 409.
	if rec.Code == http.StatusOK {
		t.Errorf("expected non-200 response when status=running but no live process, got 200: %s", rec.Body.String())
	}
}

// TestDeleteApp_Missing_Returns404 verifies that DELETE of a nonexistent slug
// returns 404. The CLI maps this to exit 0 with "absent" semantics; the
// server stays truthful.
func TestDeleteApp_Missing_Returns404(t *testing.T) {
	srv, store, _ := newManagerTestServer(t)
	hash, _ := testHashPassword("pass")
	store.CreateUser(db.CreateUserParams{Username: "owner", PasswordHash: hash, Role: "developer"})
	token, _ := auth.IssueJWT(1, "owner", "developer", "test-secret")

	req := authedRequest(t, "DELETE", "/api/apps/nonexistent", nil, token)
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("delete missing app: expected 404, got %d: %s", rec.Code, rec.Body.String())
	}
}

// TestRevokeAppAccess_NonMember_Returns204 verifies that revoking access for a
// user who is not a member returns 204 no-op instead of 404. This makes the
// revoke path idempotent: the outcome (user has no access) is already in place.
func TestRevokeAppAccess_NonMember_Returns204(t *testing.T) {
	srv, store, _ := newManagerTestServer(t)
	token := setupIdempotencyApp(t, store, "my-app")

	// Create a second user who has no access to the app.
	hash2, _ := testHashPassword("pass")
	store.CreateUser(db.CreateUserParams{Username: "stranger", PasswordHash: hash2, Role: "developer"})
	// user id 2 = stranger

	body, _ := json.Marshal(map[string]any{"user_id": 2})
	req := authedRequest(t, "DELETE", "/api/apps/my-app/members", body, token)
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("revoke non-member: expected 204 no-op, got %d: %s", rec.Code, rec.Body.String())
	}
}

// TestGrantAppAccess_DuplicateSameRole_Returns204 verifies that granting the
// same user access twice with the same role returns 204 and is idempotent.
// The DB already uses INSERT ... ON CONFLICT DO NOTHING, so this should pass
// without any handler change.
func TestGrantAppAccess_DuplicateSameRole_Returns204(t *testing.T) {
	srv, store, _ := newManagerTestServer(t)
	token := setupIdempotencyApp(t, store, "my-app")

	hash2, _ := testHashPassword("pass")
	store.CreateUser(db.CreateUserParams{Username: "alice", PasswordHash: hash2, Role: "developer"})

	body, _ := json.Marshal(map[string]any{"username": "alice", "role": "viewer"})

	// First grant.
	req := authedRequest(t, "POST", "/api/apps/my-app/members", body, token)
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("first grant: expected 204, got %d: %s", rec.Code, rec.Body.String())
	}

	// Second grant with the same role - must be idempotent.
	req = authedRequest(t, "POST", "/api/apps/my-app/members", bytes.Clone(body), token)
	rec = httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("duplicate grant same role: expected 204, got %d: %s", rec.Code, rec.Body.String())
	}
}

// TestGrantAppAccess_SameUserDifferentRole_UpdatesRole verifies that granting
// the same user a different role updates the role, returning 204.
func TestGrantAppAccess_SameUserDifferentRole_UpdatesRole(t *testing.T) {
	srv, store, _ := newManagerTestServer(t)
	token := setupIdempotencyApp(t, store, "my-app")

	hash2, _ := testHashPassword("pass")
	store.CreateUser(db.CreateUserParams{Username: "alice", PasswordHash: hash2, Role: "developer"})

	body1, _ := json.Marshal(map[string]any{"username": "alice", "role": "viewer"})
	req := authedRequest(t, "POST", "/api/apps/my-app/members", body1, token)
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("first grant: expected 204, got %d", rec.Code)
	}

	// Now promote to manager.
	body2, _ := json.Marshal(map[string]any{"username": "alice", "role": "manager"})
	req = authedRequest(t, "POST", "/api/apps/my-app/members", body2, token)
	rec = httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("role upgrade: expected 204, got %d: %s", rec.Code, rec.Body.String())
	}

	// Verify the role was updated.
	members, err := store.GetAppMembers("my-app")
	if err != nil {
		t.Fatalf("GetAppMembers: %v", err)
	}
	var found bool
	for _, m := range members {
		if m.Username == "alice" {
			found = true
			if m.Role != "manager" {
				t.Errorf("alice role = %q, want manager", m.Role)
			}
		}
	}
	if !found {
		t.Error("alice not found in members after role upgrade")
	}
}

// TestShareAdd_DuplicateMount_Returns200NoOp verifies that a second POST to
// mount the same source returns 200 no-op instead of 409 Conflict.
func TestShareAdd_DuplicateMount_Returns200NoOp(t *testing.T) {
	srv, store, _ := newManagerTestServer(t)
	token := setupIdempotencyApp(t, store, "consumer-app")

	// Create a second app as the data source.
	if err := store.CreateApp(db.CreateAppParams{Slug: "source-app", Name: "Source", OwnerID: 1}); err != nil {
		t.Fatalf("create source app: %v", err)
	}

	body, _ := json.Marshal(map[string]string{"source_slug": "source-app"})

	// First mount - must succeed with 201.
	req := authedRequest(t, "POST", "/api/apps/consumer-app/shared-data", body, token)
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("first mount: expected 201, got %d: %s", rec.Code, rec.Body.String())
	}

	// Second mount with same source - must return 200 no-op.
	req = authedRequest(t, "POST", "/api/apps/consumer-app/shared-data", bytes.Clone(body), token)
	rec = httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("duplicate mount: expected 200 no-op, got %d: %s", rec.Code, rec.Body.String())
	}
	var resp map[string]any
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode duplicate mount response: %v", err)
	}
	if resp["source_slug"] != "source-app" {
		t.Errorf("source_slug = %v, want source-app", resp["source_slug"])
	}
}

// TestRevokeSharedData_NotMounted_Returns200 verifies that revoking a
// non-existent shared-data mount returns 200 no-op. The outcome (not mounted)
// is already in place; repeating the operation is safe.
func TestRevokeSharedData_NotMounted_Returns200(t *testing.T) {
	srv, store, _ := newManagerTestServer(t)
	token := setupIdempotencyApp(t, store, "consumer-app")

	if err := store.CreateApp(db.CreateAppParams{Slug: "source-app", Name: "Source", OwnerID: 1}); err != nil {
		t.Fatalf("create source app: %v", err)
	}

	req := authedRequest(t, "DELETE", "/api/apps/consumer-app/shared-data/source-app", nil, token)
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("revoke non-mount: expected 204 no-op, got %d: %s", rec.Code, rec.Body.String())
	}
}

// TestEnvSet_IdenticalValue_ReturnsChangedFalse verifies that setting an env
// var to its current value returns {"changed": false} in the response body,
// allowing the CLI to skip --restart side effects when nothing changed.
func TestEnvSet_IdenticalValue_ReturnsChangedFalse(t *testing.T) {
	srv, store, _ := newManagerTestServer(t)
	token := setupIdempotencyApp(t, store, "my-app")

	app, _ := store.GetAppBySlug("my-app")
	if err := store.UpsertAppEnvVar(app.ID, "PORT", []byte("8080"), false); err != nil {
		t.Fatalf("pre-seed env var: %v", err)
	}

	body, _ := json.Marshal(map[string]any{"value": "8080", "secret": false})
	req := authedRequest(t, "PUT", "/api/apps/my-app/env/PORT", body, token)
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("env set identical: expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var resp map[string]any
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if changed, _ := resp["changed"].(bool); changed {
		t.Errorf("changed = true, want false for identical value")
	}
}

// TestEnvSet_DifferentValue_ReturnsChangedTrue verifies that a value change
// returns {"changed": true}.
func TestEnvSet_DifferentValue_ReturnsChangedTrue(t *testing.T) {
	srv, store, _ := newManagerTestServer(t)
	token := setupIdempotencyApp(t, store, "my-app")

	app, _ := store.GetAppBySlug("my-app")
	if err := store.UpsertAppEnvVar(app.ID, "PORT", []byte("8080"), false); err != nil {
		t.Fatalf("pre-seed env var: %v", err)
	}

	body, _ := json.Marshal(map[string]any{"value": "9090", "secret": false})
	req := authedRequest(t, "PUT", "/api/apps/my-app/env/PORT", body, token)
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("env set new value: expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var resp map[string]any
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if changed, _ := resp["changed"].(bool); !changed {
		t.Errorf("changed = false, want true for new value")
	}
}

// TestEnvSet_NewKey_ReturnsChangedTrue verifies that a brand-new key returns
// {"changed": true}.
func TestEnvSet_NewKey_ReturnsChangedTrue(t *testing.T) {
	srv, store, _ := newManagerTestServer(t)
	token := setupIdempotencyApp(t, store, "my-app")
	// No pre-existing env var.
	_ = store

	body, _ := json.Marshal(map[string]any{"value": "hello", "secret": false})
	req := authedRequest(t, "PUT", "/api/apps/my-app/env/GREETING", body, token)
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("env set new key: expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var resp map[string]any
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if changed, _ := resp["changed"].(bool); !changed {
		t.Errorf("changed = false, want true for new key")
	}
}

// TestEnvSet_SequentialIdempotency drives the real handler twice on the same
// key via the router to cover the full production code path. The first PUT must
// return changed:true; the repeat with the identical value must return
// changed:false, exercising the comparison branch that enables the CLI to skip
// its restart side effect.
func TestEnvSet_SequentialIdempotency(t *testing.T) {
	srv, store, _ := newManagerTestServer(t)
	token := setupIdempotencyApp(t, store, "my-app")

	body, _ := json.Marshal(map[string]any{"value": "8080", "secret": false})

	// First PUT: key is new, must be changed:true.
	req := authedRequest(t, "PUT", "/api/apps/my-app/env/PORT", body, token)
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("first PUT: expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var first map[string]any
	if err := json.NewDecoder(rec.Body).Decode(&first); err != nil {
		t.Fatalf("decode first response: %v", err)
	}
	if changed, _ := first["changed"].(bool); !changed {
		t.Errorf("first PUT: changed = false, want true for new key")
	}

	// Second PUT: identical value. The handler must detect no change and return
	// changed:false. This is the response the CLI uses to skip --restart.
	req = authedRequest(t, "PUT", "/api/apps/my-app/env/PORT", bytes.Clone(body), token)
	rec = httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("second PUT: expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var second map[string]any
	if err := json.NewDecoder(rec.Body).Decode(&second); err != nil {
		t.Fatalf("decode second response: %v", err)
	}
	if changed, _ := second["changed"].(bool); changed {
		t.Errorf("second PUT identical value: changed = true, want false")
	}
}

// TestScheduleCreate_IdenticalConfig_Returns200NoOp verifies that creating a
// schedule with the same name AND identical config is a no-op (200). Different
// config keeps the 409 Conflict behavior (pinned by TestSchedules_Create_DuplicateName_Returns409).
func TestScheduleCreate_IdenticalConfig_Returns200NoOp(t *testing.T) {
	srv, store, _ := newManagerTestServer(t)
	token := setupIdempotencyApp(t, store, "my-app")

	body := validScheduleBody(t)

	// First POST - must succeed with 201.
	req := authedRequest(t, "POST", "/api/apps/my-app/schedules", body, token)
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("first POST: expected 201, got %d: %s", rec.Code, rec.Body.String())
	}

	// Second POST with IDENTICAL config - must return 200 no-op.
	req = authedRequest(t, "POST", "/api/apps/my-app/schedules", bytes.Clone(body), token)
	rec = httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("identical config duplicate: expected 200 no-op, got %d: %s", rec.Code, rec.Body.String())
	}
	var resp map[string]any
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode no-op response: %v", err)
	}
	if resp["name"] != "daily-job" {
		t.Errorf("no-op response name = %v, want daily-job", resp["name"])
	}
}

// TestScheduleCreate_DifferentConfig_Keeps409 verifies the pinned behavior:
// same name with different config returns 409 Conflict.
func TestScheduleCreate_DifferentConfig_Keeps409(t *testing.T) {
	srv, store, _ := newManagerTestServer(t)
	token := setupIdempotencyApp(t, store, "my-app")
	_ = store

	body := validScheduleBody(t)

	// First POST.
	req := authedRequest(t, "POST", "/api/apps/my-app/schedules", body, token)
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("first POST: expected 201, got %d", rec.Code)
	}

	// Second POST - same name, different cron.
	differentBody, _ := json.Marshal(map[string]any{
		"name":            "daily-job",
		"cron_expr":       "0 6 * * *", // different time
		"command":         []string{"Rscript", "daily.R"},
		"enabled":         true,
		"timeout_seconds": 300,
		"overlap_policy":  "skip",
		"missed_policy":   "skip",
	})
	req = authedRequest(t, "POST", "/api/apps/my-app/schedules", differentBody, token)
	rec = httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)
	if rec.Code != http.StatusConflict {
		t.Fatalf("different config: expected 409, got %d: %s", rec.Code, rec.Body.String())
	}
}
