package db_test

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/rvben/shinyhub/internal/db"
)

// scheduleRunAudit decodes the detail of every schedule_run_<status> audit row.
func scheduleRunAudits(t *testing.T, store *db.Store, status string) []map[string]any {
	t.Helper()
	events, err := store.ListAuditEvents("schedule_run_"+status, 100, 0)
	if err != nil {
		t.Fatal(err)
	}
	out := make([]map[string]any, 0, len(events))
	for _, ev := range events {
		var detail map[string]any
		if err := json.Unmarshal([]byte(ev.Detail), &detail); err != nil {
			t.Fatalf("audit detail %q is not JSON: %v", ev.Detail, err)
		}
		if ev.ResourceType != "schedule" {
			t.Fatalf("resource_type = %q, want schedule", ev.ResourceType)
		}
		out = append(out, detail)
	}
	return out
}

func createNamedSchedule(t *testing.T, store *db.Store, appID int64, name string) int64 {
	t.Helper()
	id, err := store.CreateSchedule(db.CreateScheduleParams{
		AppID: appID, Name: name, CronExpr: "0 5 * * *", CommandJSON: `["true"]`,
		Enabled: true, TimeoutSeconds: 60, OverlapPolicy: "skip", MissedPolicy: "skip",
	})
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func insertRunningRun(t *testing.T, store *db.Store, scheduleID int64, trigger string) int64 {
	t.Helper()
	id, err := store.InsertScheduleRun(db.InsertScheduleRunParams{
		ScheduleID: scheduleID, Status: "running", Trigger: trigger,
		StartedAt: time.Now().UTC(), LogPath: "x.log",
	})
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func completeRun(t *testing.T, store *db.Store, runID int64, status string, exitCode *int) *db.ScheduleRunCompletion {
	t.Helper()
	c, err := store.CompleteScheduleRun(db.CompleteScheduleRunParams{
		RunID: runID, Status: status, ExitCode: exitCode, FinishedAt: time.Now().UTC(),
	})
	if err != nil {
		t.Fatalf("complete run %d as %s: %v", runID, status, err)
	}
	return c
}

func TestCompleteScheduleRun_AuditNamesScheduleAndApp(t *testing.T) {
	store := newScheduleStore(t)
	appID := newScheduleAppFixture(t, store, "sales-report")
	schedID := createNamedSchedule(t, store, appID, "nightly-refresh")
	runID := insertRunningRun(t, store, schedID, "schedule")

	code := 3
	c := completeRun(t, store, runID, "failed", &code)
	if c.Transition == nil {
		t.Fatal("completing a running run returned no transition")
	}
	tr := *c.Transition
	if tr.AppSlug != "sales-report" || tr.ScheduleName != "nightly-refresh" || tr.ScheduleID != schedID ||
		tr.RunID != runID || tr.Status != "failed" || tr.Trigger != "schedule" ||
		tr.ExitCode == nil || *tr.ExitCode != 3 {
		t.Fatalf("transition = %+v", tr)
	}

	audits := scheduleRunAudits(t, store, "failed")
	if len(audits) != 1 {
		t.Fatalf("schedule_run_failed audit rows = %d, want 1", len(audits))
	}
	a := audits[0]
	if a["app"] != "sales-report" || a["schedule"] != "nightly-refresh" ||
		a["schedule_id"] != float64(schedID) || a["run_id"] != float64(runID) ||
		a["trigger"] != "schedule" || a["exit_code"] != float64(3) {
		t.Fatalf("audit detail = %v", a)
	}
}

func TestCompleteScheduleRun_AbsentExitCodeIsNullNotZero(t *testing.T) {
	store := newScheduleStore(t)
	appID := newScheduleAppFixture(t, store, "etl")
	schedID := createNamedSchedule(t, store, appID, "load")
	completeRun(t, store, insertRunningRun(t, store, schedID, "manual"), "timed_out", nil)

	audits := scheduleRunAudits(t, store, "timed_out")
	if len(audits) != 1 {
		t.Fatalf("schedule_run_timed_out audit rows = %d, want 1", len(audits))
	}
	v, present := audits[0]["exit_code"]
	if !present || v != nil {
		t.Fatalf("exit_code must be present and null when no exit was observed, got present=%v value=%v", present, v)
	}
}

func TestCompleteScheduleRun_RepeatedCompletionReportsOnce(t *testing.T) {
	store := newScheduleStore(t)
	appID := newScheduleAppFixture(t, store, "repeat")
	schedID := createNamedSchedule(t, store, appID, "job")
	runID := insertRunningRun(t, store, schedID, "schedule")

	zero := 0
	if c := completeRun(t, store, runID, "succeeded", &zero); c.Transition == nil {
		t.Fatal("first completion returned no transition")
	}
	// A retry after a lost commit acknowledgement, or a second writer, finds the
	// run already terminal. It must not claim a transition or audit it again.
	if c := completeRun(t, store, runID, "succeeded", &zero); c.Transition != nil {
		t.Fatalf("repeated completion reported a transition: %+v", c.Transition)
	}
	if got := len(scheduleRunAudits(t, store, "succeeded")); got != 1 {
		t.Fatalf("schedule_run_succeeded audit rows = %d, want 1", got)
	}
}

func TestCompleteScheduleRun_AuditStaysAttributableAfterScheduleDeletion(t *testing.T) {
	store := newScheduleStore(t)
	appID := newScheduleAppFixture(t, store, "gone")
	schedID := createNamedSchedule(t, store, appID, "deleted-later")
	completeRun(t, store, insertRunningRun(t, store, schedID, "schedule"), "failed", nil)
	if err := store.DeleteSchedule(schedID); err != nil {
		t.Fatal(err)
	}
	audits := scheduleRunAudits(t, store, "failed")
	if len(audits) != 1 || audits[0]["schedule"] != "deleted-later" || audits[0]["app"] != "gone" {
		t.Fatalf("audit after deletion = %v", audits)
	}
}

func TestCompleteScheduleRun_LogLineFollowsCommit(t *testing.T) {
	store := newScheduleStore(t)
	appID := newScheduleAppFixture(t, store, "logged")
	schedID := createNamedSchedule(t, store, appID, "hourly")
	runID := insertRunningRun(t, store, schedID, "schedule")

	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&buf, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })

	completeRun(t, store, runID, "failed", nil)

	var lines []map[string]any
	for _, raw := range strings.Split(strings.TrimSpace(buf.String()), "\n") {
		var rec map[string]any
		if json.Unmarshal([]byte(raw), &rec) == nil && rec["msg"] == "audit_event" {
			lines = append(lines, rec)
		}
	}
	if len(lines) != 1 {
		t.Fatalf("audit_event log lines = %d, want 1:\n%s", len(lines), buf.String())
	}
	line := lines[0]
	if line["action"] != "schedule_run_failed" || line["resource_id"] != strconv.FormatInt(schedID, 10) {
		t.Fatalf("audit_event line = %v", line)
	}
	if detail, _ := line["detail"].(string); !strings.Contains(detail, `"schedule":"hourly"`) ||
		!strings.Contains(detail, `"app":"logged"`) {
		t.Fatalf("audit_event detail does not name the schedule and app: %v", line["detail"])
	}
}

func TestMarkRunningSchedulesInterrupted_ReportsNamedTransitions(t *testing.T) {
	store := newScheduleStore(t)
	appA := newScheduleAppFixture(t, store, "app-a")
	appB := newScheduleAppFixture(t, store, "app-b")
	schedA := createNamedSchedule(t, store, appA, "extract")
	schedB := createNamedSchedule(t, store, appB, "publish")
	runA := insertRunningRun(t, store, schedA, "schedule")
	runB := insertRunningRun(t, store, schedB, "manual")
	finished := insertRunningRun(t, store, schedA, "schedule")
	completeRun(t, store, finished, "failed", nil)

	got, err := store.MarkRunningSchedulesInterrupted()
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("transitions = %+v, want the two running runs", got)
	}
	byRun := map[int64]db.ScheduleRunTransition{}
	for _, tr := range got {
		byRun[tr.RunID] = tr
	}
	if tr := byRun[runA]; tr.ScheduleName != "extract" || tr.AppSlug != "app-a" || tr.Status != "interrupted" ||
		tr.Trigger != "schedule" || tr.ExitCode != nil {
		t.Fatalf("run A transition = %+v", tr)
	}
	if tr := byRun[runB]; tr.ScheduleName != "publish" || tr.AppSlug != "app-b" || tr.Trigger != "manual" {
		t.Fatalf("run B transition = %+v", tr)
	}

	audits := scheduleRunAudits(t, store, "interrupted")
	if len(audits) != 2 {
		t.Fatalf("schedule_run_interrupted audit rows = %d, want 2", len(audits))
	}
	names := map[string]bool{}
	for _, a := range audits {
		names[a["app"].(string)+"/"+a["schedule"].(string)] = true
	}
	if !names["app-a/extract"] || !names["app-b/publish"] {
		t.Fatalf("interrupted audit names = %v", names)
	}

	// A second pass finds nothing running and reports nothing.
	again, err := store.MarkRunningSchedulesInterrupted()
	if err != nil || len(again) != 0 {
		t.Fatalf("second pass = %+v, %v", again, err)
	}
}

func TestScheduleFreshness_ConsecutiveFailuresAndLastFailure(t *testing.T) {
	store := newScheduleStore(t)
	appID := newScheduleAppFixture(t, store, "streak")
	schedID := createNamedSchedule(t, store, appID, "job")
	freshness := func() db.ScheduleFreshness {
		t.Helper()
		rows, err := store.ScheduleFreshnessByApp(appID)
		if err != nil || len(rows) != 1 {
			t.Fatalf("freshness = %+v, %v", rows, err)
		}
		return rows[0]
	}
	run := func(status string) {
		t.Helper()
		completeRun(t, store, insertRunningRun(t, store, schedID, "schedule"), status, nil)
	}

	if fr := freshness(); fr.ConsecutiveFailures != 0 || fr.LastFailureAt != nil || fr.LastInterruptedAt != nil {
		t.Fatalf("never-run schedule: consecutive=%d last_failure=%v last_interrupted=%v",
			fr.ConsecutiveFailures, fr.LastFailureAt, fr.LastInterruptedAt)
	}
	// A schedule that has never succeeded still has a well-defined streak.
	run("failed")
	if fr := freshness(); fr.ConsecutiveFailures != 1 || fr.LastFailureAt == nil || fr.LastInterruptedAt != nil {
		t.Fatalf("first failure: consecutive=%d last_failure=%v last_interrupted=%v",
			fr.ConsecutiveFailures, fr.LastFailureAt, fr.LastInterruptedAt)
	}
	run("succeeded")
	if fr := freshness(); fr.ConsecutiveFailures != 0 || fr.LastFailureAt == nil {
		t.Fatalf("after success: consecutive=%d last_failure=%v (last failure must persist)", fr.ConsecutiveFailures, fr.LastFailureAt)
	}
	// interrupted and cancelled neither count nor reset; timed_out counts.
	run("failed")
	run("interrupted")
	run("cancelled")
	run("timed_out")
	if fr := freshness(); fr.ConsecutiveFailures != 2 || fr.LastInterruptedAt == nil {
		t.Fatalf("failed+interrupted+cancelled+timed_out: consecutive=%d last_interrupted=%v, want 2 and set",
			fr.ConsecutiveFailures, fr.LastInterruptedAt)
	}
	run("succeeded")
	if fr := freshness(); fr.ConsecutiveFailures != 0 {
		t.Fatalf("after recovery: consecutive=%d, want 0", fr.ConsecutiveFailures)
	}
}

// A run transition and its audit event commit together, so an audit insert
// that fails aborts the transition. That failure must reach the same telemetry
// as every other audit write failure; otherwise a broken audit table shows up
// only as runs that never leave the running state.
func TestScheduleRunTransition_AuditInsertFailureIsReported(t *testing.T) {
	store := newScheduleStore(t)
	appID := newScheduleAppFixture(t, store, "audit-down")
	schedID := createNamedSchedule(t, store, appID, "job")
	completing := insertRunningRun(t, store, schedID, "schedule")
	inherited := insertRunningRun(t, store, schedID, "schedule")
	if _, err := store.DB().Exec(`ALTER TABLE audit_events RENAME TO unavailable_audit_events`); err != nil {
		t.Fatal(err)
	}
	failures := 0
	store.SetAuditErrorHook(func() { failures++ })
	var logs bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&logs, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })

	if _, err := store.CompleteScheduleRun(db.CompleteScheduleRunParams{
		RunID: completing, Status: "failed", FinishedAt: time.Now().UTC(),
	}); err == nil {
		t.Fatal("CompleteScheduleRun succeeded without its audit event")
	}
	if failures != 1 {
		t.Fatalf("audit error hook calls after CompleteScheduleRun = %d, want 1", failures)
	}
	if _, err := store.MarkRunningSchedulesInterrupted(); err == nil {
		t.Fatal("MarkRunningSchedulesInterrupted succeeded without its audit events")
	}
	if failures != 2 {
		t.Fatalf("audit error hook calls after MarkRunningSchedulesInterrupted = %d, want 2", failures)
	}

	for _, runID := range []int64{completing, inherited} {
		var status string
		if err := store.DB().QueryRow(`SELECT status FROM schedule_runs WHERE id = ?`, runID).Scan(&status); err != nil {
			t.Fatal(err)
		}
		if status != "running" {
			t.Fatalf("run %d status = %q, want running (transition must roll back with its audit event)", runID, status)
		}
	}
	out := logs.String()
	for _, action := range []string{"schedule_run_failed", "schedule_run_interrupted"} {
		if !strings.Contains(out, `"msg":"audit_log_write_failed","action":"`+action+`"`) {
			t.Fatalf("no audit_log_write_failed line for %s in:\n%s", action, out)
		}
	}
	if strings.Contains(out, `"msg":"audit_event"`) {
		t.Fatalf("a rolled-back transition logged audit_event:\n%s", out)
	}
}
