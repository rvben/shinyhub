package api_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/rvben/shinyhub/internal/api"
	"github.com/rvben/shinyhub/internal/auth"
	"github.com/rvben/shinyhub/internal/config"
	"github.com/rvben/shinyhub/internal/db"
	"github.com/rvben/shinyhub/internal/dbtest"
	"github.com/rvben/shinyhub/internal/jobs"
	"github.com/rvben/shinyhub/internal/lifecycle/scheduler"
	"github.com/rvben/shinyhub/internal/process"
	"github.com/rvben/shinyhub/internal/proxy"
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

// TestSchedules_Create_SurfacesDSTAdvisory verifies the create response carries
// a dst_advisory when a fixed-hour schedule will fire twice on a DST fall-back
// day, and omits it for a UTC schedule that never overlaps a transition.
func TestSchedules_Create_SurfacesDSTAdvisory(t *testing.T) {
	if _, err := time.LoadLocation("Europe/Amsterdam"); err != nil {
		t.Skipf("tz database missing: %v", err)
	}
	srv, store, _ := newManagerTestServer(t)

	hash, _ := auth.HashPassword("pass")
	store.CreateUser(db.CreateUserParams{Username: "owner", PasswordHash: hash, Role: "developer"})
	token, _ := auth.IssueJWT(1, "owner", "developer", "test-secret")
	if err := store.CreateApp(db.CreateAppParams{Slug: "my-app", Name: "My App", OwnerID: 1}); err != nil {
		t.Fatalf("create app: %v", err)
	}

	// 02:30 Europe/Amsterdam lands in the fall-back repeated hour.
	body, _ := json.Marshal(map[string]any{
		"name":            "nightly",
		"cron_expr":       "30 2 * * *",
		"command":         []string{"echo", "hi"},
		"enabled":         true,
		"timeout_seconds": 60,
		"overlap_policy":  "skip",
		"missed_policy":   "skip",
		"timezone":        "Europe/Amsterdam",
	})
	req := authedRequest(t, "POST", "/api/apps/my-app/schedules", body, token)
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", rec.Code, rec.Body.String())
	}
	var created map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &created)
	advisory, ok := created["dst_advisory"].(string)
	if !ok || advisory == "" {
		t.Fatalf("expected dst_advisory in response, got %v", created["dst_advisory"])
	}
	if !strings.Contains(advisory, "twice") {
		t.Errorf("advisory should explain the double-fire, got %q", advisory)
	}

	// A UTC schedule must not carry an advisory.
	body2, _ := json.Marshal(map[string]any{
		"name":            "utc-job",
		"cron_expr":       "30 2 * * *",
		"command":         []string{"echo", "hi"},
		"enabled":         true,
		"timeout_seconds": 60,
		"overlap_policy":  "skip",
		"missed_policy":   "skip",
		"timezone":        "UTC",
	})
	req2 := authedRequest(t, "POST", "/api/apps/my-app/schedules", body2, token)
	rec2 := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec2, req2)
	if rec2.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", rec2.Code, rec2.Body.String())
	}
	var created2 map[string]any
	_ = json.Unmarshal(rec2.Body.Bytes(), &created2)
	if _, ok := created2["dst_advisory"]; ok {
		t.Errorf("UTC schedule must not carry dst_advisory, got %v", created2["dst_advisory"])
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

// TestSchedules_RunDetail_ReturnsRow verifies that a viewer can fetch a run
// detail by ID and receives the persisted row.
func TestSchedules_RunDetail_ReturnsRow(t *testing.T) {
	srv, store, _ := newManagerTestServer(t)

	hash, _ := auth.HashPassword("pass")
	_ = store.CreateUser(db.CreateUserParams{Username: "alice", PasswordHash: hash, Role: "developer"})
	user, _ := store.GetUserByUsername("alice")
	_ = store.CreateApp(db.CreateAppParams{Slug: "fetch", Name: "fetch", OwnerID: user.ID})
	app, _ := store.GetAppBySlug("fetch")

	schedID, _ := store.CreateSchedule(db.CreateScheduleParams{
		AppID: app.ID, Name: "x", CronExpr: "* * * * *", CommandJSON: `["true"]`,
		Enabled: true, TimeoutSeconds: 10, OverlapPolicy: "skip", MissedPolicy: "skip",
	})
	runID, _ := store.InsertScheduleRun(db.InsertScheduleRunParams{
		ScheduleID: schedID, Status: "succeeded", Trigger: "schedule",
		StartedAt: time.Now().UTC(), LogPath: "/tmp/x.log",
	})

	token, _ := auth.IssueJWT(user.ID, user.Username, user.Role, "test-secret")
	req := authedRequest(t, "GET", fmt.Sprintf("/api/apps/fetch/schedules/%d/runs/%d", schedID, runID), nil, token)
	rr := httptest.NewRecorder()
	srv.Router().ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}

	var got map[string]any
	if err := json.NewDecoder(rr.Body).Decode(&got); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	// SCH-6: the run JSON must use snake_case keys (consistent with every
	// other DTO the API emits) and must not leak the PascalCase field names.
	if got["status"] != "succeeded" {
		t.Errorf("expected status=succeeded, got %v (full: %v)", got["status"], got)
	}
	if _, ok := got["Status"]; ok {
		t.Errorf("response leaks PascalCase key %q; want snake_case only", "Status")
	}
	for _, key := range []string{"id", "schedule_id", "trigger", "started_at", "exit_code"} {
		if _, ok := got[key]; !ok {
			t.Errorf("response missing snake_case key %q (full: %v)", key, got)
		}
	}
	// The server-side log file path is an internal detail with no client
	// consumer; it must not be serialized to API clients.
	if _, ok := got["LogPath"]; ok {
		t.Errorf("response leaks server log path under %q", "LogPath")
	}
	if _, ok := got["log_path"]; ok {
		t.Errorf("response leaks server log path under %q", "log_path")
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

// TestSchedules_RunDetail_RejectsCrossAppRun verifies that a viewer of app B
// cannot fetch the JSON detail of a run that belongs to a schedule in app A,
// even when the schedule ID and run ID are known.
func TestSchedules_RunDetail_RejectsCrossAppRun(t *testing.T) {
	srv, store, _ := newManagerTestServer(t)

	hashA, _ := auth.HashPassword("pass")
	store.CreateUser(db.CreateUserParams{Username: "owner-a", PasswordHash: hashA, Role: "developer"})
	if err := store.CreateApp(db.CreateAppParams{Slug: "app-a", Name: "App A", OwnerID: 1}); err != nil {
		t.Fatalf("create app-a: %v", err)
	}

	hashB, _ := auth.HashPassword("pass")
	store.CreateUser(db.CreateUserParams{Username: "owner-b", PasswordHash: hashB, Role: "developer"})
	if err := store.CreateApp(db.CreateAppParams{Slug: "app-b", Name: "App B", OwnerID: 2}); err != nil {
		t.Fatalf("create app-b: %v", err)
	}
	tokenB, _ := auth.IssueJWT(2, "owner-b", "developer", "test-secret")

	appA, _ := store.GetAppBySlug("app-a")
	schedID, err := store.CreateSchedule(db.CreateScheduleParams{
		AppID: appA.ID, Name: "x", CronExpr: "* * * * *", CommandJSON: `["true"]`,
		Enabled: true, TimeoutSeconds: 10, OverlapPolicy: "skip", MissedPolicy: "skip",
	})
	if err != nil {
		t.Fatalf("create schedule: %v", err)
	}
	runID, err := store.InsertScheduleRun(db.InsertScheduleRunParams{
		ScheduleID: schedID, Status: "succeeded", Trigger: "schedule",
		StartedAt: time.Now().UTC(), LogPath: "/tmp/x.log",
	})
	if err != nil {
		t.Fatalf("insert schedule run: %v", err)
	}

	req := authedRequest(t, "GET", fmt.Sprintf("/api/apps/app-b/schedules/%d/runs/%d", schedID, runID), nil, tokenB)
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404 for cross-app run detail, got %d: %s", rec.Code, rec.Body.String())
	}
}

// TestSchedules_RunLogs_RejectsCrossAppRun verifies that a viewer of app B
// cannot stream logs for a run that belongs to a schedule in app A, even when
// the schedule ID and run ID are known.
func TestSchedules_RunLogs_RejectsCrossAppRun(t *testing.T) {
	srv, store, _ := newManagerTestServer(t)

	hashA, _ := auth.HashPassword("pass")
	store.CreateUser(db.CreateUserParams{Username: "owner-a", PasswordHash: hashA, Role: "developer"})
	if err := store.CreateApp(db.CreateAppParams{Slug: "app-a", Name: "App A", OwnerID: 1}); err != nil {
		t.Fatalf("create app-a: %v", err)
	}

	hashB, _ := auth.HashPassword("pass")
	store.CreateUser(db.CreateUserParams{Username: "owner-b", PasswordHash: hashB, Role: "developer"})
	if err := store.CreateApp(db.CreateAppParams{Slug: "app-b", Name: "App B", OwnerID: 2}); err != nil {
		t.Fatalf("create app-b: %v", err)
	}
	tokenB, _ := auth.IssueJWT(2, "owner-b", "developer", "test-secret")

	appA, _ := store.GetAppBySlug("app-a")
	schedID, err := store.CreateSchedule(db.CreateScheduleParams{
		AppID: appA.ID, Name: "x", CronExpr: "* * * * *", CommandJSON: `["true"]`,
		Enabled: true, TimeoutSeconds: 10, OverlapPolicy: "skip", MissedPolicy: "skip",
	})
	if err != nil {
		t.Fatalf("create schedule: %v", err)
	}
	runID, err := store.InsertScheduleRun(db.InsertScheduleRunParams{
		ScheduleID: schedID, Status: "succeeded", Trigger: "schedule",
		StartedAt: time.Now().UTC(), LogPath: "/tmp/x.log",
	})
	if err != nil {
		t.Fatalf("insert schedule run: %v", err)
	}

	req := authedRequest(t, "GET", fmt.Sprintf("/api/apps/app-b/schedules/%d/runs/%d/logs", schedID, runID), nil, tokenB)
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404 for cross-app run logs, got %d: %s", rec.Code, rec.Body.String())
	}
}

// TestSchedules_RunLogs_PlainTextWhenNotFollowing asserts that a one-shot
// (follow=false) request for a finished run's logs returns a plain-text body
// with no SSE "data: " framing, mirroring GET /api/apps/{slug}/logs. This
// keeps scripted callers (and `shinyhub schedule logs` without --follow) from
// having to strip event-stream prefixes. follow=true keeps the SSE shape.
func TestSchedules_RunLogs_PlainTextWhenNotFollowing(t *testing.T) {
	srv, store, _ := newManagerTestServer(t)

	hash, _ := auth.HashPassword("pass")
	_ = store.CreateUser(db.CreateUserParams{Username: "owner", PasswordHash: hash, Role: "developer"})
	if err := store.CreateApp(db.CreateAppParams{Slug: "fetch", Name: "fetch", OwnerID: 1}); err != nil {
		t.Fatalf("create app: %v", err)
	}
	app, _ := store.GetAppBySlug("fetch")
	token, _ := auth.IssueJWT(1, "owner", "developer", "test-secret")

	schedID, _ := store.CreateSchedule(db.CreateScheduleParams{
		AppID: app.ID, Name: "x", CronExpr: "* * * * *", CommandJSON: `["true"]`,
		Enabled: true, TimeoutSeconds: 10, OverlapPolicy: "skip", MissedPolicy: "skip",
	})

	logPath := filepath.Join(t.TempDir(), "run.log")
	if err := os.WriteFile(logPath, []byte("line-one\nline-two\n"), 0o644); err != nil {
		t.Fatalf("write log: %v", err)
	}
	runID, _ := store.InsertScheduleRun(db.InsertScheduleRunParams{
		ScheduleID: schedID, Status: "succeeded", Trigger: "manual",
		StartedAt: time.Now().UTC(), LogPath: logPath,
	})

	// follow=false: plain text, no "data:" framing.
	req := authedRequest(t, "GET", fmt.Sprintf("/api/apps/fetch/schedules/%d/runs/%d/logs?follow=false", schedID, runID), nil, token)
	rr := httptest.NewRecorder()
	srv.Router().ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	if ct := rr.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/plain") {
		t.Errorf("follow=false Content-Type = %q, want text/plain", ct)
	}
	body := rr.Body.String()
	if strings.Contains(body, "data: ") {
		t.Errorf("follow=false body must not contain SSE framing:\n%s", body)
	}
	if !strings.Contains(body, "line-one") || !strings.Contains(body, "line-two") {
		t.Errorf("follow=false body missing log lines:\n%s", body)
	}

	// follow=true: keep the SSE event-stream shape for live streaming clients.
	req = authedRequest(t, "GET", fmt.Sprintf("/api/apps/fetch/schedules/%d/runs/%d/logs?follow=true", schedID, runID), nil, token)
	rr = httptest.NewRecorder()
	srv.Router().ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("follow=true status=%d body=%s", rr.Code, rr.Body.String())
	}
	if ct := rr.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/event-stream") {
		t.Errorf("follow=true Content-Type = %q, want text/event-stream", ct)
	}
}

// TestSchedules_RunLogs_FollowStopsWhenRunFinishes asserts that a follow=true
// stream attached to a still-running run closes once the run reaches a
// terminal state. A finite scheduled command's log stream must end when the
// run ends; otherwise `schedule run --follow` would hang forever and could
// never report the run's exit code.
func TestSchedules_RunLogs_FollowStopsWhenRunFinishes(t *testing.T) {
	srv, store, _ := newManagerTestServer(t)

	hash, _ := auth.HashPassword("pass")
	_ = store.CreateUser(db.CreateUserParams{Username: "owner", PasswordHash: hash, Role: "developer"})
	if err := store.CreateApp(db.CreateAppParams{Slug: "fetch", Name: "fetch", OwnerID: 1}); err != nil {
		t.Fatalf("create app: %v", err)
	}
	app, _ := store.GetAppBySlug("fetch")
	token, _ := auth.IssueJWT(1, "owner", "developer", "test-secret")

	schedID, _ := store.CreateSchedule(db.CreateScheduleParams{
		AppID: app.ID, Name: "x", CronExpr: "* * * * *", CommandJSON: `["true"]`,
		Enabled: true, TimeoutSeconds: 10, OverlapPolicy: "skip", MissedPolicy: "skip",
	})
	logPath := filepath.Join(t.TempDir(), "run.log")
	if err := os.WriteFile(logPath, []byte("starting\n"), 0o644); err != nil {
		t.Fatalf("write log: %v", err)
	}
	runID, _ := store.InsertScheduleRun(db.InsertScheduleRunParams{
		ScheduleID: schedID, Status: "running", Trigger: "manual",
		StartedAt: time.Now().UTC(), LogPath: logPath,
	})

	ts := httptest.NewServer(srv.Router())
	defer ts.Close()

	url := fmt.Sprintf("%s/api/apps/fetch/schedules/%d/runs/%d/logs?follow=true", ts.URL, schedID, runID)
	req, _ := http.NewRequest("GET", url, nil)
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET logs: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status=%d", resp.StatusCode)
	}

	// Mark the run finished shortly after the stream attaches.
	go func() {
		time.Sleep(200 * time.Millisecond)
		exit := 1
		_ = store.FinishScheduleRun(db.FinishScheduleRunParams{
			RunID: runID, Status: "failed", ExitCode: &exit, FinishedAt: time.Now().UTC(),
		})
	}()

	done := make(chan error, 1)
	go func() {
		_, rerr := io.ReadAll(resp.Body)
		done <- rerr
	}()

	select {
	case <-done:
		// Stream closed after the run finished: correct.
	case <-time.After(5 * time.Second):
		t.Fatal("follow stream did not close after the run reached a terminal state")
	}
}

// TestSchedules_RunLogs_RejectsPublicViewer asserts that an unrelated
// authenticated user cannot read a public app's schedule run logs.
// Run logs may contain stderr that includes secret values surfaced by the
// scheduled command, so the endpoint must require manage rights even when
// the app's HTTP surface is public.
func TestSchedules_RunLogs_RejectsPublicViewer(t *testing.T) {
	srv, store, _ := newManagerTestServer(t)

	hashOwner, _ := auth.HashPassword("pass")
	store.CreateUser(db.CreateUserParams{Username: "owner", PasswordHash: hashOwner, Role: "developer"})
	if err := store.CreateApp(db.CreateAppParams{Slug: "pub", Name: "Public", OwnerID: 1}); err != nil {
		t.Fatalf("create app: %v", err)
	}
	if err := store.SetAppAccess("pub", "public"); err != nil {
		t.Fatalf("set public access: %v", err)
	}

	hashOther, _ := auth.HashPassword("pass")
	store.CreateUser(db.CreateUserParams{Username: "stranger", PasswordHash: hashOther, Role: "developer"})
	tokenStranger, _ := auth.IssueJWT(2, "stranger", "developer", "test-secret")

	// Real log file so the test fails for the right reason — auth, not file IO.
	logPath := filepath.Join(t.TempDir(), "run.log")
	if err := os.WriteFile(logPath, []byte("AWS_SECRET_ACCESS_KEY=hunter2\n"), 0600); err != nil {
		t.Fatalf("write log: %v", err)
	}

	app, _ := store.GetAppBySlug("pub")
	schedID, err := store.CreateSchedule(db.CreateScheduleParams{
		AppID: app.ID, Name: "x", CronExpr: "* * * * *", CommandJSON: `["true"]`,
		Enabled: true, TimeoutSeconds: 10, OverlapPolicy: "skip", MissedPolicy: "skip",
	})
	if err != nil {
		t.Fatalf("create schedule: %v", err)
	}
	runID, err := store.InsertScheduleRun(db.InsertScheduleRunParams{
		ScheduleID: schedID, Status: "succeeded", Trigger: "schedule",
		StartedAt: time.Now().UTC(), LogPath: logPath,
	})
	if err != nil {
		t.Fatalf("insert schedule run: %v", err)
	}

	req := authedRequest(t, "GET", fmt.Sprintf("/api/apps/pub/schedules/%d/runs/%d/logs", schedID, runID), nil, tokenStranger)
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403 for public-only viewer reading run logs, got %d: %s", rec.Code, rec.Body.String())
	}
	if bytes.Contains(rec.Body.Bytes(), []byte("hunter2")) {
		t.Fatalf("response body leaked log content: %q", rec.Body.String())
	}
}

// TestSchedules_GrantSharedData_RequiresExplicitAccessOnSource asserts that
// the source app's *visibility* is not enough to grant a shared-data mount.
// A developer who only has public-viewer access to "src" must not be able to
// mount src's data dir into their own app — read-only or not — because that
// dir holds whatever business data the source app's owner shipped via
// `shinyhub data push`. Only an explicit member, the owner, or a platform
// operator may grant the mount. See A.2 in the v0.2.2 audit.
func TestSchedules_GrantSharedData_RequiresExplicitAccessOnSource(t *testing.T) {
	srv, store, _ := newManagerTestServer(t)

	// Source app: public visibility, owned by someone else.
	hashOwner, _ := auth.HashPassword("pass")
	store.CreateUser(db.CreateUserParams{Username: "src-owner", PasswordHash: hashOwner, Role: "developer"})
	if err := store.CreateApp(db.CreateAppParams{Slug: "src", Name: "Source", OwnerID: 1}); err != nil {
		t.Fatalf("create src: %v", err)
	}
	if err := store.SetAppAccess("src", "public"); err != nil {
		t.Fatalf("set src public: %v", err)
	}

	// Caller: a developer who owns their own app but has no explicit access to src.
	hashCaller, _ := auth.HashPassword("pass")
	store.CreateUser(db.CreateUserParams{Username: "caller", PasswordHash: hashCaller, Role: "developer"})
	tokenCaller, _ := auth.IssueJWT(2, "caller", "developer", "test-secret")
	if err := store.CreateApp(db.CreateAppParams{Slug: "mine", Name: "Mine", OwnerID: 2}); err != nil {
		t.Fatalf("create mine: %v", err)
	}

	body, _ := json.Marshal(map[string]any{"source_slug": "src"})
	req := authedRequest(t, "POST", "/api/apps/mine/shared-data", body, tokenCaller)
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403 granting shared-data on public src without explicit access, got %d: %s", rec.Code, rec.Body.String())
	}
}

// TestSchedules_Create_DuplicateName_DifferentConfig_Returns409 verifies that
// posting a second schedule with the same name but different configuration
// returns 409 Conflict with a JSON body that identifies the conflict (not a
// generic 500). Identical-config duplicates are idempotent (200 no-op); only
// configuration conflicts produce 409.
func TestSchedules_Create_DuplicateName_DifferentConfig_Returns409(t *testing.T) {
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

	// First POST — must succeed.
	req := authedRequest(t, "POST", "/api/apps/my-app/schedules", validScheduleBody(t), token)
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("first POST: expected 201, got %d: %s", rec.Code, rec.Body.String())
	}

	// Second POST with same name but different cron expression — must return 409.
	differentBody, _ := json.Marshal(map[string]any{
		"name":            "daily-job",
		"cron_expr":       "0 6 * * *", // different time from validScheduleBody ("0 2 * * *")
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
		t.Fatalf("different config duplicate: expected 409, got %d: %s", rec.Code, rec.Body.String())
	}

	var body map[string]any
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode 409 body: %v", err)
	}
	errMsg, _ := body["error"].(string)
	if errMsg == "" {
		t.Fatalf("409 body missing 'error' field: %v", body)
	}
	if !strings.Contains(errMsg, "already exists") {
		t.Errorf("409 error message should describe the conflict, got: %q", errMsg)
	}
}

// scheduleWithTimezone creates an app + schedule that has an explicit stored
// timezone. Returns the schedule ID and a valid manager token for the owner.
// Used by the PATCH timezone tri-state tests below.
func scheduleWithTimezone(t *testing.T, store *db.Store, storedTZ string) (schedID int64, token string) {
	t.Helper()
	hash, _ := auth.HashPassword("pass")
	_ = store.CreateUser(db.CreateUserParams{Username: "tz-owner", PasswordHash: hash, Role: "developer"})
	_ = store.CreateApp(db.CreateAppParams{Slug: "tz-app", Name: "TZ App", OwnerID: 1})
	app, _ := store.GetAppBySlug("tz-app")
	var tzPtr *string
	if storedTZ != "" {
		tzPtr = &storedTZ
	}
	id, _ := store.CreateSchedule(db.CreateScheduleParams{
		AppID: app.ID, Name: "tz-sched", CronExpr: "0 9 * * *",
		CommandJSON: `["echo","hi"]`, Enabled: true, TimeoutSeconds: 60,
		OverlapPolicy: "skip", MissedPolicy: "skip", Timezone: tzPtr,
	})
	tok, _ := auth.IssueJWT(1, "tz-owner", "developer", "test-secret")
	return id, tok
}

// TestSchedules_Patch_Timezone_Absent_LeavesUnchanged asserts that omitting
// the timezone key from a PATCH body leaves the stored timezone intact. This
// test meaningfully fails if absent and null are collapsed to the same branch.
func TestSchedules_Patch_Timezone_Absent_LeavesUnchanged(t *testing.T) {
	srv, store, _ := newManagerTestServer(t)
	schedID, token := scheduleWithTimezone(t, store, "Europe/Amsterdam")

	// PATCH body has no "timezone" key at all.
	body, _ := json.Marshal(map[string]any{"enabled": false})
	req := authedRequest(t, "PATCH", fmt.Sprintf("/api/apps/tz-app/schedules/%d", schedID), body, token)
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var dto map[string]any
	json.NewDecoder(rec.Body).Decode(&dto)
	// timezone must still be "Europe/Amsterdam", not null/cleared.
	if dto["timezone"] == nil {
		t.Errorf("timezone should be unchanged (Europe/Amsterdam), got null")
	}
	if got, _ := dto["timezone"].(string); got != "Europe/Amsterdam" {
		t.Errorf("timezone = %q, want Europe/Amsterdam", got)
	}
	if inherited, _ := dto["timezone_inherited"].(bool); inherited {
		t.Errorf("timezone_inherited should be false, got true")
	}
}

// TestSchedules_Patch_Timezone_Null_ClearsToInherit asserts that sending
// {"timezone": null} explicitly in a PATCH body clears the stored timezone
// and switches the schedule to server-default inheritance.
func TestSchedules_Patch_Timezone_Null_ClearsToInherit(t *testing.T) {
	srv, store, _ := newManagerTestServer(t)
	schedID, token := scheduleWithTimezone(t, store, "Europe/Amsterdam")

	// PATCH body sets timezone to JSON null.
	body := []byte(fmt.Sprintf(`{"timezone":null,"enabled":true}`))
	req := authedRequest(t, "PATCH", fmt.Sprintf("/api/apps/tz-app/schedules/%d", schedID), body, token)
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var dto map[string]any
	json.NewDecoder(rec.Body).Decode(&dto)
	if dto["timezone"] != nil {
		t.Errorf("timezone should be null (cleared), got %v", dto["timezone"])
	}
	if inherited, _ := dto["timezone_inherited"].(bool); !inherited {
		t.Errorf("timezone_inherited should be true after null clear, got false")
	}
}

// TestSchedules_Patch_Timezone_EmptyString_ClearsToInherit asserts that
// {"timezone": ""} clears the stored timezone to inherit — identical outcome
// to null but via empty string.
func TestSchedules_Patch_Timezone_EmptyString_ClearsToInherit(t *testing.T) {
	srv, store, _ := newManagerTestServer(t)
	schedID, token := scheduleWithTimezone(t, store, "Europe/Amsterdam")

	body := []byte(fmt.Sprintf(`{"timezone":""}`))
	req := authedRequest(t, "PATCH", fmt.Sprintf("/api/apps/tz-app/schedules/%d", schedID), body, token)
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var dto map[string]any
	json.NewDecoder(rec.Body).Decode(&dto)
	if dto["timezone"] != nil {
		t.Errorf("timezone should be null (cleared), got %v", dto["timezone"])
	}
	if inherited, _ := dto["timezone_inherited"].(bool); !inherited {
		t.Errorf("timezone_inherited should be true after empty-string clear, got false")
	}
}

// TestSchedules_Patch_Timezone_ValidZone_Sets asserts that a non-empty valid
// IANA timezone string in a PATCH body is validated and stored; and that an
// invalid timezone returns 400 with a clear error message.
func TestSchedules_Patch_Timezone_ValidZone_Sets(t *testing.T) {
	srv, store, _ := newManagerTestServer(t)
	schedID, token := scheduleWithTimezone(t, store, "Europe/Amsterdam")

	// Valid zone: should succeed and update the stored timezone.
	body := []byte(fmt.Sprintf(`{"timezone":"America/New_York"}`))
	req := authedRequest(t, "PATCH", fmt.Sprintf("/api/apps/tz-app/schedules/%d", schedID), body, token)
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("valid zone: expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var dto map[string]any
	json.NewDecoder(rec.Body).Decode(&dto)
	if got, _ := dto["timezone"].(string); got != "America/New_York" {
		t.Errorf("timezone = %q, want America/New_York", got)
	}
	if got, _ := dto["effective_timezone"].(string); got != "America/New_York" {
		t.Errorf("effective_timezone = %q, want America/New_York", got)
	}
	if inherited, _ := dto["timezone_inherited"].(bool); inherited {
		t.Errorf("timezone_inherited should be false for explicit zone")
	}

	// Invalid zone: should return 400 with a message that mentions the bad value.
	badBody := []byte(fmt.Sprintf(`{"timezone":"Mars/Olympus"}`))
	req = authedRequest(t, "PATCH", fmt.Sprintf("/api/apps/tz-app/schedules/%d", schedID), badBody, token)
	rec = httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("invalid zone: expected 400, got %d: %s", rec.Code, rec.Body.String())
	}
	var errBody map[string]any
	json.NewDecoder(rec.Body).Decode(&errBody)
	errMsg, _ := errBody["error"].(string)
	if !strings.Contains(strings.ToLower(errMsg), "timezone") && !strings.Contains(strings.ToLower(errMsg), "zone") {
		t.Errorf("400 error message should describe the timezone problem, got: %q", errMsg)
	}
}

// TestSchedules_GrantSharedData_AllowedForExplicitMember asserts the happy
// path still works: when the caller is an explicit member of the source app
// (any role), the grant succeeds.
func TestSchedules_GrantSharedData_AllowedForExplicitMember(t *testing.T) {
	srv, store, _ := newManagerTestServer(t)

	hashOwner, _ := auth.HashPassword("pass")
	store.CreateUser(db.CreateUserParams{Username: "src-owner", PasswordHash: hashOwner, Role: "developer"})
	if err := store.CreateApp(db.CreateAppParams{Slug: "src", Name: "Source", OwnerID: 1}); err != nil {
		t.Fatalf("create src: %v", err)
	}

	hashCaller, _ := auth.HashPassword("pass")
	store.CreateUser(db.CreateUserParams{Username: "caller", PasswordHash: hashCaller, Role: "developer"})
	tokenCaller, _ := auth.IssueJWT(2, "caller", "developer", "test-secret")
	if err := store.CreateApp(db.CreateAppParams{Slug: "mine", Name: "Mine", OwnerID: 2}); err != nil {
		t.Fatalf("create mine: %v", err)
	}
	// Make caller an explicit member of src (default role = viewer).
	if err := store.GrantAppAccess("src", 2); err != nil {
		t.Fatalf("grant member: %v", err)
	}

	body, _ := json.Marshal(map[string]any{"source_slug": "src"})
	req := authedRequest(t, "POST", "/api/apps/mine/shared-data", body, tokenCaller)
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("expected 201 granting shared-data with explicit member access, got %d: %s", rec.Code, rec.Body.String())
	}
}

// grantSharedDataFixture sets up a server where "caller" (user id 2) owns two
// apps and returns the server plus the caller's JWT. The caller owns both apps
// so hasExplicitAccess passes for either as a mount source.
func grantSharedDataFixture(t *testing.T) (srv *api.Server, store *db.Store, token string) {
	t.Helper()
	srv, store, _ = newManagerTestServer(t)
	hashCaller, _ := auth.HashPassword("pass")
	store.CreateUser(db.CreateUserParams{Username: "caller", PasswordHash: hashCaller, Role: "developer"})
	token, _ = auth.IssueJWT(1, "caller", "developer", "test-secret")
	if err := store.CreateApp(db.CreateAppParams{Slug: "mine", Name: "Mine", OwnerID: 1}); err != nil {
		t.Fatalf("create mine: %v", err)
	}
	if err := store.CreateApp(db.CreateAppParams{Slug: "other", Name: "Other", OwnerID: 1}); err != nil {
		t.Fatalf("create other: %v", err)
	}
	return srv, store, token
}

// DOC-2: under the native runtime the read-only mount is a convention only -
// the source data dir is symlinked and the filesystem still permits writes
// through it. The grant response must carry a warning so operators learn the
// RO contract is not OS-enforced unless they switch to the Docker runtime.
func TestSchedules_GrantSharedData_NativeRuntimeWarnsReadOnlyConvention(t *testing.T) {
	srv, store := newManagerTestServerWithRuntimeMode(t, "native")
	hashCaller, _ := auth.HashPassword("pass")
	store.CreateUser(db.CreateUserParams{Username: "caller", PasswordHash: hashCaller, Role: "developer"})
	token, _ := auth.IssueJWT(1, "caller", "developer", "test-secret")
	if err := store.CreateApp(db.CreateAppParams{Slug: "mine", Name: "Mine", OwnerID: 1}); err != nil {
		t.Fatalf("create mine: %v", err)
	}
	if err := store.CreateApp(db.CreateAppParams{Slug: "other", Name: "Other", OwnerID: 1}); err != nil {
		t.Fatalf("create other: %v", err)
	}

	body, _ := json.Marshal(map[string]any{"source_slug": "other"})
	req := authedRequest(t, "POST", "/api/apps/mine/shared-data", body, token)
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", rec.Code, rec.Body.String())
	}
	var resp map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	warning, _ := resp["warning"].(string)
	if warning == "" {
		t.Fatalf("expected a read-only-convention warning under native runtime, got none: %s", rec.Body.String())
	}
	if !strings.Contains(warning, "Docker") {
		t.Errorf("warning should point at the Docker runtime for enforcement, got %q", warning)
	}
}

// DOC-2: under the Docker runtime the mount is OS-enforced read-only, so the
// grant response must NOT carry the convention warning.
func TestSchedules_GrantSharedData_DockerRuntimeOmitsWarning(t *testing.T) {
	srv, store := newManagerTestServerWithRuntimeMode(t, "docker")
	hashCaller, _ := auth.HashPassword("pass")
	store.CreateUser(db.CreateUserParams{Username: "caller", PasswordHash: hashCaller, Role: "developer"})
	token, _ := auth.IssueJWT(1, "caller", "developer", "test-secret")
	if err := store.CreateApp(db.CreateAppParams{Slug: "mine", Name: "Mine", OwnerID: 1}); err != nil {
		t.Fatalf("create mine: %v", err)
	}
	if err := store.CreateApp(db.CreateAppParams{Slug: "other", Name: "Other", OwnerID: 1}); err != nil {
		t.Fatalf("create other: %v", err)
	}

	body, _ := json.Marshal(map[string]any{"source_slug": "other"})
	req := authedRequest(t, "POST", "/api/apps/mine/shared-data", body, token)
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", rec.Code, rec.Body.String())
	}
	var resp map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if _, ok := resp["warning"]; ok {
		t.Errorf("docker runtime enforces RO; warning key must be absent, got %s", rec.Body.String())
	}
}

// ERR-2: a self-mount (source == consumer) is a client error, not a 500.
func TestSchedules_GrantSharedData_SelfMountReturns400(t *testing.T) {
	srv, _, token := grantSharedDataFixture(t)

	body, _ := json.Marshal(map[string]any{"source_slug": "mine"})
	req := authedRequest(t, "POST", "/api/apps/mine/shared-data", body, token)
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("self-mount: expected 400, got %d: %s", rec.Code, rec.Body.String())
	}
}

// ERR-3: granting the same mount twice is idempotent (200 no-op), not a 409.
// The outcome (already mounted) is already in place; the source_slug is returned
// so the caller can confirm which mount the no-op refers to.
func TestSchedules_GrantSharedData_DuplicateReturns200NoOp(t *testing.T) {
	srv, _, token := grantSharedDataFixture(t)

	body, _ := json.Marshal(map[string]any{"source_slug": "other"})
	req := authedRequest(t, "POST", "/api/apps/mine/shared-data", body, token)
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("first grant: expected 201, got %d: %s", rec.Code, rec.Body.String())
	}

	body2, _ := json.Marshal(map[string]any{"source_slug": "other"})
	req = authedRequest(t, "POST", "/api/apps/mine/shared-data", body2, token)
	rec = httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("duplicate grant: expected 200 no-op, got %d: %s", rec.Code, rec.Body.String())
	}
	var resp map[string]any
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode duplicate response: %v", err)
	}
	if resp["source_slug"] != "other" {
		t.Errorf("source_slug = %v, want other", resp["source_slug"])
	}
}

// SCH-3: a grant that would close a read cycle (mine->other, then other->mine)
// is a client error, not a 500.
func TestSchedules_GrantSharedData_CycleReturns400(t *testing.T) {
	srv, _, token := grantSharedDataFixture(t)

	// mine reads other.
	body, _ := json.Marshal(map[string]any{"source_slug": "other"})
	req := authedRequest(t, "POST", "/api/apps/mine/shared-data", body, token)
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("grant mine->other: expected 201, got %d: %s", rec.Code, rec.Body.String())
	}

	// other reads mine would close the cycle.
	body2, _ := json.Marshal(map[string]any{"source_slug": "mine"})
	req = authedRequest(t, "POST", "/api/apps/other/shared-data", body2, token)
	rec = httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("cycle grant: expected 400, got %d: %s", rec.Code, rec.Body.String())
	}
}

// ERR-4: revoking a mount that was never granted is a 204 no-op. The outcome
// (not mounted) is already in place; repeating the revoke is safe.
func TestSchedules_RevokeSharedData_NotMountedReturns204(t *testing.T) {
	srv, _, token := grantSharedDataFixture(t)

	req := authedRequest(t, "DELETE", "/api/apps/mine/shared-data/other", nil, token)
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("revoke non-mounted: expected 204 no-op, got %d: %s", rec.Code, rec.Body.String())
	}
}

// buildScheduleE2EServer wires the shared pieces for the schedule API tests: an
// admin user + token, a config, a native-runtime process manager, the server,
// and a real jobs.Manager. It deliberately does NOT call SetJobs, so each caller
// chooses how to wire the scheduler (nil, or a real not-started one).
func buildScheduleE2EServer(t *testing.T) (srv *api.Server, store *db.Store, token string, jm *jobs.Manager) {
	t.Helper()
	appsDir := t.TempDir()
	store = dbtest.New(t)

	hash, _ := auth.HashPassword("pass")
	if err := store.CreateUser(db.CreateUserParams{
		Username: "admin", PasswordHash: hash, Role: "admin",
	}); err != nil {
		t.Fatal(err)
	}
	admin, _ := store.GetUserByUsername("admin")
	token, _ = auth.IssueJWT(admin.ID, admin.Username, admin.Role, "test-secret")

	cfg := &config.Config{
		Auth:    config.AuthConfig{Secret: "test-secret"},
		Storage: config.StorageConfig{AppsDir: appsDir, VersionRetention: 5},
	}
	mgr := process.NewManager(appsDir, process.NewNativeRuntime())
	srv = api.New(cfg, store, mgr, proxy.New())

	var err error
	jm, err = jobs.NewManager(mgr, nil, process.DefaultTier, store, nil, appsDir, appsDir)
	if err != nil {
		t.Fatalf("jobs.NewManager: %v", err)
	}
	// Drain the jobs manager before t.TempDir() cleanup runs. A run triggered by
	// the test (run_on_register first-fire, manual run) launches an async execute
	// goroutine that writes a run log under appsDir (a t.TempDir). If that
	// goroutine is still writing when Go's RemoveAll cleanup deletes the dir, the
	// unlink races ("directory not empty") and the test flakes under load. Stop
	// cancels the run contexts and waits for the goroutines to finish; registered
	// after t.TempDir()/dbtest.New so it runs first (cleanups are LIFO), draining
	// the writers before the directory is removed and the store is closed.
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		jm.Stop(ctx)
	})
	return srv, store, token, jm
}

func newScheduleE2EServerWithJobs(t *testing.T) (*api.Server, *db.Store, string) {
	t.Helper()
	srv, store, token, jm := buildScheduleE2EServer(t)
	// Pass nil scheduler so s.scheduler is nil and Reload is skipped; s.jobs is
	// non-nil so run_on_register dispatch is enabled.
	srv.SetJobs(jm, nil)
	return srv, store, token
}

// TestCreateSchedule_SchedulerNotStarted_Returns201 verifies that creating a
// schedule while the cron engine has not started yet returns 201, not 500: the
// row IS persisted and activates when the scheduler starts. This matches the
// deploy path's soft handling of scheduler.ErrNotStarted (both go through
// Server.reloadScheduler).
func TestCreateSchedule_SchedulerNotStarted_Returns201(t *testing.T) {
	srv, store, token, jm := buildScheduleE2EServer(t)
	// A real scheduler that is NOT started: Reload returns scheduler.ErrNotStarted.
	srv.SetJobs(jm, scheduler.New(jm, store, time.UTC))
	if err := store.CreateApp(db.CreateAppParams{Slug: "warmapp", Name: "warmapp", OwnerID: 1, Access: "private"}); err != nil {
		t.Fatal(err)
	}

	reqBody := `{"name":"warm","cron_expr":"0 5 * * *","command":["true"],"timeout_seconds":600,"overlap_policy":"skip","missed_policy":"skip"}`
	req := httptest.NewRequest("POST", "/api/apps/warmapp/schedules", strings.NewReader(reqBody))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	srv.Router().ServeHTTP(rr, req)
	if rr.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201 (ErrNotStarted must be soft-handled, not 500); body=%s", rr.Code, rr.Body.String())
	}
	app, _ := store.GetAppBySlug("warmapp")
	if id := scheduleIDBySlugAndName(t, store, app.ID, "warm"); id == 0 {
		t.Errorf("schedule row was not persisted")
	}
}

// scheduleIDBySlugAndName resolves a schedule ID by listing all schedules for
// the given app and matching by name.
func scheduleIDBySlugAndName(t *testing.T, store *db.Store, appID int64, name string) int64 {
	t.Helper()
	rows, err := store.ListSchedulesByApp(appID)
	if err != nil {
		t.Fatal(err)
	}
	for _, sc := range rows {
		if sc.Name == name {
			return sc.ID
		}
	}
	t.Fatalf("schedule %q not found in app %d", name, appID)
	return 0
}

// TestCreateSchedule_RunOnRegister_FiresOnce verifies that creating a schedule
// with run_on_register=true dispatches a first run immediately and returns the
// run id in the response as first_fire_run_id.
func TestCreateSchedule_RunOnRegister_FiresOnce(t *testing.T) {
	srv, store, token := newScheduleE2EServerWithJobs(t)
	if err := store.CreateApp(db.CreateAppParams{Slug: "warmapp", Name: "warmapp", OwnerID: 1, Access: "private"}); err != nil {
		t.Fatal(err)
	}
	app, _ := store.GetAppBySlug("warmapp")

	reqBody := `{"name":"warm","cron_expr":"0 5 * * *","command":["true"],"timeout_seconds":60,"overlap_policy":"skip","missed_policy":"skip","run_on_register":true}`
	req := httptest.NewRequest("POST", "/api/apps/warmapp/schedules", strings.NewReader(reqBody))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	srv.Router().ServeHTTP(rr, req)
	if rr.Code != http.StatusCreated {
		t.Fatalf("status = %d, body=%s", rr.Code, rr.Body.String())
	}
	var dto struct {
		ID             int64  `json:"id"`
		FirstFireRunID *int64 `json:"first_fire_run_id"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &dto); err != nil {
		t.Fatal(err)
	}
	if dto.FirstFireRunID == nil {
		// run_on_register first-fire is best-effort: maybeFirstFire returns nil
		// (and logs) when its gate sees a prior successful run or when dispatch
		// errors. Probe the DB so a recurrence pinpoints which: zero runs means
		// the dispatch never inserted; a non-ErrNotFound gate error means the
		// gate check itself failed.
		schedID := scheduleIDBySlugAndName(t, store, app.ID, "warm")
		runs, runsErr := store.ListScheduleRuns(schedID, 50, 0)
		_, gateErr := store.LastSuccessfulRun(schedID)
		t.Fatalf("first_fire_run_id is nil; status=%d body=%s; runs=%d (err=%v); gate(LastSuccessfulRun) err=%v",
			rr.Code, rr.Body.String(), len(runs), runsErr, gateErr)
	}

	schedID := scheduleIDBySlugAndName(t, store, app.ID, "warm")
	// Wait for the async run to be recorded.
	deadline := time.Now().Add(5 * time.Second)
	var hasRegister bool
	for time.Now().Before(deadline) {
		runs, _ := store.ListScheduleRuns(schedID, 50, 0)
		for _, r := range runs {
			if r.Trigger == "register" {
				hasRegister = true
			}
		}
		if hasRegister {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if !hasRegister {
		t.Errorf("no 'register' run recorded")
	}
}
