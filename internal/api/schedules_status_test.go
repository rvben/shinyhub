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

func TestFleetScheduleStatus_AdminOnly(t *testing.T) {
	srv, store := newFleetHealthServer(t)
	hash, _ := auth.HashPassword("pass")
	store.CreateUser(db.CreateUserParams{Username: "dev", PasswordHash: hash, Role: "developer"})
	dev, _ := store.GetUserByUsername("dev")
	devTok, _ := auth.IssueJWT(dev.ID, "dev", "developer", "test-secret")

	req := authedRequest(t, "GET", "/api/fleet/schedules/status", nil, devTok)
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("non-admin status = %d, want 403", rec.Code)
	}
}

func TestFleetScheduleStatus_StaleFlagAndAge(t *testing.T) {
	srv, store := newFleetHealthServer(t)
	hash, _ := auth.HashPassword("pass")
	store.CreateUser(db.CreateUserParams{Username: "admin", PasswordHash: hash, Role: "admin"})
	admin, _ := store.GetUserByUsername("admin")
	adminTok, _ := auth.IssueJWT(admin.ID, "admin", "admin", "test-secret")

	store.CreateApp(db.CreateAppParams{Slug: "jp-dash", Name: "jp-dash", OwnerID: admin.ID})
	app, _ := store.GetAppBySlug("jp-dash")
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
			Stale           bool   `json:"stale"`
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
	if got[0].Slug != "jp-dash" || got[0].Schedule != "refresh-data" {
		t.Fatalf("row = %+v", got[0])
	}
	if !got[0].Stale {
		t.Fatalf("a daily schedule last-succeeded 30h ago should be stale: %+v", got[0])
	}
	if got[0].LastSuccessAgeS == nil || *got[0].LastSuccessAgeS < 100000 {
		t.Fatalf("last_success_age_s = %v, want ~108000 (30h)", got[0].LastSuccessAgeS)
	}
}
