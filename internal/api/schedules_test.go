package api_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

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

	// requireManageApp returns 403 when the user can view but cannot manage.
	if rec.Code != http.StatusForbidden && rec.Code != http.StatusNotFound {
		t.Fatalf("expected 403 or 404, got %d: %s", rec.Code, rec.Body.String())
	}
}
