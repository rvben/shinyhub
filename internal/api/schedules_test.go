package api_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/rvben/shinyhub/internal/auth"
	"github.com/rvben/shinyhub/internal/db"
)

// validScheduleBody returns a JSON body for a schedule that passes validation.
func validScheduleBody(t *testing.T) []byte {
	t.Helper()
	body, _ := json.Marshal(map[string]any{
		"name":            "daily-job",
		"cron_expr":       "0 2 * * *",
		"command":         []string{"Rscript", "daily.R"},
		"enabled":         true,
		"timeout_seconds": 300,
		"overlap_policy":  "skip",
		"missed_policy":   "skip",
	})
	return body
}

// TestSchedules_CreateAndList_HappyPath verifies that an app owner can create
// a schedule and then retrieve it via the list endpoint.
func TestSchedules_CreateAndList_HappyPath(t *testing.T) {
	srv, store, _ := newManagerTestServer(t)

	// Create owner user.
	hash, _ := auth.HashPassword("pass")
	store.CreateUser(db.CreateUserParams{Username: "owner", PasswordHash: hash, Role: "developer"})
	token, _ := auth.IssueJWT(1, "owner", "developer", "test-secret")

	// Create app owned by user ID 1.
	if err := store.CreateApp(db.CreateAppParams{
		Slug:    "my-app",
		Name:    "My App",
		OwnerID: 1,
	}); err != nil {
		t.Fatalf("create app: %v", err)
	}

	// POST /api/apps/my-app/schedules — should return 201.
	req := authedRequest(t, "POST", "/api/apps/my-app/schedules", validScheduleBody(t), token)
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", rec.Code, rec.Body.String())
	}

	var created map[string]any
	if err := json.NewDecoder(bytes.NewReader(rec.Body.Bytes())).Decode(&created); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if created["name"] != "daily-job" {
		t.Errorf("expected name=daily-job, got %v", created["name"])
	}

	// GET /api/apps/my-app/schedules — should return 200 with the created schedule.
	req = authedRequest(t, "GET", "/api/apps/my-app/schedules", nil, token)
	rec = httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var list []map[string]any
	if err := json.NewDecoder(rec.Body).Decode(&list); err != nil {
		t.Fatalf("decode list: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("expected 1 schedule, got %d", len(list))
	}
	if list[0]["name"] != "daily-job" {
		t.Errorf("expected name=daily-job in list, got %v", list[0]["name"])
	}
}

// TestSchedules_Create_ValidationRejected verifies that a bad cron expression
// causes the server to return 400.
func TestSchedules_Create_ValidationRejected(t *testing.T) {
	srv, store, _ := newManagerTestServer(t)

	hash, _ := auth.HashPassword("pass")
	store.CreateUser(db.CreateUserParams{Username: "owner", PasswordHash: hash, Role: "developer"})
	token, _ := auth.IssueJWT(1, "owner", "developer", "test-secret")

	if err := store.CreateApp(db.CreateAppParams{
		Slug:    "my-app",
		Name:    "My App",
		OwnerID: 1,
	}); err != nil {
		t.Fatalf("create app: %v", err)
	}

	body, _ := json.Marshal(map[string]any{
		"name":            "bad-schedule",
		"cron_expr":       "not-a-cron",
		"command":         []string{"echo", "hi"},
		"enabled":         true,
		"timeout_seconds": 60,
		"overlap_policy":  "skip",
		"missed_policy":   "skip",
	})

	req := authedRequest(t, "POST", "/api/apps/my-app/schedules", body, token)
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for invalid cron, got %d: %s", rec.Code, rec.Body.String())
	}
}

// TestSchedules_Create_ViewerCannotCreate verifies that a user who has view
// access but not management rights on the app receives 403.
func TestSchedules_Create_ViewerCannotCreate(t *testing.T) {
	srv, store, _ := newManagerTestServer(t)

	// Owner creates the app.
	ownerHash, _ := auth.HashPassword("pass")
	store.CreateUser(db.CreateUserParams{Username: "owner", PasswordHash: ownerHash, Role: "developer"})
	if err := store.CreateApp(db.CreateAppParams{
		Slug:    "my-app",
		Name:    "My App",
		OwnerID: 1, // owner user ID
	}); err != nil {
		t.Fatalf("create app: %v", err)
	}

	// A second user granted explicit member access (view-only, not manager role).
	viewerHash, _ := auth.HashPassword("pass")
	store.CreateUser(db.CreateUserParams{Username: "viewer", PasswordHash: viewerHash, Role: "viewer"})
	// Grant access so requireViewApp passes, but with default role (not "manager").
	if err := store.GrantAppAccess("my-app", 2); err != nil {
		t.Fatalf("grant app access: %v", err)
	}
	viewerToken, _ := auth.IssueJWT(2, "viewer", "viewer", "test-secret")

	req := authedRequest(t, "POST", "/api/apps/my-app/schedules", validScheduleBody(t), viewerToken)
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)

	// requireManageApp returns 403 when the user has view access but cannot manage.
	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d: %s", rec.Code, rec.Body.String())
	}
}

// TestSchedules_Patch_RejectsCrossAppSchedule verifies that a manager of app B
// cannot patch a schedule that belongs to app A.
func TestSchedules_Patch_RejectsCrossAppSchedule(t *testing.T) {
	srv, store, _ := newManagerTestServer(t)

	// User 1 owns app-a.
	hashA, _ := auth.HashPassword("pass")
	store.CreateUser(db.CreateUserParams{Username: "owner-a", PasswordHash: hashA, Role: "developer"})
	if err := store.CreateApp(db.CreateAppParams{Slug: "app-a", Name: "App A", OwnerID: 1}); err != nil {
		t.Fatalf("create app-a: %v", err)
	}
	tokenA, _ := auth.IssueJWT(1, "owner-a", "developer", "test-secret")

	// User 2 owns app-b.
	hashB, _ := auth.HashPassword("pass")
	store.CreateUser(db.CreateUserParams{Username: "owner-b", PasswordHash: hashB, Role: "developer"})
	if err := store.CreateApp(db.CreateAppParams{Slug: "app-b", Name: "App B", OwnerID: 2}); err != nil {
		t.Fatalf("create app-b: %v", err)
	}
	tokenB, _ := auth.IssueJWT(2, "owner-b", "developer", "test-secret")

	// Create a schedule in app-a as owner-a.
	req := authedRequest(t, "POST", "/api/apps/app-a/schedules", validScheduleBody(t), tokenA)
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create schedule: expected 201, got %d: %s", rec.Code, rec.Body.String())
	}
	var created map[string]any
	json.NewDecoder(bytes.NewReader(rec.Body.Bytes())).Decode(&created)
	schedID := int64(created["id"].(float64))

	// As owner-b (manager of app-b), try to PATCH the schedule that belongs to app-a.
	patchBody, _ := json.Marshal(map[string]any{"enabled": false})
	req = authedRequest(t, "PATCH", fmt.Sprintf("/api/apps/app-b/schedules/%d", schedID), patchBody, tokenB)
	rec = httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404 for cross-app schedule patch, got %d: %s", rec.Code, rec.Body.String())
	}
}

// TestSchedules_Cancel_RejectsCrossAppRun verifies that a manager of app B
// cannot cancel a run that belongs to a schedule in app A.
func TestSchedules_Cancel_RejectsCrossAppRun(t *testing.T) {
	srv, store, _ := newManagerTestServer(t)

	// User 1 owns app-a.
	hashA, _ := auth.HashPassword("pass")
	store.CreateUser(db.CreateUserParams{Username: "owner-a", PasswordHash: hashA, Role: "developer"})
	if err := store.CreateApp(db.CreateAppParams{Slug: "app-a", Name: "App A", OwnerID: 1}); err != nil {
		t.Fatalf("create app-a: %v", err)
	}
	tokenA, _ := auth.IssueJWT(1, "owner-a", "developer", "test-secret")

	// User 2 owns app-b.
	hashB, _ := auth.HashPassword("pass")
	store.CreateUser(db.CreateUserParams{Username: "owner-b", PasswordHash: hashB, Role: "developer"})
	if err := store.CreateApp(db.CreateAppParams{Slug: "app-b", Name: "App B", OwnerID: 2}); err != nil {
		t.Fatalf("create app-b: %v", err)
	}
	tokenB, _ := auth.IssueJWT(2, "owner-b", "developer", "test-secret")

	// Create a schedule in app-a as owner-a.
	req := authedRequest(t, "POST", "/api/apps/app-a/schedules", validScheduleBody(t), tokenA)
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create schedule: expected 201, got %d: %s", rec.Code, rec.Body.String())
	}
	var created map[string]any
	json.NewDecoder(bytes.NewReader(rec.Body.Bytes())).Decode(&created)
	schedID := int64(created["id"].(float64))

	// Fabricate a run row for the schedule in app-a directly via the store.
	runID, err := store.InsertScheduleRun(db.InsertScheduleRunParams{
		ScheduleID: schedID,
		Status:     "running",
		Trigger:    "manual",
		StartedAt:  time.Now(),
		LogPath:    "/tmp/test.log",
	})
	if err != nil {
		t.Fatalf("insert schedule run: %v", err)
	}

	// As owner-b (manager of app-b), try to cancel the run belonging to app-a's schedule.
	// Use schedID as the {id} segment — even a matching schedule ID from a different app must be rejected.
	req = authedRequest(t, "POST", fmt.Sprintf("/api/apps/app-b/schedules/%d/runs/%d/cancel", schedID, runID), nil, tokenB)
	rec = httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404 for cross-app run cancel, got %d: %s", rec.Code, rec.Body.String())
	}
}
