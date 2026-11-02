package db_test

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/rvben/shinyhub/internal/db"
	"github.com/rvben/shinyhub/internal/schedulespec"
)

func TestProducerStatusRecovery(t *testing.T) {
	for _, initial := range []string{"running", "degraded", "hibernated", "stopped", "failed"} {
		t.Run(initial, func(t *testing.T) {
			store := newScheduleStore(t)
			appID := newScheduleAppFixture(t, store, "recover-"+initial)
			dep := promoteConvergenceDeployment(t, store, appID, "v1", "sha256:v1")
			schedule := createConvergenceSchedule(t, store, appID, `["producer"]`, schedulespec.DeployTriggerBundleChange)
			slug := "recover-" + initial
			if err := store.UpdateAppStatus(db.UpdateAppStatusParams{Slug: slug, Status: initial}); err != nil {
				t.Fatal(err)
			}
			failed := insertProducerRun(t, store, schedule, dep, `["producer"]`)
			finishProducerRun(t, store, failed, "failed")
			app, err := store.GetAppByID(appID)
			if err != nil {
				t.Fatal(err)
			}
			displaced := initial == "running" || initial == "hibernated" || initial == "degraded"
			if displaced && (app.Status != "failed" || !strings.Contains(app.LastError, fmt.Sprint(failed))) {
				t.Fatalf("failure status/diagnostic: %+v", app)
			}
			if displaced && app.ProducerRecoveryStatus != initial {
				t.Fatalf("recovery intent=%q want=%q", app.ProducerRecoveryStatus, initial)
			}
			// Startup must retain the original recovery intent.
			if err := store.EnforceCompatibilityQuarantines(); err != nil {
				t.Fatal(err)
			}
			repair := insertProducerRun(t, store, schedule, dep, `["producer"]`)
			finishProducerRun(t, store, repair, "succeeded")
			finishProducerRun(t, store, repair, "succeeded") // idempotent completion
			app, err = store.GetAppByID(appID)
			if err != nil || app.Status != initial || (displaced && app.LastError != "") {
				t.Fatalf("repair app=%+v err=%v", app, err)
			}
			var obligationStatus string
			if err := store.DB().QueryRow(`SELECT status FROM schedule_deploy_obligations WHERE schedule_id = ? AND deployment_id = ?`, schedule, dep.ID).Scan(&obligationStatus); err != nil || obligationStatus != "satisfied" {
				t.Fatalf("repair obligation status=%s err=%v", obligationStatus, err)
			}
			rows, err := store.DB().Query(`SELECT detail FROM audit_events WHERE action='app_status_changed' ORDER BY id`)
			if err != nil {
				t.Fatal(err)
			}
			defer rows.Close()
			var details []map[string]any
			for rows.Next() {
				var raw string
				if err := rows.Scan(&raw); err != nil {
					t.Fatal(err)
				}
				var detail map[string]any
				if err := json.Unmarshal([]byte(raw), &detail); err != nil {
					t.Fatal(err)
				}
				details = append(details, detail)
			}
			if err := rows.Err(); err != nil {
				t.Fatal(err)
			}
			want := 0
			if displaced {
				want = 2
			}
			if len(details) != want {
				t.Fatalf("transitions=%v want %d", details, want)
			}
			if displaced && (details[0]["from"] != initial || details[0]["to"] != "failed" || details[0]["schedule_run_id"] != float64(failed) || details[1]["reason"] != "producer_repair_succeeded") {
				t.Fatalf("transition provenance=%v", details)
			}
		})
	}
}

func TestProducerStatusRecovery_IndependentBlockersAndOperatorStop(t *testing.T) {
	for _, stop := range []bool{false, true} {
		t.Run(fmt.Sprint(stop), func(t *testing.T) {
			store := newScheduleStore(t)
			appID := newScheduleAppFixture(t, store, "multi-repair")
			dep := promoteConvergenceDeployment(t, store, appID, "v1", "sha256:v1")
			first := createConvergenceSchedule(t, store, appID, `["first"]`, schedulespec.DeployTriggerBundleChange)
			second, err := store.CreateSchedule(db.CreateScheduleParams{AppID: appID, Name: "second", CronExpr: "0 6 * * *", CommandJSON: `["second"]`, Enabled: true, DeployTrigger: schedulespec.DeployTriggerBundleChange, OnSuccess: "none", RollFallback: "defer"})
			if err != nil {
				t.Fatal(err)
			}
			if err := store.UpdateAppStatus(db.UpdateAppStatusParams{Slug: "multi-repair", Status: "running"}); err != nil {
				t.Fatal(err)
			}
			finishProducerRun(t, store, insertProducerRun(t, store, first, dep, `["first"]`), "failed")
			finishProducerRun(t, store, insertProducerRun(t, store, second, dep, `["second"]`), "failed")
			finishProducerRun(t, store, insertProducerRun(t, store, first, dep, `["first"]`), "succeeded")
			app, _ := store.GetAppByID(appID)
			if app.Status != "failed" {
				t.Fatalf("first repair cleared second failure: %+v", app)
			}
			if stop {
				if err := store.UpdateAppStatus(db.UpdateAppStatusParams{Slug: app.Slug, Status: "stopped"}); err != nil {
					t.Fatal(err)
				}
			}
			finishProducerRun(t, store, insertProducerRun(t, store, second, dep, `["second"]`), "succeeded")
			app, _ = store.GetAppByID(appID)
			want := "running"
			if stop {
				want = "stopped"
			}
			if app.Status != want {
				t.Fatalf("app status=%s want %s", app.Status, want)
			}
		})
	}
}

func TestProducerStatusRecovery_DeploymentBarrierBlocksRepair(t *testing.T) {
	store := newScheduleStore(t)
	appID := newScheduleAppFixture(t, store, "deployment-blocker")
	dep := promoteConvergenceDeployment(t, store, appID, "v1", "sha256:v1")
	schedule := createConvergenceSchedule(t, store, appID, `["producer"]`, schedulespec.DeployTriggerBundleChange)
	if err := store.UpdateAppStatus(db.UpdateAppStatusParams{Slug: "deployment-blocker", Status: "running"}); err != nil {
		t.Fatal(err)
	}
	finishProducerRun(t, store, insertProducerRun(t, store, schedule, dep, `["producer"]`), "failed")
	candidate, err := store.BeginDeployment(appID, "v2", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := store.MarkDeploymentProducerBarrierEntered(candidate.ID); err != nil {
		t.Fatal(err)
	}
	finishProducerRun(t, store, insertProducerRun(t, store, schedule, dep, `["producer"]`), "succeeded")
	app, err := store.GetAppByID(appID)
	if err != nil || app.Status != "failed" {
		t.Fatalf("deployment barrier bypassed: app=%+v err=%v", app, err)
	}
}

func TestProducerStatusRecovery_AuditFailureRollsBackCompletion(t *testing.T) {
	store := newScheduleStore(t)
	appID := newScheduleAppFixture(t, store, "audit-rollback")
	dep := promoteConvergenceDeployment(t, store, appID, "v1", "sha256:v1")
	schedule := createConvergenceSchedule(t, store, appID, `["producer"]`, schedulespec.DeployTriggerBundleChange)
	if err := store.UpdateAppStatus(db.UpdateAppStatusParams{Slug: "audit-rollback", Status: "running"}); err != nil {
		t.Fatal(err)
	}
	runID := insertProducerRun(t, store, schedule, dep, `["producer"]`)
	// Force the transition audit insert to fail after terminal state and
	// uncertainty were written, so all three must roll back together.
	if _, err := store.DB().Exec(`ALTER TABLE audit_events RENAME TO unavailable_audit_events`); err != nil {
		t.Fatal(err)
	}
	_, err := store.CompleteScheduleRunAndEnqueueActivation(db.CompleteScheduleRunParams{RunID: runID, Status: "failed", FinishedAt: time.Now().UTC(), DataWriteAttempted: true})
	if err == nil {
		t.Fatal("completion unexpectedly succeeded without an audit")
	}
	app, err := store.GetAppByID(appID)
	if err != nil || app.Status != "running" || app.LastError != "" {
		t.Fatalf("status escaped rollback: app=%+v err=%v", app, err)
	}
	run, err := store.GetScheduleRun(runID)
	if err != nil || run.Status != "running" {
		t.Fatalf("terminal state escaped rollback: run=%+v err=%v", run, err)
	}
	var uncertainty int
	if err := store.DB().QueryRow(`SELECT COUNT(*) FROM schedule_data_uncertainty WHERE schedule_id=?`, schedule).Scan(&uncertainty); err != nil || uncertainty != 0 {
		t.Fatalf("uncertainty escaped rollback: count=%d err=%v", uncertainty, err)
	}
}

func TestProducerStatusRecovery_StartupRestoresAfterLastBlockerClears(t *testing.T) {
	store := newScheduleStore(t)
	appID := newScheduleAppFixture(t, store, "startup-repair")
	dep := promoteConvergenceDeployment(t, store, appID, "v1", "sha256:v1")
	schedule := createConvergenceSchedule(t, store, appID, `["producer"]`, schedulespec.DeployTriggerBundleChange)
	if err := store.UpdateAppStatus(db.UpdateAppStatusParams{Slug: "startup-repair", Status: "running"}); err != nil {
		t.Fatal(err)
	}
	finishProducerRun(t, store, insertProducerRun(t, store, schedule, dep, `["producer"]`), "failed")
	candidate, err := store.BeginDeployment(appID, "v2", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := store.MarkDeploymentProducerBarrierEntered(candidate.ID); err != nil {
		t.Fatal(err)
	}
	finishProducerRun(t, store, insertProducerRun(t, store, schedule, dep, `["producer"]`), "succeeded")
	if err := store.PromoteDeployment(candidate.ID); err != nil {
		t.Fatal(err)
	}
	if err := store.EnforceCompatibilityQuarantines(); err != nil {
		t.Fatal(err)
	}
	app, err := store.GetAppByID(appID)
	if err != nil || app.Status != "running" {
		t.Fatalf("cleared blocker remained failed: app=%+v err=%v", app, err)
	}
}

func TestProducerStatusRecovery_DeletingWinsStartupQuarantine(t *testing.T) {
	store := newScheduleStore(t)
	appID := newScheduleAppFixture(t, store, "deleting-producer")
	dep := promoteConvergenceDeployment(t, store, appID, "v1", "sha256:v1")
	schedule := createConvergenceSchedule(t, store, appID, `["producer"]`, schedulespec.DeployTriggerBundleChange)
	if err := store.UpdateAppStatus(db.UpdateAppStatusParams{Slug: "deleting-producer", Status: "deleting"}); err != nil {
		t.Fatal(err)
	}
	finishProducerRun(t, store, insertProducerRun(t, store, schedule, dep, `["producer"]`), "failed")
	if err := store.EnforceCompatibilityQuarantines(); err != nil {
		t.Fatal(err)
	}
	app, err := store.GetAppByID(appID)
	if err != nil || app.Status != "deleting" {
		t.Fatalf("deletion lost: app=%+v err=%v", app, err)
	}
}

func TestProducerStatusRecovery_InterruptedWriterAcrossRestart(t *testing.T) {
	// A SQLite file exercises actual close/reopen and startup terminalization.
	// Other tests in this file exercise the same transition queries on Postgres.
	path := filepath.Join(t.TempDir(), "restart.db")
	open := func() *db.Store {
		t.Helper()
		s, err := db.Open(path)
		if err != nil {
			t.Fatal(err)
		}
		if err := s.Migrate(); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = s.Close() })
		return s
	}
	store := open()
	appID := newScheduleAppFixture(t, store, "interrupted-producer")
	dep := promoteConvergenceDeployment(t, store, appID, "v1", "sha256:v1")
	schedule := createConvergenceSchedule(t, store, appID, `["producer"]`, schedulespec.DeployTriggerBundleChange)
	if err := store.UpdateAppStatus(db.UpdateAppStatusParams{Slug: "interrupted-producer", Status: "running"}); err != nil {
		t.Fatal(err)
	}
	insertProducerRun(t, store, schedule, dep, `["producer"]`)
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store = open()
	if got, err := store.MarkRunningSchedulesInterrupted(); err != nil || len(got) != 1 {
		t.Fatalf("startup interruption transitions=%+v err=%v", got, err)
	}
	if err := store.EnforceCompatibilityQuarantines(); err != nil {
		t.Fatal(err)
	}
	app, err := store.GetAppByID(appID)
	if err != nil || app.Status != "failed" || app.LastError == "" {
		t.Fatalf("startup quarantine app=%+v err=%v", app, err)
	}
	// Restart once more while failed, ensuring the saved lifecycle intent is
	// persisted rather than inferred from in-memory state or the last run.
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store = open()
	if err := store.EnforceCompatibilityQuarantines(); err != nil {
		t.Fatal(err)
	}
	finishProducerRun(t, store, insertProducerRun(t, store, schedule, dep, `["producer"]`), "succeeded")
	app, err = store.GetAppByID(appID)
	if err != nil || app.Status != "running" || app.LastError != "" {
		t.Fatalf("restart repair app=%+v err=%v", app, err)
	}
}

func TestProducerStatusRecovery_ConcurrentCompletions(t *testing.T) {
	store := newScheduleStore(t)
	appID := newScheduleAppFixture(t, store, "concurrent-producers")
	dep := promoteConvergenceDeployment(t, store, appID, "v1", "sha256:v1")
	first := createConvergenceSchedule(t, store, appID, `["first"]`, schedulespec.DeployTriggerBundleChange)
	second, err := store.CreateSchedule(db.CreateScheduleParams{AppID: appID, Name: "second", CronExpr: "0 6 * * *", CommandJSON: `["second"]`, Enabled: true, DeployTrigger: schedulespec.DeployTriggerBundleChange, OnSuccess: "none", RollFallback: "defer"})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.UpdateAppStatus(db.UpdateAppStatusParams{Slug: "concurrent-producers", Status: "running"}); err != nil {
		t.Fatal(err)
	}
	failed := insertProducerRun(t, store, first, dep, `["first"]`)
	succeeded := insertProducerRun(t, store, second, dep, `["second"]`)
	start := make(chan struct{})
	results := make(chan error, 2)
	for _, completion := range []db.CompleteScheduleRunParams{
		{RunID: failed, Status: "failed", FinishedAt: time.Now().UTC(), DataWriteAttempted: true},
		{RunID: succeeded, Status: "succeeded", FinishedAt: time.Now().UTC(), DataWriteAttempted: true},
	} {
		go func(p db.CompleteScheduleRunParams) {
			<-start
			_, err := store.CompleteScheduleRunAndEnqueueActivation(p)
			results <- err
		}(completion)
	}
	close(start)
	for range 2 {
		if err := <-results; err != nil {
			t.Fatal(err)
		}
	}
	app, err := store.GetAppByID(appID)
	if err != nil || app.Status != "failed" {
		t.Fatalf("concurrent success masked failure: app=%+v err=%v", app, err)
	}
	finishProducerRun(t, store, insertProducerRun(t, store, first, dep, `["first"]`), "succeeded")
	app, err = store.GetAppByID(appID)
	if err != nil || app.Status != "running" {
		t.Fatalf("concurrent repair app=%+v err=%v", app, err)
	}
	var transitions int
	if err := store.DB().QueryRow(`SELECT COUNT(*) FROM audit_events WHERE action='app_status_changed'`).Scan(&transitions); err != nil || transitions != 2 {
		t.Fatalf("concurrent transition count=%d err=%v", transitions, err)
	}
}
