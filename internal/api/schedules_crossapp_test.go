package api_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/rvben/shinyhub/internal/auth"
	"github.com/rvben/shinyhub/internal/db"
)

// crossAppScheduleFixture sets up owner-a/app-a and owner-b/app-b, creates a
// schedule in app-a, and returns app-b's token + the app-a schedule ID.
func crossAppScheduleFixture(t *testing.T, srv interface {
	Router() http.Handler
}, store *db.Store) (tokenB string, schedID int64) {
	t.Helper()
	hashA, _ := testHashPassword("pass")
	store.CreateUser(db.CreateUserParams{Username: "owner-a", PasswordHash: hashA, Role: "developer"})
	if err := store.CreateApp(db.CreateAppParams{Slug: "app-a", Name: "App A", OwnerID: 1}); err != nil {
		t.Fatalf("create app-a: %v", err)
	}
	tokenA, _ := auth.IssueJWT(1, "owner-a", "developer", "test-secret")

	hashB, _ := testHashPassword("pass")
	store.CreateUser(db.CreateUserParams{Username: "owner-b", PasswordHash: hashB, Role: "developer"})
	if err := store.CreateApp(db.CreateAppParams{Slug: "app-b", Name: "App B", OwnerID: 2}); err != nil {
		t.Fatalf("create app-b: %v", err)
	}
	tokenB, _ = auth.IssueJWT(2, "owner-b", "developer", "test-secret")

	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, authedRequest(t, "POST", "/api/apps/app-a/schedules", validScheduleBody(t), tokenA))
	if rec.Code != http.StatusCreated {
		t.Fatalf("create schedule in app-a: expected 201, got %d: %s", rec.Code, rec.Body.String())
	}
	var created map[string]any
	json.NewDecoder(bytes.NewReader(rec.Body.Bytes())).Decode(&created)
	return tokenB, int64(created["id"].(float64))
}

// TestSchedules_Delete_RejectsCrossAppSchedule proves the cross-app IDOR guard
// on handleDeleteSchedule: a manager of app-b cannot delete a schedule owned by
// app-a (TEST-10 - this guard had no test).
func TestSchedules_Delete_RejectsCrossAppSchedule(t *testing.T) {
	srv, store, _ := newManagerTestServer(t)
	tokenB, schedID := crossAppScheduleFixture(t, srv, store)

	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, authedRequest(t, "DELETE", fmt.Sprintf("/api/apps/app-b/schedules/%d", schedID), nil, tokenB))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404 for cross-app schedule delete, got %d: %s", rec.Code, rec.Body.String())
	}
	// The schedule must still exist (not deleted through the wrong app).
	if _, err := store.GetSchedule(schedID); err != nil {
		t.Fatalf("schedule was deleted via a cross-app request: %v", err)
	}
}

// TestSchedules_Run_RejectsCrossAppSchedule proves the cross-app IDOR guard on
// handleRunSchedule: a manager of app-b cannot trigger a run of app-a's schedule.
func TestSchedules_Run_RejectsCrossAppSchedule(t *testing.T) {
	srv, store, _ := newManagerTestServer(t)
	tokenB, schedID := crossAppScheduleFixture(t, srv, store)

	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, authedRequest(t, "POST", fmt.Sprintf("/api/apps/app-b/schedules/%d/run", schedID), nil, tokenB))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404 for cross-app schedule run, got %d: %s", rec.Code, rec.Body.String())
	}
}
