package main

import (
	"context"
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

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // maintenance still runs its prompt first pass, then exits.
	telemetry := &appLogMaintenanceMetricsSpy{}
	runMaintenance(ctx, store, process.NewManager(appsDir, process.NewNativeRuntime()), telemetry, config.MaintenanceConfig{
		AppLogRunRetentionCount: 1,
		Interval:                time.Hour,
	})

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
}
