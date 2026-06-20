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
