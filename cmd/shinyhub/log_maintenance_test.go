package main

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/rvben/shinyhub/internal/config"
	"github.com/rvben/shinyhub/internal/db"
	"github.com/rvben/shinyhub/internal/dbtest"
	"github.com/rvben/shinyhub/internal/process"
)

type appLogMaintenanceMetricsSpy struct {
	runs  int64
	files int
}

func (m *appLogMaintenanceMetricsSpy) RecordAppLogRunsPruned(count int64) { m.runs += count }
func (m *appLogMaintenanceMetricsSpy) RecordAppLogFilesPruned(count int)  { m.files += count }

func TestRunMaintenancePrunesDatabaseBeforeLocalLogFiles(t *testing.T) {
	store := dbtest.New(t)
	if err := store.CreateUser(db.CreateUserParams{Username: "owner", PasswordHash: "hash", Role: "developer"}); err != nil {
		t.Fatal(err)
	}
	owner, _ := store.GetUserByUsername("owner")
	if _, err := store.CreateApp(db.CreateAppParams{Slug: "demo", Name: "Demo", OwnerID: owner.ID}); err != nil {
		t.Fatal(err)
	}
	app, _ := store.GetAppBySlug("demo")
	appsDir := t.TempDir()
	logDir := filepath.Join(appsDir, "demo", "logs")
	if err := os.MkdirAll(logDir, 0o750); err != nil {
		t.Fatal(err)
	}
	runIDs := []string{
		"90000000-0000-4000-8000-000000000001",
		"90000000-0000-4000-8000-000000000002",
		"90000000-0000-4000-8000-000000000003",
	}
	base := time.Unix(1_700_000_000, 0)
	for i, runID := range runIDs {
		started := base.Add(time.Duration(i) * time.Minute)
		if err := store.CreateAppLogRun(db.CreateAppLogRunParams{
			RunID: runID, AppID: app.ID, ReplicaIndex: 0, Status: "starting", StartedAt: started,
		}); err != nil {
			t.Fatal(err)
		}
		if err := store.FinishAppLogRun(runID, "stopped", started.Add(time.Second), false); err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(logDir, "replica-0-"+runID+".log")
		if err := os.WriteFile(path, []byte(runID+"\n"), 0o640); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.BeginUsageSession(db.UsageSessionStart{
		ID: "expired-while-disabled", Slug: app.Slug, InstanceID: "cp",
		StartedAt: time.Now().UTC().Add(-48 * time.Hour),
	}); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // maintenance still runs its prompt first pass, then exits.
	telemetry := &appLogMaintenanceMetricsSpy{}
	runMaintenance(ctx, store, process.NewManager(appsDir, process.NewNativeRuntime()), telemetry, config.MaintenanceConfig{
		AppLogRunRetentionCount: 1,
		Interval:                time.Hour,
	}, config.UsageConfig{Enabled: false, RawRetentionDays: 1, AggregateRetentionDays: 365})

	runs, err := store.ListAppLogRuns(app.ID, 100)
	if err != nil || len(runs) != 1 || runs[0].RunID != runIDs[2] {
		t.Fatalf("retained runs = %+v, %v", runs, err)
	}
	for i, runID := range runIDs {
		path := filepath.Join(logDir, "replica-0-"+runID+".log")
		_, err := os.Stat(path)
		if i < 2 && !os.IsNotExist(err) {
			t.Errorf("old local run still exists: %s", path)
		}
		if i == 2 && err != nil {
			t.Errorf("newest local run removed: %v", err)
		}
	}
	if telemetry.runs != 2 || telemetry.files != 2 {
		t.Fatalf("maintenance metrics = runs:%d files:%d, want runs:2 files:2", telemetry.runs, telemetry.files)
	}
	var usageRows int
	if err := store.DB().QueryRow(`SELECT COUNT(*) FROM usage_sessions`).Scan(&usageRows); err != nil {
		t.Fatal(err)
	}
	if usageRows != 0 {
		t.Fatalf("disabled usage retained %d expired rows", usageRows)
	}
}

// TestRunMaintenancePrunesFleetRunsAndDevelopmentSessions proves runMaintenance
// actually invokes the fleet run and development session prune knobs when
// configured, not just that the underlying Store methods work in isolation.
func TestRunMaintenancePrunesFleetRunsAndDevelopmentSessions(t *testing.T) {
	store := dbtest.New(t)
	if err := store.CreateUser(db.CreateUserParams{Username: "owner2", PasswordHash: "hash", Role: "developer"}); err != nil {
		t.Fatal(err)
	}
	owner, _ := store.GetUserByUsername("owner2")
	if _, err := store.CreateApp(db.CreateAppParams{Slug: "fleet-demo", Name: "Fleet Demo", OwnerID: owner.ID}); err != nil {
		t.Fatal(err)
	}
	app, _ := store.GetAppBySlug("fleet-demo")

	for i, id := range []string{"run-old", "run-new"} {
		if _, _, err := store.CreateFleetRun(db.CreateFleetRunParams{
			ID: id, FleetID: "acme", Kind: "fleet_apply",
		}); err != nil {
			t.Fatalf("create fleet run %d: %v", i, err)
		}
		if err := store.FinishFleetRun(id, "succeeded", 0, ""); err != nil {
			t.Fatalf("finish fleet run %d: %v", i, err)
		}
	}

	if err := store.UpsertDevelopmentSession(db.UpsertDevelopmentSessionParams{
		ID: "sess-old", AppID: app.ID, TargetKind: db.DevelopmentTargetExisting,
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.EndDevelopmentSession(app.ID, "sess-old", time.Now().UTC().Add(-100*24*time.Hour)); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // maintenance still runs its prompt first pass, then exits.
	runMaintenance(ctx, store, nil, nil, config.MaintenanceConfig{
		FleetRunRetentionCount:          1,
		DevelopmentSessionRetentionDays: 30,
		Interval:                        time.Hour,
	}, config.UsageConfig{Enabled: false})

	if _, err := store.GetFleetRun("run-old"); err != db.ErrNotFound {
		t.Errorf("expected run-old to be pruned, got %v", err)
	}
	if _, err := store.GetFleetRun("run-new"); err != nil {
		t.Errorf("expected run-new to survive prune, got %v", err)
	}
	if _, err := store.GetDevelopmentSession(app.ID, "sess-old"); err != db.ErrNotFound {
		t.Errorf("expected sess-old to be pruned, got %v", err)
	}
}

// TestRunMaintenanceFinalizesStaleUsageSessionsIndependentOfRetention proves
// runMaintenance closes out a crashed (heartbeat-stale) usage session even
// when both usage retention knobs are disabled. Without independent
// finalization, a session that never gets ended_at set stays in the
// usage_closed_daily fast path's live re-scan forever whenever an operator
// tracks usage but never enables retention.
func TestRunMaintenanceFinalizesStaleUsageSessionsIndependentOfRetention(t *testing.T) {
	store := dbtest.New(t)
	if err := store.CreateUser(db.CreateUserParams{Username: "owner3", PasswordHash: "hash", Role: "developer"}); err != nil {
		t.Fatal(err)
	}
	owner, _ := store.GetUserByUsername("owner3")
	if _, err := store.CreateApp(db.CreateAppParams{Slug: "usage-demo", Name: "Usage Demo", OwnerID: owner.ID}); err != nil {
		t.Fatal(err)
	}

	// started_at (and therefore the initial heartbeat_at) is far enough in the
	// past to be stale, but well inside any retention window would-be enforce -
	// retention is disabled below, so only heartbeat staleness should matter.
	if err := store.BeginUsageSession(db.UsageSessionStart{
		ID: "crashed-1", Slug: "usage-demo", InstanceID: "cp",
		StartedAt: time.Now().UTC().Add(-5 * time.Minute),
	}); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // maintenance still runs its prompt first pass, then exits.
	runMaintenance(ctx, store, nil, nil, config.MaintenanceConfig{
		Interval: time.Hour,
	}, config.UsageConfig{Enabled: true, RawRetentionDays: 0, AggregateRetentionDays: 0})

	var endedAt sql.NullTime
	if err := store.DB().QueryRow(`SELECT ended_at FROM usage_sessions WHERE id = ?`, "crashed-1").Scan(&endedAt); err != nil {
		t.Fatal(err)
	}
	if !endedAt.Valid {
		t.Fatal("expected crashed-1 to be finalized (ended_at set) despite retention being disabled")
	}
}
