package db_test

import (
	"errors"
	"testing"
	"time"

	"github.com/rvben/shinyhub/internal/db"
)

func TestManualRestartIsNeitherCoalescedNorScheduledRollDamper(t *testing.T) {
	store := newScheduleStore(t)
	appID := newScheduleAppFixture(t, store, "manual-roll-damper")
	app, err := store.GetAppByID(appID)
	if err != nil {
		t.Fatal(err)
	}
	dep := promoteConvergenceDeployment(t, store, appID, "v1", "digest-v1")
	manual, err := store.EnqueueRollingRestart(app, dep)
	if err != nil {
		t.Fatal(err)
	}
	id, err := store.CreateSchedule(db.CreateScheduleParams{AppID: appID, Name: "refresh", CronExpr: "0 5 * * *", CommandJSON: `["true"]`, Enabled: true, TimeoutSeconds: 60, OverlapPolicy: "skip", MissedPolicy: "skip", OnSuccess: "roll", MinRollIntervalSeconds: 3600})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Add(time.Second)
	run := insertActivationRun(t, store, id, now.Add(-time.Minute), "roll", 3600)
	scheduled, err := store.CompleteScheduleRunAndEnqueueActivation(db.CompleteScheduleRunParams{RunID: run, Status: "succeeded", ExitCode: intPtr(0), FinishedAt: now})
	if err != nil {
		t.Fatal(err)
	}
	current, err := store.GetScheduleActivation(manual.ID)
	if err != nil || current.Status != "pending" {
		t.Fatalf("manual restart superseded: %+v %v", current, err)
	}
	claimed, err := store.ClaimNextScheduleActivation(now)
	if err != nil || claimed.ID != manual.ID {
		t.Fatalf("claim=%+v %v", claimed, err)
	}
	if err := store.FinishScheduleActivation(manual.ID, "succeeded", "", now, false); err != nil {
		t.Fatal(err)
	}
	after, err := store.GetScheduleActivation(scheduled.ID)
	if err != nil || !after.DueAt.Equal(scheduled.DueAt) {
		t.Fatalf("manual restart damped scheduled roll: %+v %v", after, err)
	}
}

func TestTerminalServingCandidateWithoutRuntimeIsReaped(t *testing.T) {
	store := newScheduleStore(t)
	appID := newScheduleAppFixture(t, store, "terminal-roll")
	app, _ := store.GetAppByID(appID)
	dep := promoteConvergenceDeployment(t, store, appID, "v1", "digest-v1")
	a, err := store.EnqueueRollingRestart(app, dep)
	if err != nil {
		t.Fatal(err)
	}
	candidate, err := store.ActivationDeployment(a.ID, dep)
	if err != nil {
		t.Fatal(err)
	}
	a, err = store.ClaimNextScheduleActivation(time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if err := store.FinishScheduleActivation(a.ID, "failed", "deadline expired", time.Now(), false); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ClaimNextScheduleActivation(time.Now()); !errors.Is(err, db.ErrNotFound) {
		t.Fatalf("claim=%v", err)
	}
	after, err := store.GetDeploymentByID(candidate.ID)
	if err != nil || after.Status != db.DeploymentFailed {
		t.Fatalf("candidate=%+v %v", after, err)
	}
}

func TestServingActivationPreservesProducerConvergenceAdmission(t *testing.T) {
	store := newScheduleStore(t)
	appID := newScheduleAppFixture(t, store, "serving-producer-convergence")
	app, _ := store.GetAppByID(appID)
	dep := promoteConvergenceDeployment(t, store, appID, "v1", "digest-v1")
	createConvergenceSchedule(t, store, appID, `["true"]`, "bundle_change")
	if _, err := store.ReconcileDeployObligationsForDeployment(appID, dep.ID); err != nil {
		t.Fatal(err)
	}
	a, err := store.EnqueueRollingRestart(app, dep)
	if err != nil {
		t.Fatal(err)
	}
	candidate, err := store.ActivationDeployment(a.ID, dep)
	if err != nil {
		t.Fatal(err)
	}
	// A serving-only candidate does not change the immutable producer bundle.
	claimed, err := store.ClaimNextDeployObligation()
	if err != nil || claimed.DeploymentID != dep.ID {
		t.Fatalf("pending serving candidate blocked producer: %+v %v", claimed, err)
	}
	if err := store.PromoteDeployment(candidate.ID); err != nil {
		t.Fatal(err)
	}
	if err := store.ReconcileAllDeployObligations(); err != nil {
		t.Fatal(err)
	}
	claimed, err = store.ClaimNextDeployObligation()
	if err != nil || claimed.DeploymentID != candidate.ID {
		t.Fatalf("convergence missed serving authority: %+v %v", claimed, err)
	}
}
