package db_test

import (
	"errors"
	"testing"
	"time"

	"github.com/rvben/shinyhub/internal/db"
	"github.com/rvben/shinyhub/internal/schedulespec"
)

func promoteConvergenceDeployment(t *testing.T, store *db.Store, appID int64, version, digest string) *db.Deployment {
	t.Helper()
	dep, err := store.BeginDeployment(appID, version, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SetDeploymentDigest(dep.ID, digest); err != nil {
		t.Fatal(err)
	}
	if err := store.PromoteDeployment(dep.ID); err != nil {
		t.Fatal(err)
	}
	dep.Status = db.DeploymentSucceeded
	dep.ContentDigest = digest
	return dep
}

func createConvergenceSchedule(t *testing.T, store *db.Store, appID int64, command, policy string) int64 {
	t.Helper()
	id, err := store.CreateSchedule(db.CreateScheduleParams{
		AppID: appID, Name: "producer", CronExpr: "0 5 * * *", CommandJSON: command,
		Enabled: true, TimeoutSeconds: 60, OverlapPolicy: "skip", MissedPolicy: "skip",
		DeployTrigger: policy, OnSuccess: "none", RollFallback: "defer",
	})
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func insertProducerRun(t *testing.T, store *db.Store, scheduleID int64, dep *db.Deployment, command string) int64 {
	t.Helper()
	canonical, fingerprint, err := schedulespec.ProducerIdentity(command)
	if err != nil {
		t.Fatal(err)
	}
	deploymentID := dep.ID
	runID, err := store.InsertScheduleRun(db.InsertScheduleRunParams{
		ScheduleID: scheduleID, Status: "running", Trigger: "deploy", StartedAt: time.Now().UTC(),
		OnSuccess: "none", RollFallback: "defer", DeploymentID: &deploymentID,
		AppVersion: dep.Version, ContentDigest: dep.ContentDigest,
		ProducerFingerprint: fingerprint, ProducerCommandJSON: canonical,
		PublishesData: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	return runID
}

func finishProducerRun(t *testing.T, store *db.Store, runID int64, status string) {
	t.Helper()
	finishProducerRunAt(t, store, runID, status, time.Now().UTC())
}

func finishProducerRunAt(t *testing.T, store *db.Store, runID int64, status string, finishedAt time.Time) {
	t.Helper()
	zero := 0
	var exitCode *int
	if status == "succeeded" {
		exitCode = &zero
	}
	if _, err := store.CompleteScheduleRunAndEnqueueActivation(db.CompleteScheduleRunParams{
		RunID: runID, Status: status, ExitCode: exitCode, FinishedAt: finishedAt,
		DataWriteAttempted: true,
	}); err != nil {
		t.Fatal(err)
	}
}

func TestCompatibilityQuarantine_TracksUncertainPublisherUntilNewSuccess(t *testing.T) {
	store := newScheduleStore(t)
	appID := newScheduleAppFixture(t, store, "uncertain-writer")
	dep := promoteConvergenceDeployment(t, store, appID, "v1", "sha256:v1")
	scheduleID := createConvergenceSchedule(t, store, appID, `["producer"]`, schedulespec.DeployTriggerBundleChange)

	first := insertProducerRun(t, store, scheduleID, dep, `["producer"]`)
	finishProducerRun(t, store, first, "succeeded")
	if quarantined, err := store.AppCompatibilityQuarantined(appID); err != nil || quarantined {
		t.Fatalf("after successful publication quarantined=%v err=%v", quarantined, err)
	}

	uncertain := insertProducerRun(t, store, scheduleID, dep, `["producer"]`)
	if quarantined, err := store.AppCompatibilityQuarantined(appID); err != nil || !quarantined {
		t.Fatalf("while physical publisher is unaccounted quarantined=%v err=%v", quarantined, err)
	}
	finishProducerRun(t, store, uncertain, "interrupted")
	// Model a serving app. Deliberately stopped apps retain their lifecycle
	// status while the same durable quarantine continues to block starts.
	if _, err := store.DB().Exec(`UPDATE apps SET status = 'running' WHERE id = ?`, appID); err != nil {
		t.Fatal(err)
	}
	if err := store.EnforceCompatibilityQuarantines(); err != nil {
		t.Fatal(err)
	}
	app, err := store.GetAppByID(appID)
	if err != nil || app.Status != "failed" {
		t.Fatalf("enforced app=%+v err=%v", app, err)
	}

	repair := insertProducerRun(t, store, scheduleID, dep, `["producer"]`)
	finishProducerRun(t, store, repair, "succeeded")
	if quarantined, err := store.AppCompatibilityQuarantined(appID); err != nil || quarantined {
		t.Fatalf("after newer repair publication quarantined=%v err=%v", quarantined, err)
	}
}

func TestCompatibilityQuarantine_UsesPhysicalCompletionNotAdmissionOrder(t *testing.T) {
	store := newScheduleStore(t)
	appID := newScheduleAppFixture(t, store, "writer-physical-order")
	dep := promoteConvergenceDeployment(t, store, appID, "v1", "sha256:v1")
	scheduleID := createConvergenceSchedule(t, store, appID, `["producer"]`, schedulespec.DeployTriggerBundleChange)

	admittedFirst := insertProducerRun(t, store, scheduleID, dep, `["producer"]`)
	admittedSecond := insertProducerRun(t, store, scheduleID, dep, `["producer"]`)
	publicationTime := time.Now().UTC()
	finishProducerRunAt(t, store, admittedSecond, "succeeded", publicationTime)
	finishProducerRunAt(t, store, admittedFirst, "interrupted", publicationTime.Add(time.Second))

	if quarantined, err := store.AppCompatibilityQuarantined(appID); err != nil || !quarantined {
		t.Fatalf("later physical overwrite with lower run id quarantined=%v err=%v", quarantined, err)
	}
}

func TestScheduleUncertainty_IsPerScheduleSurvivesPruningAndBlocksDeletion(t *testing.T) {
	store := newScheduleStore(t)
	appID := newScheduleAppFixture(t, store, "uncertainty-retention")
	dep := promoteConvergenceDeployment(t, store, appID, "v1", "sha256:v1")
	scheduleA := createConvergenceSchedule(t, store, appID, `["producer-a"]`, schedulespec.DeployTriggerBundleChange)
	scheduleB, err := store.CreateSchedule(db.CreateScheduleParams{
		AppID: appID, Name: "producer-b", CronExpr: "0 6 * * *", CommandJSON: `["producer-b"]`,
		Enabled: true, TimeoutSeconds: 60, OverlapPolicy: "skip", MissedPolicy: "skip",
		DeployTrigger: schedulespec.DeployTriggerBundleChange, OnSuccess: "none", RollFallback: "defer",
	})
	if err != nil {
		t.Fatal(err)
	}

	failedA := insertProducerRun(t, store, scheduleA, dep, `["producer-a"]`)
	finishProducerRun(t, store, failedA, "failed")
	finishProducerRun(t, store, insertProducerRun(t, store, scheduleB, dep, `["producer-b"]`), "succeeded")
	if quarantined, err := store.AppCompatibilityQuarantined(appID); err != nil || !quarantined {
		t.Fatalf("producer B cleared producer A uncertainty: quarantined=%v err=%v", quarantined, err)
	}
	if deleted, err := store.DeleteScheduleIfIdle(scheduleA); err != nil || deleted {
		t.Fatalf("uncertain schedule deletion: deleted=%v err=%v", deleted, err)
	}

	ordinary, err := store.InsertScheduleRun(db.InsertScheduleRunParams{
		ScheduleID: scheduleA, Status: "running", Trigger: "manual", StartedAt: time.Now().UTC().Add(time.Second),
		OnSuccess: "none", RollFallback: "defer",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.CompleteScheduleRunAndEnqueueActivation(db.CompleteScheduleRunParams{
		RunID: ordinary, Status: "succeeded", ExitCode: intPtr(0), FinishedAt: time.Now().UTC().Add(2 * time.Second),
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.PruneScheduleRuns(scheduleA, 1); err != nil {
		t.Fatal(err)
	}
	if _, err := store.GetScheduleRun(failedA); err != nil {
		t.Fatalf("uncertainty source run was pruned: %v", err)
	}
	rows, err := store.ScheduleFreshnessByApp(appID)
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range rows {
		if row.ScheduleID == scheduleA && row.DeployTriggerSatisfied {
			t.Fatal("uncertain producer reported deploy-trigger satisfied")
		}
	}

	finishProducerRun(t, store, insertProducerRun(t, store, scheduleA, dep, `["producer-a"]`), "succeeded")
	if required, err := store.ScheduleProducerRepairRequired(scheduleA); err != nil || required {
		t.Fatalf("same-schedule success did not clear uncertainty: required=%v err=%v", required, err)
	}
}

func TestMarkRunningSchedulesInterrupted_MaterializesWriterUncertaintyAfterPriorSuccess(t *testing.T) {
	store := newScheduleStore(t)
	appID := newScheduleAppFixture(t, store, "startup-interruption")
	dep := promoteConvergenceDeployment(t, store, appID, "v1", "sha256:v1")
	scheduleID := createConvergenceSchedule(t, store, appID, `["producer"]`, schedulespec.DeployTriggerBundleChange)
	finishProducerRun(t, store, insertProducerRun(t, store, scheduleID, dep, `["producer"]`), "succeeded")
	interruptedRun := insertProducerRun(t, store, scheduleID, dep, `["producer"]`)

	count, err := store.MarkRunningSchedulesInterrupted()
	if err != nil || count != 1 {
		t.Fatalf("MarkRunningSchedulesInterrupted count=%d err=%v", count, err)
	}
	run, err := store.GetScheduleRun(interruptedRun)
	var sequence int64
	sequenceErr := store.DB().QueryRow(`SELECT data_write_sequence FROM schedule_runs WHERE id = ?`, interruptedRun).Scan(&sequence)
	if err != nil || sequenceErr != nil || run.Status != "interrupted" || sequence == 0 {
		t.Fatalf("interrupted writer=%+v sequence=%d err=%v sequence_err=%v", run, sequence, err, sequenceErr)
	}
	if quarantined, err := store.AppCompatibilityQuarantined(appID); err != nil || !quarantined {
		t.Fatalf("inherited writer uncertainty missing: quarantined=%v err=%v", quarantined, err)
	}
}

func TestScheduleConvergence_ObsoleteWriterFinishingLastReopensCurrentObligation(t *testing.T) {
	store := newScheduleStore(t)
	appID := newScheduleAppFixture(t, store, "writer-order")
	v1 := promoteConvergenceDeployment(t, store, appID, "v1", "sha256:v1")
	scheduleID := createConvergenceSchedule(t, store, appID, `["producer-v1"]`, schedulespec.DeployTriggerBundleChange)

	oldRun := insertProducerRun(t, store, scheduleID, v1, `["producer-v1"]`)
	v2 := promoteConvergenceDeployment(t, store, appID, "v2", "sha256:v2")
	newCommand := `["producer-v2"]`
	if err := store.UpdateSchedule(scheduleID, db.UpdateScheduleParams{CommandJSON: &newCommand}); err != nil {
		t.Fatal(err)
	}
	obligations, err := store.ReconcileDeployObligationsForDeployment(appID, v2.ID)
	if err != nil || len(obligations) != 1 || obligations[0].Status != "pending" {
		t.Fatalf("v2 obligation = %+v, %v", obligations, err)
	}

	newRun := insertProducerRun(t, store, scheduleID, v2, newCommand)
	finishProducerRun(t, store, newRun, "succeeded")
	current, err := store.GetDeployObligation(obligations[0].ID)
	if err != nil || current.Status != "satisfied" {
		t.Fatalf("v2 after success = %+v, %v", current, err)
	}

	finishProducerRun(t, store, oldRun, "succeeded")
	state, err := store.GetScheduleProducerState(scheduleID)
	if err != nil {
		t.Fatal(err)
	}
	if state.ContentDigest != "sha256:v1" {
		t.Fatalf("last writer digest = %q, want obsolete v1 writer to be recorded", state.ContentDigest)
	}
	current, err = store.GetDeployObligation(obligations[0].ID)
	if err != nil || current.Status != "pending" {
		t.Fatalf("v2 obligation after obsolete writer = %+v, %v; must reopen", current, err)
	}
}

func TestScheduleConvergence_CallerClockSkewCannotInvertPhysicalCompletionOrder(t *testing.T) {
	store := newScheduleStore(t)
	appID := newScheduleAppFixture(t, store, "completion-order")
	v1 := promoteConvergenceDeployment(t, store, appID, "v1", "sha256:v1")
	scheduleID := createConvergenceSchedule(t, store, appID, `["producer-v1"]`, schedulespec.DeployTriggerBundleChange)
	olderRun := insertProducerRun(t, store, scheduleID, v1, `["producer-v1"]`)

	v2 := promoteConvergenceDeployment(t, store, appID, "v2", "sha256:v2")
	newCommand := `["producer-v2"]`
	if err := store.UpdateSchedule(scheduleID, db.UpdateScheduleParams{CommandJSON: &newCommand}); err != nil {
		t.Fatal(err)
	}
	obligations, err := store.ReconcileDeployObligationsForDeployment(appID, v2.ID)
	if err != nil || len(obligations) != 1 {
		t.Fatalf("v2 obligation = %+v, %v", obligations, err)
	}
	newerRun := insertProducerRun(t, store, scheduleID, v2, newCommand)
	base := time.Now().UTC().Truncate(time.Microsecond)

	// Completion is called while the physical app-writer fence remains held, so
	// transaction order is physical order. Give the later call an older caller
	// timestamp to prove clock skew cannot make the first call win.
	finishProducerRunAt(t, store, newerRun, "succeeded", base.Add(time.Second))
	finishProducerRunAt(t, store, olderRun, "succeeded", base)
	state, err := store.GetScheduleProducerState(scheduleID)
	if err != nil {
		t.Fatal(err)
	}
	if state.ContentDigest != "sha256:v1" || state.ScheduleRunID == nil || *state.ScheduleRunID != olderRun {
		t.Fatalf("last writer under caller clock skew = %+v, want physically later v1 run %d", state, olderRun)
	}
	current, err := store.GetDeployObligation(obligations[0].ID)
	if err != nil || current.Status != "pending" {
		t.Fatalf("v2 obligation after physically later old writer = %+v, %v", current, err)
	}
}

func TestScheduleConvergence_EqualCompletionTimesUsePhysicalTransactionOrder(t *testing.T) {
	store := newScheduleStore(t)
	appID := newScheduleAppFixture(t, store, "completion-tie")
	v1 := promoteConvergenceDeployment(t, store, appID, "v1", "sha256:v1")
	scheduleID := createConvergenceSchedule(t, store, appID, `["producer-v1"]`, schedulespec.DeployTriggerBundleChange)
	lowerRun := insertProducerRun(t, store, scheduleID, v1, `["producer-v1"]`)
	v2 := promoteConvergenceDeployment(t, store, appID, "v2", "sha256:v2")
	higherRun := insertProducerRun(t, store, scheduleID, v2, `["producer-v2"]`)
	finishedAt := time.Now().UTC().Truncate(time.Microsecond)

	finishProducerRunAt(t, store, higherRun, "succeeded", finishedAt)
	finishProducerRunAt(t, store, lowerRun, "succeeded", finishedAt)
	state, err := store.GetScheduleProducerState(scheduleID)
	if err != nil {
		t.Fatal(err)
	}
	if state.ScheduleRunID == nil || *state.ScheduleRunID != lowerRun || state.ContentDigest != "sha256:v1" {
		t.Fatalf("equal-time writer = %+v, want physically later lower run id %d", state, lowerRun)
	}
}

func TestScheduleConvergence_CommandChangeOnSameBundleCreatesNewIdentity(t *testing.T) {
	store := newScheduleStore(t)
	appID := newScheduleAppFixture(t, store, "command-identity")
	dep := promoteConvergenceDeployment(t, store, appID, "v1", "sha256:same")
	scheduleID := createConvergenceSchedule(t, store, appID, `["old"]`, schedulespec.DeployTriggerBundleChange)
	finishProducerRun(t, store, insertProducerRun(t, store, scheduleID, dep, `["old"]`), "succeeded")

	newCommand := `["new"]`
	if err := store.UpdateSchedule(scheduleID, db.UpdateScheduleParams{CommandJSON: &newCommand}); err != nil {
		t.Fatal(err)
	}
	obligations, err := store.ReconcileDeployObligationsForDeployment(appID, dep.ID)
	if err != nil || len(obligations) != 1 {
		t.Fatalf("obligations = %+v, %v", obligations, err)
	}
	if obligations[0].Status != "pending" || obligations[0].ProducerCommandJSON != newCommand {
		t.Fatalf("new command obligation = %+v", obligations[0])
	}
}

func TestScheduleConvergence_RollbackDoesNotReuseHistoricalSuccess(t *testing.T) {
	store := newScheduleStore(t)
	appID := newScheduleAppFixture(t, store, "rollback-state")
	v1 := promoteConvergenceDeployment(t, store, appID, "v1", "sha256:v1")
	scheduleID := createConvergenceSchedule(t, store, appID, `["producer"]`, schedulespec.DeployTriggerBundleChange)
	finishProducerRun(t, store, insertProducerRun(t, store, scheduleID, v1, `["producer"]`), "succeeded")
	v2 := promoteConvergenceDeployment(t, store, appID, "v2", "sha256:v2")
	finishProducerRun(t, store, insertProducerRun(t, store, scheduleID, v2, `["producer"]`), "succeeded")

	rollback := promoteConvergenceDeployment(t, store, appID, "v1-rollback", "sha256:v1")
	obligations, err := store.ReconcileDeployObligationsForDeployment(appID, rollback.ID)
	if err != nil || len(obligations) != 1 || obligations[0].Status != "pending" {
		t.Fatalf("rollback obligation = %+v, %v", obligations, err)
	}
}

func TestScheduleConvergence_FailureRequiresExplicitRetry(t *testing.T) {
	store := newScheduleStore(t)
	appID := newScheduleAppFixture(t, store, "explicit-retry")
	dep := promoteConvergenceDeployment(t, store, appID, "v1", "sha256:v1")
	createConvergenceSchedule(t, store, appID, `["producer"]`, schedulespec.DeployTriggerBundleChange)
	obligations, err := store.ReconcileDeployObligationsForDeployment(appID, dep.ID)
	if err != nil {
		t.Fatal(err)
	}
	claimed, err := store.ClaimNextDeployObligation()
	if err != nil {
		t.Fatal(err)
	}
	runID, err := store.InsertDeployScheduleRun(db.InsertScheduleRunParams{
		ScheduleID: claimed.ScheduleID, Status: "running", Trigger: "deploy", StartedAt: time.Now().UTC(),
		OnSuccess: claimed.OnSuccess, RollFallback: claimed.RollFallback,
		DeploymentID: &claimed.DeploymentID, AppVersion: claimed.AppVersion, ContentDigest: claimed.ContentDigest,
		ProducerFingerprint: claimed.ProducerFingerprint, ProducerCommandJSON: claimed.ProducerCommandJSON,
		DeployObligationID: &claimed.ID,
	})
	if err != nil {
		t.Fatal(err)
	}
	finishProducerRun(t, store, runID, "failed")
	if _, err := store.ReconcileDeployObligationsForDeployment(appID, dep.ID); err != nil {
		t.Fatal(err)
	}
	failed, err := store.GetDeployObligation(obligations[0].ID)
	if err != nil || failed.Status != "failed" {
		t.Fatalf("failed obligation = %+v, %v", failed, err)
	}
	if err := store.RetryDeployObligation(failed.ID); err != nil {
		t.Fatal(err)
	}
	retried, _ := store.GetDeployObligation(failed.ID)
	if retried.Status != "pending" {
		t.Fatalf("retried status = %q", retried.Status)
	}
}

func TestScheduleConvergence_RecoveryRepairsAdmissionWithoutRun(t *testing.T) {
	store := newScheduleStore(t)
	appID := newScheduleAppFixture(t, store, "admission-recovery")
	dep := promoteConvergenceDeployment(t, store, appID, "v1", "sha256:v1")
	createConvergenceSchedule(t, store, appID, `["producer"]`, schedulespec.DeployTriggerBundleChange)
	if _, err := store.ReconcileDeployObligationsForDeployment(appID, dep.ID); err != nil {
		t.Fatal(err)
	}
	claimed, err := store.ClaimNextDeployObligation()
	if err != nil || claimed.Status != "dispatching" {
		t.Fatalf("claim = %+v, %v", claimed, err)
	}
	if err := store.RecoverDeployObligations(); err != nil {
		t.Fatal(err)
	}
	recovered, err := store.GetDeployObligation(claimed.ID)
	if err != nil || recovered.Status != "pending" {
		t.Fatalf("recovered = %+v, %v", recovered, err)
	}
	if _, err := store.ClaimNextDeployObligation(); err != nil && !errors.Is(err, db.ErrNotFound) {
		t.Fatal(err)
	}
}

func TestScheduleConvergence_FirstDeploySurvivesRunHistoryPruning(t *testing.T) {
	store := newScheduleStore(t)
	appID := newScheduleAppFixture(t, store, "first-deploy-pruned")
	v1 := promoteConvergenceDeployment(t, store, appID, "v1", "sha256:v1")
	scheduleID := createConvergenceSchedule(t, store, appID, `["producer"]`, schedulespec.DeployTriggerFirstDeploy)
	finishProducerRun(t, store, insertProducerRun(t, store, scheduleID, v1, `["producer"]`), "succeeded")

	// A newer ordinary run lets bounded history remove the deploy producer row.
	ordinaryID, err := store.InsertScheduleRun(db.InsertScheduleRunParams{
		ScheduleID: scheduleID, Status: "running", Trigger: "manual", StartedAt: time.Now().UTC().Add(time.Second),
		OnSuccess: "none", RollFallback: "defer",
	})
	if err != nil {
		t.Fatal(err)
	}
	finishProducerRun(t, store, ordinaryID, "failed")
	if deleted, err := store.PruneScheduleRuns(scheduleID, 1); err != nil || deleted == 0 {
		t.Fatalf("prune = %d, %v", deleted, err)
	}

	v2 := promoteConvergenceDeployment(t, store, appID, "v2", "sha256:v2")
	obligations, err := store.ReconcileDeployObligationsForDeployment(appID, v2.ID)
	if err != nil || len(obligations) != 1 || obligations[0].Status != "satisfied" {
		t.Fatalf("first_deploy after pruning = %+v, %v", obligations, err)
	}
}

func TestScheduleConvergence_ReconcilesEveryPersistedSchedule(t *testing.T) {
	store := newScheduleStore(t)
	appID := newScheduleAppFixture(t, store, "all-persisted")
	dep := promoteConvergenceDeployment(t, store, appID, "v1", "sha256:v1")
	first := createConvergenceSchedule(t, store, appID, `["manifest-producer"]`, schedulespec.DeployTriggerBundleChange)
	second, err := store.CreateSchedule(db.CreateScheduleParams{
		AppID: appID, Name: "api-created", CronExpr: "0 6 * * *", CommandJSON: `["api-producer"]`,
		Enabled: true, TimeoutSeconds: 60, OverlapPolicy: "skip", MissedPolicy: "skip",
		DeployTrigger: schedulespec.DeployTriggerBundleChange, OnSuccess: "none", RollFallback: "defer",
	})
	if err != nil {
		t.Fatal(err)
	}
	obligations, err := store.ReconcileDeployObligationsForDeployment(appID, dep.ID)
	if err != nil || len(obligations) != 2 {
		t.Fatalf("obligations = %+v, %v", obligations, err)
	}
	seen := map[int64]bool{}
	for _, obligation := range obligations {
		seen[obligation.ScheduleID] = true
	}
	if !seen[first] || !seen[second] {
		t.Fatalf("persisted schedules not fully reconciled: %+v", obligations)
	}
}

func TestScheduleConvergence_ClaimRejectsStaleDesiredState(t *testing.T) {
	t.Run("disabled schedule", func(t *testing.T) {
		store := newScheduleStore(t)
		appID := newScheduleAppFixture(t, store, "claim-disabled")
		dep := promoteConvergenceDeployment(t, store, appID, "v1", "sha256:v1")
		scheduleID := createConvergenceSchedule(t, store, appID, `["producer"]`, schedulespec.DeployTriggerBundleChange)
		if _, err := store.ReconcileDeployObligationsForDeployment(appID, dep.ID); err != nil {
			t.Fatal(err)
		}
		disabled := false
		if err := store.UpdateSchedule(scheduleID, db.UpdateScheduleParams{Enabled: &disabled}); err != nil {
			t.Fatal(err)
		}
		if _, err := store.ClaimNextDeployObligation(); !errors.Is(err, db.ErrNotFound) {
			t.Fatalf("claim disabled stale obligation error = %v, want not found", err)
		}
	})

	t.Run("superseded deployment", func(t *testing.T) {
		store := newScheduleStore(t)
		appID := newScheduleAppFixture(t, store, "claim-old-deploy")
		v1 := promoteConvergenceDeployment(t, store, appID, "v1", "sha256:v1")
		createConvergenceSchedule(t, store, appID, `["producer"]`, schedulespec.DeployTriggerBundleChange)
		if _, err := store.ReconcileDeployObligationsForDeployment(appID, v1.ID); err != nil {
			t.Fatal(err)
		}
		promoteConvergenceDeployment(t, store, appID, "v2", "sha256:v2")
		if _, err := store.ClaimNextDeployObligation(); !errors.Is(err, db.ErrNotFound) {
			t.Fatalf("claim old deployment obligation error = %v, want not found", err)
		}
	})
}

func TestScheduleConvergence_ReleasedPoisonWorkDoesNotBlockAnotherApp(t *testing.T) {
	store := newScheduleStore(t)
	firstApp := newScheduleAppFixture(t, store, "poison-first")
	firstDep := promoteConvergenceDeployment(t, store, firstApp, "v1", "sha256:first")
	createConvergenceSchedule(t, store, firstApp, `["first"]`, schedulespec.DeployTriggerBundleChange)
	if _, err := store.ReconcileDeployObligationsForDeployment(firstApp, firstDep.ID); err != nil {
		t.Fatal(err)
	}

	secondApp := newScheduleAppFixture(t, store, "healthy-second")
	secondDep := promoteConvergenceDeployment(t, store, secondApp, "v1", "sha256:second")
	secondSchedule := createConvergenceSchedule(t, store, secondApp, `["second"]`, schedulespec.DeployTriggerBundleChange)
	if _, err := store.ReconcileDeployObligationsForDeployment(secondApp, secondDep.ID); err != nil {
		t.Fatal(err)
	}

	poison, err := store.ClaimNextDeployObligation()
	if err != nil {
		t.Fatal(err)
	}
	if err := store.ReleaseDeployObligation(poison.ID, errors.New("temporary admission failure")); err != nil {
		t.Fatal(err)
	}
	healthy, err := store.ClaimNextDeployObligation()
	if err != nil {
		t.Fatalf("healthy obligation remained head-of-line blocked: %v", err)
	}
	if healthy.ScheduleID != secondSchedule {
		t.Fatalf("claimed schedule %d, want healthy schedule %d", healthy.ScheduleID, secondSchedule)
	}
}

func TestScheduleConvergence_ReleasedWorkWaitsForDatabaseDeadline(t *testing.T) {
	store := newScheduleStore(t)
	appID := newScheduleAppFixture(t, store, "retry-deadline")
	dep := promoteConvergenceDeployment(t, store, appID, "v1", "sha256:v1")
	createConvergenceSchedule(t, store, appID, `["producer"]`, schedulespec.DeployTriggerBundleChange)
	if _, err := store.ReconcileDeployObligationsForDeployment(appID, dep.ID); err != nil {
		t.Fatal(err)
	}
	claimed, err := store.ClaimNextDeployObligation()
	if err != nil {
		t.Fatal(err)
	}
	if err := store.ReleaseDeployObligation(claimed.ID, errors.New("retry later")); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ClaimNextDeployObligation(); !errors.Is(err, db.ErrNotFound) {
		t.Fatalf("released obligation was eligible before DB deadline: %v", err)
	}
	if _, err := store.DB().Exec(`UPDATE schedule_deploy_obligations
		SET next_attempt_at = datetime('now', '-1 second') WHERE id = ?`, claimed.ID); err != nil {
		t.Fatal(err)
	}
	if got, err := store.ClaimNextDeployObligation(); err != nil || got.ID != claimed.ID {
		t.Fatalf("released obligation after DB deadline=%+v err=%v", got, err)
	}
}

func TestScheduleConvergence_ScopedClaimCannotConsumeAnotherRequest(t *testing.T) {
	store := newScheduleStore(t)
	firstApp := newScheduleAppFixture(t, store, "scope-first")
	firstDep := promoteConvergenceDeployment(t, store, firstApp, "v1", "sha256:first")
	createConvergenceSchedule(t, store, firstApp, `["first"]`, schedulespec.DeployTriggerBundleChange)
	if _, err := store.ReconcileDeployObligationsForDeployment(firstApp, firstDep.ID); err != nil {
		t.Fatal(err)
	}

	secondApp := newScheduleAppFixture(t, store, "scope-second")
	secondDep := promoteConvergenceDeployment(t, store, secondApp, "v1", "sha256:second")
	secondSchedule := createConvergenceSchedule(t, store, secondApp, `["second"]`, schedulespec.DeployTriggerBundleChange)
	if _, err := store.ReconcileDeployObligationsForDeployment(secondApp, secondDep.ID); err != nil {
		t.Fatal(err)
	}

	claimed, err := store.ClaimNextDeployObligationFor(secondApp, secondDep.ID)
	if err != nil {
		t.Fatal(err)
	}
	if claimed.ScheduleID != secondSchedule || claimed.DeploymentID != secondDep.ID {
		t.Fatalf("scoped claim crossed request boundary: %+v", claimed)
	}
}
