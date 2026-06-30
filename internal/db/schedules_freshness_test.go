package db_test

import (
	"testing"
	"time"

	"github.com/rvben/shinyhub/internal/db"
)

func TestScheduleFreshness(t *testing.T) {
	store := newScheduleStore(t)
	appID := newScheduleAppFixture(t, store, "jp-dash")
	schedID, err := store.CreateSchedule(db.CreateScheduleParams{
		AppID: appID, Name: "refresh-data", CronExpr: "0 6 * * *",
		CommandJSON: `["echo","hi"]`, Enabled: true, TimeoutSeconds: 3600,
		OverlapPolicy: "skip", MissedPolicy: "skip",
	})
	if err != nil {
		t.Fatalf("CreateSchedule: %v", err)
	}

	base := time.Date(2026, 6, 29, 6, 0, 0, 0, time.UTC)
	// Older SUCCEEDED run (finished 2 min after it started).
	r1, err := store.InsertScheduleRun(db.InsertScheduleRunParams{
		ScheduleID: schedID, Status: "running", Trigger: "schedule",
		StartedAt: base.Add(-24 * time.Hour), LogPath: "r1.log",
	})
	if err != nil {
		t.Fatalf("insert r1: %v", err)
	}
	if err := store.FinishScheduleRun(db.FinishScheduleRunParams{
		RunID: r1, Status: "succeeded", ExitCode: ptrInt(0), FinishedAt: base.Add(-24 * time.Hour).Add(2 * time.Minute),
	}); err != nil {
		t.Fatalf("finish r1: %v", err)
	}
	// Newer FAILED run (the most recent run overall, by started_at).
	r2, err := store.InsertScheduleRun(db.InsertScheduleRunParams{
		ScheduleID: schedID, Status: "running", Trigger: "schedule",
		StartedAt: base, LogPath: "r2.log",
	})
	if err != nil {
		t.Fatalf("insert r2: %v", err)
	}
	if err := store.FinishScheduleRun(db.FinishScheduleRunParams{
		RunID: r2, Status: "failed", ExitCode: ptrInt(1), FinishedAt: base.Add(time.Minute),
	}); err != nil {
		t.Fatalf("finish r2: %v", err)
	}

	rows, err := store.ScheduleFreshness()
	if err != nil {
		t.Fatalf("ScheduleFreshness: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("got %d rows, want 1", len(rows))
	}
	fr := rows[0]
	if fr.Slug != "jp-dash" || fr.Name != "refresh-data" {
		t.Fatalf("slug/name = %q/%q", fr.Slug, fr.Name)
	}
	if !fr.Enabled || fr.CronExpr != "0 6 * * *" || fr.TimeoutSeconds != 3600 {
		t.Fatalf("scalar fields wrong: %+v", fr)
	}
	// Last run is the NEWER failed run (ordered by started_at DESC).
	if fr.LastRunStatus != "failed" || fr.LastRunAt == nil || fr.LastRunAt.Unix() != base.Unix() {
		t.Fatalf("last run = %q @ %v, want failed @ %v", fr.LastRunStatus, fr.LastRunAt, base)
	}
	// Last success is the OLDER succeeded run's finished_at (only succeeded counts).
	wantSuccess := base.Add(-24 * time.Hour).Add(2 * time.Minute)
	if fr.LastSuccessAt == nil || fr.LastSuccessAt.Unix() != wantSuccess.Unix() {
		t.Fatalf("last success = %v, want %v (the succeeded run's finished_at)", fr.LastSuccessAt, wantSuccess)
	}
}

func TestScheduleFreshness_NeverRun(t *testing.T) {
	store := newScheduleStore(t)
	appID := newScheduleAppFixture(t, store, "ccro-kpi")
	if _, err := store.CreateSchedule(db.CreateScheduleParams{
		AppID: appID, Name: "refresh-data", CronExpr: "0 6 * * *",
		CommandJSON: `["echo","hi"]`, Enabled: true, TimeoutSeconds: 3600,
		OverlapPolicy: "skip", MissedPolicy: "skip",
	}); err != nil {
		t.Fatalf("CreateSchedule: %v", err)
	}
	rows, err := store.ScheduleFreshness()
	if err != nil {
		t.Fatalf("ScheduleFreshness: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("got %d rows, want 1", len(rows))
	}
	if rows[0].LastRunAt != nil || rows[0].LastSuccessAt != nil || rows[0].LastRunStatus != "" {
		t.Fatalf("never-run schedule should have nil last-run/last-success, got %+v", rows[0])
	}
}
