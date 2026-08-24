package api_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/rvben/shinyhub/internal/auth"
	"github.com/rvben/shinyhub/internal/db"
)

// The role gate is covered by TestFleetScheduleStatus_OperatorCanRead and
// TestFleetScheduleStatus_BelowOperatorForbidden in schedules_status_access_test.go.

func TestFleetScheduleStatus_StaleFlagAndAge(t *testing.T) {
	srv, store := newFleetHealthServer(t)
	hash, _ := testHashPassword("pass")
	store.CreateUser(db.CreateUserParams{Username: "admin", PasswordHash: hash, Role: "admin"})
	admin, _ := store.GetUserByUsername("admin")
	adminTok, _ := auth.IssueJWT(admin.ID, "admin", "admin", "test-secret")

	store.CreateApp(db.CreateAppParams{Slug: "alpha-dash", Name: "alpha-dash", OwnerID: admin.ID})
	app, _ := store.GetAppBySlug("alpha-dash")
	schedID, err := store.CreateSchedule(db.CreateScheduleParams{
		AppID: app.ID, Name: "refresh-data", CronExpr: "0 6 * * *",
		CommandJSON: `["echo","hi"]`, Enabled: true, TimeoutSeconds: 3600,
		OverlapPolicy: "skip", MissedPolicy: "skip",
	})
	if err != nil {
		t.Fatalf("CreateSchedule: %v", err)
	}
	// Last success ~30h ago -> a daily schedule is now stale.
	old := time.Now().Add(-30 * time.Hour)
	runID, err := store.InsertScheduleRun(db.InsertScheduleRunParams{
		ScheduleID: schedID, Status: "running", Trigger: "schedule", StartedAt: old, LogPath: "x.log",
	})
	if err != nil {
		t.Fatalf("InsertScheduleRun: %v", err)
	}
	exit := 0
	if err := store.FinishScheduleRun(db.FinishScheduleRunParams{
		RunID: runID, Status: "succeeded", ExitCode: &exit, FinishedAt: old.Add(time.Minute),
	}); err != nil {
		t.Fatalf("FinishScheduleRun: %v", err)
	}

	req := authedRequest(t, "GET", "/api/fleet/schedules/status", nil, adminTok)
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var env struct {
		Items []struct {
			Slug            string `json:"slug"`
			Schedule        string `json:"schedule"`
			LastRunID       *int64 `json:"last_run_id"`
			Stale           bool   `json:"stale"`
			Refreshing      bool   `json:"refreshing"`
			LastRunStatus   string `json:"last_run_status"`
			LastSuccessAgeS *int64 `json:"last_success_age_s"`
		} `json:"items"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&env); err != nil {
		t.Fatalf("decode: %v", err)
	}
	got := env.Items
	if len(got) != 1 {
		t.Fatalf("got %d rows, want 1: %+v", len(got), got)
	}
	if got[0].Slug != "alpha-dash" || got[0].Schedule != "refresh-data" {
		t.Fatalf("row = %+v", got[0])
	}
	if !got[0].Stale {
		t.Fatalf("a daily schedule last-succeeded 30h ago should be stale: %+v", got[0])
	}
	if got[0].LastRunID == nil || *got[0].LastRunID != runID {
		t.Fatalf("last_run_id = %v, want %d", got[0].LastRunID, runID)
	}
	if got[0].Refreshing {
		t.Fatalf("refreshing = true for a terminal run: %+v", got[0])
	}
	if got[0].LastSuccessAgeS == nil || *got[0].LastSuccessAgeS < 100000 {
		t.Fatalf("last_success_age_s = %v, want ~108000 (30h)", got[0].LastSuccessAgeS)
	}
}
