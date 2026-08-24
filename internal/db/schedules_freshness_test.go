package db_test

import (
	"testing"
	"time"

	"github.com/rvben/shinyhub/internal/db"
)

func TestScheduleFreshness(t *testing.T) {
	store := newScheduleStore(t)
	appID := newScheduleAppFixture(t, store, "alpha-dash")
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
	if fr.Slug != "alpha-dash" || fr.Name != "refresh-data" {
		t.Fatalf("slug/name = %q/%q", fr.Slug, fr.Name)
	}
	if !fr.Enabled || fr.CronExpr != "0 6 * * *" || fr.TimeoutSeconds != 3600 {
		t.Fatalf("scalar fields wrong: %+v", fr)
	}
	// Last run is the NEWER failed run (ordered by started_at DESC).
	if fr.LastRunID == nil || *fr.LastRunID != r2 || fr.LastRunStatus != "failed" ||
		fr.LastRunAt == nil || fr.LastRunAt.Unix() != base.Unix() {
		t.Fatalf("last run = %q @ %v, want failed @ %v", fr.LastRunStatus, fr.LastRunAt, base)
	}
	// Last success is the OLDER succeeded run's finished_at (only succeeded counts).
	wantSuccess := base.Add(-24 * time.Hour).Add(2 * time.Minute)
	if fr.LastSuccessAt == nil || fr.LastSuccessAt.Unix() != wantSuccess.Unix() {
		t.Fatalf("last success = %v, want %v (the succeeded run's finished_at)", fr.LastSuccessAt, wantSuccess)
	}
}

// ScheduleFreshnessByApp backs the per-app schedule list, which must not pay
// the cost of the fleet-wide scan nor expose another app's schedules. It
// carries ScheduleID so a caller can join the row onto a schedule by identity
// rather than by name.
func TestScheduleFreshnessByApp_ScopesToOneApp(t *testing.T) {
	store := newScheduleStore(t)
	mineID := newScheduleAppFixture(t, store, "mine")
	theirsID := newScheduleAppFixture(t, store, "theirs")

	mineSched, err := store.CreateSchedule(db.CreateScheduleParams{
		AppID: mineID, Name: "refresh-data", CronExpr: "0 6 * * *",
		CommandJSON: `["echo","hi"]`, Enabled: true, TimeoutSeconds: 3600,
		OverlapPolicy: "skip", MissedPolicy: "skip",
	})
	if err != nil {
		t.Fatalf("CreateSchedule mine: %v", err)
	}
	if _, err := store.CreateSchedule(db.CreateScheduleParams{
		AppID: theirsID, Name: "other-job", CronExpr: "0 7 * * *",
		CommandJSON: `["echo","hi"]`, Enabled: true, TimeoutSeconds: 3600,
		OverlapPolicy: "skip", MissedPolicy: "skip",
	}); err != nil {
		t.Fatalf("CreateSchedule theirs: %v", err)
	}

	base := time.Date(2026, 6, 29, 6, 0, 0, 0, time.UTC)
	runID, err := store.InsertScheduleRun(db.InsertScheduleRunParams{
		ScheduleID: mineSched, Status: "running", Trigger: "schedule",
		StartedAt: base, LogPath: "r.log",
	})
	if err != nil {
		t.Fatalf("insert run: %v", err)
	}
	if err := store.FinishScheduleRun(db.FinishScheduleRunParams{
		RunID: runID, Status: "succeeded", ExitCode: ptrInt(0), FinishedAt: base.Add(time.Minute),
	}); err != nil {
		t.Fatalf("finish run: %v", err)
	}

	rows, err := store.ScheduleFreshnessByApp(mineID)
	if err != nil {
		t.Fatalf("ScheduleFreshnessByApp: %v", err)
	}
	// Two bounds: my schedule present, the other app's absent. A query missing
	// its WHERE clause returns both and would pass a presence-only assertion.
	if len(rows) != 1 {
		t.Fatalf("got %d rows, want exactly 1 (the other app's schedule must not appear): %+v", len(rows), rows)
	}
	fr := rows[0]
	if fr.Slug != "mine" || fr.Name != "refresh-data" {
		t.Fatalf("row = %q/%q, want mine/refresh-data", fr.Slug, fr.Name)
	}
	if fr.ScheduleID != mineSched {
		t.Fatalf("ScheduleID = %d, want %d", fr.ScheduleID, mineSched)
	}
	if fr.LastRunStatus != "succeeded" || fr.LastSuccessAt == nil {
		t.Fatalf("freshness not resolved: %+v", fr)
	}
}

// The fleet-wide query must populate ScheduleID too, so both callers can rely
// on it. Added with the field rather than left to drift.
func TestScheduleFreshness_CarriesScheduleID(t *testing.T) {
	store := newScheduleStore(t)
	appID := newScheduleAppFixture(t, store, "solo")
	schedID, err := store.CreateSchedule(db.CreateScheduleParams{
		AppID: appID, Name: "refresh-data", CronExpr: "0 6 * * *",
		CommandJSON: `["echo","hi"]`, Enabled: true, TimeoutSeconds: 3600,
		OverlapPolicy: "skip", MissedPolicy: "skip",
	})
	if err != nil {
		t.Fatalf("CreateSchedule: %v", err)
	}
	rows, err := store.ScheduleFreshness()
	if err != nil {
		t.Fatalf("ScheduleFreshness: %v", err)
	}
	if len(rows) != 1 || rows[0].ScheduleID != schedID {
		t.Fatalf("ScheduleID not populated: %+v (want %d)", rows, schedID)
	}
}

func TestScheduleFreshness_NeverRun(t *testing.T) {
	store := newScheduleStore(t)
	appID := newScheduleAppFixture(t, store, "beta-kpi")
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
	if rows[0].LastRunID != nil || rows[0].LastRunAt != nil || rows[0].LastSuccessAt != nil || rows[0].LastRunStatus != "" {
		t.Fatalf("never-run schedule should have nil last-run/last-success, got %+v", rows[0])
	}
}

func TestScheduleFreshness_ActiveRunSurvivesNewerOverlapRecord(t *testing.T) {
	store := newScheduleStore(t)
	appID := newScheduleAppFixture(t, store, "overlap")
	schedID, err := store.CreateSchedule(db.CreateScheduleParams{
		AppID: appID, Name: "refresh", CronExpr: "* * * * *",
		CommandJSON: `["echo","hi"]`, Enabled: true, TimeoutSeconds: 600,
		OverlapPolicy: "skip", MissedPolicy: "skip",
	})
	if err != nil {
		t.Fatal(err)
	}
	activeAt := time.Now().Add(-time.Minute)
	activeID, err := store.InsertScheduleRun(db.InsertScheduleRunParams{
		ScheduleID: schedID, Status: "running", Trigger: "schedule", StartedAt: activeAt, LogPath: "active.log",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.InsertScheduleRun(db.InsertScheduleRunParams{
		ScheduleID: schedID, Status: "skipped_overlap", Trigger: "schedule", StartedAt: time.Now(), LogPath: "",
	}); err != nil {
		t.Fatal(err)
	}
	rows, err := store.ScheduleFreshness()
	if err != nil || len(rows) != 1 {
		t.Fatalf("ScheduleFreshness = %+v, %v", rows, err)
	}
	fr := rows[0]
	if fr.LastRunStatus != "skipped_overlap" {
		t.Fatalf("last status = %q, want skipped_overlap", fr.LastRunStatus)
	}
	if fr.ActiveRunID == nil || *fr.ActiveRunID != activeID || fr.ActiveRunAt == nil || fr.ActiveRunAt.Unix() != activeAt.Unix() {
		t.Fatalf("active run = id %v at %v, want %d at %v", fr.ActiveRunID, fr.ActiveRunAt, activeID, activeAt)
	}
}
