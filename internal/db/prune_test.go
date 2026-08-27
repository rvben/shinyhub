package db_test

import (
	"testing"
	"time"

	"github.com/rvben/shinyhub/internal/db"
)

// TestPruneAuditEvents deletes only events older than the retention window so
// the compliance trail stays bounded without dropping recent history.
func TestPruneAuditEvents(t *testing.T) {
	store := openTestStore(t)

	// One ancient event (well outside any sane retention) and one fresh event.
	if _, err := store.DB().Exec(
		`INSERT INTO audit_events (action, resource_type, resource_id, detail, ip_address, created_at)
		 VALUES ('old', 'app', 'x', '', '', '2000-01-01 00:00:00')`); err != nil {
		t.Fatalf("insert old event: %v", err)
	}
	store.LogAuditEvent(db.AuditEventParams{Action: "fresh", ResourceType: "app", ResourceID: "y"})

	// A zero retention is a no-op: nothing is deleted.
	if n, err := store.PruneAuditEvents(0); err != nil || n != 0 {
		t.Fatalf("PruneAuditEvents(0) = (%d, %v), want (0, nil)", n, err)
	}

	deleted, err := store.PruneAuditEvents(24 * time.Hour)
	if err != nil {
		t.Fatalf("PruneAuditEvents: %v", err)
	}
	if deleted != 1 {
		t.Fatalf("PruneAuditEvents deleted %d, want 1 (only the ancient event)", deleted)
	}

	total, err := store.CountAuditEvents("")
	if err != nil {
		t.Fatal(err)
	}
	if total != 1 {
		t.Fatalf("audit_events count = %d after prune, want 1 (fresh event survives)", total)
	}
}

// TestPruneScheduleRuns keeps the newest N runs per schedule and drops older
// ones so run history cannot grow without bound.
func TestPruneScheduleRuns(t *testing.T) {
	store := openTestStore(t)
	u := mustCreateUser(t, store, "owner", "developer")
	app := mustCreateApp(t, store, "app", u.ID)
	schedID, err := store.CreateSchedule(db.CreateScheduleParams{
		AppID: app.ID, Name: "nightly", CronExpr: "0 0 * * *",
		CommandJSON: `["python","run.py"]`, Enabled: true, TimeoutSeconds: 600,
		OverlapPolicy: "skip", MissedPolicy: "run_once",
	})
	if err != nil {
		t.Fatalf("create schedule: %v", err)
	}

	// Five completed runs, oldest first.
	for i := 0; i < 5; i++ {
		ts := time.Date(2020, 1, 1+i, 0, 0, 0, 0, time.UTC).Format("2006-01-02 15:04:05")
		if _, err := store.DB().Exec(
			`INSERT INTO schedule_runs (schedule_id, trigger, status, started_at)
			 VALUES (?, 'cron', 'success', ?)`, schedID, ts); err != nil {
			t.Fatalf("insert run %d: %v", i, err)
		}
	}

	deleted, err := store.PruneScheduleRuns(schedID, 2)
	if err != nil {
		t.Fatalf("PruneScheduleRuns: %v", err)
	}
	if deleted != 3 {
		t.Fatalf("PruneScheduleRuns deleted %d, want 3 (keep newest 2 of 5)", deleted)
	}

	runs, err := store.ListScheduleRuns(schedID, 100, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 2 {
		t.Fatalf("remaining runs = %d, want 2", len(runs))
	}
}

func TestPruneScheduleActivations_PreservesWorkDamperAnchorAndAttribution(t *testing.T) {
	store := openTestStore(t)
	u := mustCreateUser(t, store, "activation-owner", "developer")
	app := mustCreateApp(t, store, "activation-retention", u.ID)
	scheduleID, err := store.CreateSchedule(db.CreateScheduleParams{
		AppID: app.ID, Name: "refresh", CronExpr: "0 * * * *",
		CommandJSON: `["true"]`, Enabled: true, TimeoutSeconds: 60,
		OverlapPolicy: "skip", MissedPolicy: "skip", OnSuccess: "roll",
	})
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now().UTC().Add(-time.Hour).Truncate(time.Second)
	var activationIDs, runIDs []int64
	statuses := []string{"succeeded", "failed", "failed", "failed"}
	for i, status := range statuses {
		at := start.Add(time.Duration(i) * time.Minute)
		runID := insertActivationRun(t, store, scheduleID, at, "roll", 0)
		runIDs = append(runIDs, runID)
		created, err := store.CompleteScheduleRunAndEnqueueActivation(db.CompleteScheduleRunParams{
			RunID: runID, Status: "succeeded", ExitCode: intPtr(0), FinishedAt: at,
		})
		if err != nil {
			t.Fatal(err)
		}
		activationIDs = append(activationIDs, created.ID)
		claimed, err := store.ClaimNextScheduleActivation(at)
		if err != nil {
			t.Fatal(err)
		}
		if err := store.FinishScheduleActivation(claimed.ID, status, "", at.Add(time.Second), true); err != nil {
			t.Fatal(err)
		}
	}
	pendingAt := start.Add(4 * time.Minute)
	pendingRun := insertActivationRun(t, store, scheduleID, pendingAt, "roll", 0)
	pending, err := store.CompleteScheduleRunAndEnqueueActivation(db.CompleteScheduleRunParams{
		RunID: pendingRun, Status: "succeeded", ExitCode: intPtr(0), FinishedAt: pendingAt,
	})
	if err != nil {
		t.Fatal(err)
	}
	runIDs = append(runIDs, pendingRun)

	deleted, err := store.PruneScheduleActivations(2)
	if err != nil {
		t.Fatal(err)
	}
	if deleted != 1 {
		t.Fatalf("deleted activations=%d, want 1", deleted)
	}
	for _, id := range []int64{activationIDs[0], activationIDs[2], activationIDs[3], pending.ID} {
		if _, err := store.GetScheduleActivation(id); err != nil {
			t.Fatalf("retained activation %d: %v", id, err)
		}
	}
	if _, err := store.GetScheduleActivation(activationIDs[1]); err != db.ErrNotFound {
		t.Fatalf("pruned activation %d err=%v, want ErrNotFound", activationIDs[1], err)
	}

	deletedRuns, err := store.PruneScheduleRuns(scheduleID, 1)
	if err != nil {
		t.Fatal(err)
	}
	if deletedRuns != 1 {
		t.Fatalf("deleted runs=%d, want only the source of the pruned activation", deletedRuns)
	}
	for _, index := range []int{0, 2, 3, 4} {
		if _, err := store.GetScheduleRun(runIDs[index]); err != nil {
			t.Fatalf("attributed run %d: %v", runIDs[index], err)
		}
	}
}
