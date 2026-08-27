package db_test

import (
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/rvben/shinyhub/internal/db"
)

func TestCompleteScheduleRunAndEnqueueActivation_IsAtomicIdempotentAndCoalesces(t *testing.T) {
	store := newScheduleStore(t)
	appID := newScheduleAppFixture(t, store, "activation-outbox")
	scheduleID, err := store.CreateSchedule(db.CreateScheduleParams{
		AppID: appID, Name: "refresh", CronExpr: "*/15 * * * *",
		CommandJSON: `["python","fetch.py"]`, Enabled: true, TimeoutSeconds: 60,
		OverlapPolicy: "skip", MissedPolicy: "skip", OnSuccess: "roll",
		MinRollIntervalSeconds: 3600,
	})
	if err != nil {
		t.Fatalf("create schedule: %v", err)
	}

	finish := time.Now().UTC().Truncate(time.Second)
	firstRun := insertActivationRun(t, store, scheduleID, finish.Add(-time.Minute), "roll", 3600)
	first, err := store.CompleteScheduleRunAndEnqueueActivation(db.CompleteScheduleRunParams{
		RunID: firstRun, Status: "succeeded", ExitCode: intPtr(0), FinishedAt: finish,
	})
	if err != nil {
		t.Fatalf("complete first: %v", err)
	}
	if first == nil || first.Status != "pending" || first.TargetGeneration != 1 {
		t.Fatalf("first activation = %+v, want pending generation 1", first)
	}
	if first.ScheduleRunID == nil || *first.ScheduleRunID != firstRun {
		t.Fatalf("first activation run = %v, want %d", first.ScheduleRunID, firstRun)
	}
	linkedRun, err := store.GetScheduleRun(firstRun)
	if err != nil {
		t.Fatalf("get linked run: %v", err)
	}
	if linkedRun.OnSuccess != "roll" || linkedRun.ActivationID == nil || *linkedRun.ActivationID != first.ID ||
		linkedRun.TargetGeneration == nil || *linkedRun.TargetGeneration != first.TargetGeneration ||
		linkedRun.ActivationStatus != "pending" {
		t.Fatalf("linked run = %+v, want roll activation %d at generation %d", linkedRun, first.ID, first.TargetGeneration)
	}

	// A retried terminal write must return the same outbox record and must not
	// increment the generation a second time.
	again, err := store.CompleteScheduleRunAndEnqueueActivation(db.CompleteScheduleRunParams{
		RunID: firstRun, Status: "succeeded", ExitCode: intPtr(0), FinishedAt: finish,
	})
	if err != nil {
		t.Fatalf("repeat completion: %v", err)
	}
	if again == nil || again.ID != first.ID || again.TargetGeneration != 1 {
		t.Fatalf("repeat activation = %+v, want same id/generation as %+v", again, first)
	}

	// A newer success supersedes queued work for the same app and inherits its
	// due time. Repeated successes cannot push queued work farther into the
	// future.
	secondRun := insertActivationRun(t, store, scheduleID, finish, "roll", 3600)
	second, err := store.CompleteScheduleRunAndEnqueueActivation(db.CompleteScheduleRunParams{
		RunID: secondRun, Status: "succeeded", ExitCode: intPtr(0), FinishedAt: finish.Add(15 * time.Minute),
	})
	if err != nil {
		t.Fatalf("complete second: %v", err)
	}
	if second == nil || second.TargetGeneration != 2 || second.Status != "pending" {
		t.Fatalf("second activation = %+v, want pending generation 2", second)
	}
	if !second.DueAt.Equal(first.DueAt) {
		t.Fatalf("second due_at = %s, want inherited queued due_at %s", second.DueAt, first.DueAt)
	}
	rows, err := store.ListScheduleActivationsByApp(appID, 10)
	if err != nil {
		t.Fatalf("list activations: %v", err)
	}
	if len(rows) != 2 || rows[0].ID != second.ID || rows[1].Status != "superseded" || rows[1].SupersededByID == nil || *rows[1].SupersededByID != second.ID {
		t.Fatalf("activation history = %+v, want newest then superseded predecessor", rows)
	}
}

func TestCompleteScheduleRunAndEnqueueActivation_FailedAndNoneDoNotActivate(t *testing.T) {
	store := newScheduleStore(t)
	appID := newScheduleAppFixture(t, store, "activation-none")
	scheduleID, err := store.CreateSchedule(db.CreateScheduleParams{
		AppID: appID, Name: "refresh", CronExpr: "0 5 * * *", CommandJSON: `["false"]`,
		Enabled: true, TimeoutSeconds: 60, OverlapPolicy: "skip", MissedPolicy: "skip",
	})
	if err != nil {
		t.Fatalf("create schedule: %v", err)
	}
	now := time.Now().UTC().Truncate(time.Second)
	failedRun := insertActivationRun(t, store, scheduleID, now, "roll", 0)
	activation, err := store.CompleteScheduleRunAndEnqueueActivation(db.CompleteScheduleRunParams{
		RunID: failedRun, Status: "failed", ExitCode: intPtr(1), FinishedAt: now,
	})
	if err != nil || activation != nil {
		t.Fatalf("failed completion activation=%+v err=%v, want nil nil", activation, err)
	}
	noneRun := insertActivationRun(t, store, scheduleID, now, "none", 0)
	activation, err = store.CompleteScheduleRunAndEnqueueActivation(db.CompleteScheduleRunParams{
		RunID: noneRun, Status: "succeeded", ExitCode: intPtr(0), FinishedAt: now,
	})
	if err != nil || activation != nil {
		t.Fatalf("none completion activation=%+v err=%v, want nil nil", activation, err)
	}
	rows, err := store.ListScheduleActivationsByApp(appID, 10)
	if err != nil || len(rows) != 0 {
		t.Fatalf("activations=%+v err=%v, want empty", rows, err)
	}
}

func TestFinishScheduleActivation_RebasesQueuedWorkAfterRunningActivation(t *testing.T) {
	store := newScheduleStore(t)
	appID := newScheduleAppFixture(t, store, "activation-damping")
	scheduleID, err := store.CreateSchedule(db.CreateScheduleParams{
		AppID: appID, Name: "refresh", CronExpr: "*/15 * * * *", CommandJSON: `["true"]`,
		Enabled: true, TimeoutSeconds: 60, OverlapPolicy: "skip", MissedPolicy: "skip",
		OnSuccess: "roll", MinRollIntervalSeconds: 3600,
	})
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now().UTC().Truncate(time.Second)
	firstRun := insertActivationRun(t, store, scheduleID, start, "roll", 3600)
	if _, err := store.CompleteScheduleRunAndEnqueueActivation(db.CompleteScheduleRunParams{
		RunID: firstRun, Status: "succeeded", ExitCode: intPtr(0), FinishedAt: start,
	}); err != nil {
		t.Fatal(err)
	}
	first, err := store.ClaimNextScheduleActivation(start)
	if err != nil {
		t.Fatal(err)
	}

	secondRun := insertActivationRun(t, store, scheduleID, start.Add(time.Minute), "roll", 3600)
	second, err := store.CompleteScheduleRunAndEnqueueActivation(db.CompleteScheduleRunParams{
		RunID: secondRun, Status: "succeeded", ExitCode: intPtr(0), FinishedAt: start.Add(time.Minute),
	})
	if err != nil {
		t.Fatal(err)
	}
	if second.DueAt.After(start.Add(time.Minute)) {
		t.Fatalf("second activation was damped before the running predecessor finished: %s", second.DueAt)
	}

	finished := start.Add(10 * time.Minute)
	if err := store.FinishScheduleActivation(first.ID, "succeeded", "", finished, true); err != nil {
		t.Fatal(err)
	}
	audit, err := store.ListAuditEvents("schedule_activation_outcome", 10, 0)
	if err != nil || len(audit) != 1 {
		t.Fatalf("activation outcome audit=%+v err=%v", audit, err)
	}
	var detail map[string]any
	if err := json.Unmarshal([]byte(audit[0].Detail), &detail); err != nil {
		t.Fatalf("decode activation outcome audit: %v", err)
	}
	if detail["status"] != "succeeded" || detail["phase"] != "starting_surge" ||
		detail["schedule_name"] != "refresh" || detail["schedule_run_id"] != float64(firstRun) {
		t.Fatalf("activation outcome audit detail=%v", detail)
	}
	second, err = store.GetScheduleActivation(second.ID)
	if err != nil {
		t.Fatal(err)
	}
	wantDue := finished.Add(time.Hour)
	if second.Status != "deferred_interval" || !second.DueAt.Equal(wantDue) {
		t.Fatalf("queued activation = status %q due %s, want deferred_interval at %s", second.Status, second.DueAt, wantDue)
	}
}

func TestCompleteScheduleRunAndEnqueueActivation_IncreasedDamperCannotBeBypassedByOlderQueue(t *testing.T) {
	store := newScheduleStore(t)
	appID := newScheduleAppFixture(t, store, "activation-policy-floor")
	scheduleID, err := store.CreateSchedule(db.CreateScheduleParams{
		AppID: appID, Name: "refresh", CronExpr: "*/15 * * * *", CommandJSON: `["true"]`,
		Enabled: true, TimeoutSeconds: 60, OverlapPolicy: "skip", MissedPolicy: "skip", OnSuccess: "roll",
	})
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now().UTC().Truncate(time.Second)

	anchorRun := insertActivationRun(t, store, scheduleID, start, "roll", 0)
	if _, err := store.CompleteScheduleRunAndEnqueueActivation(db.CompleteScheduleRunParams{
		RunID: anchorRun, Status: "succeeded", ExitCode: intPtr(0), FinishedAt: start,
	}); err != nil {
		t.Fatal(err)
	}
	anchor, err := store.ClaimNextScheduleActivation(start)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.FinishScheduleActivation(anchor.ID, "succeeded", "", start, true); err != nil {
		t.Fatal(err)
	}

	oldRun := insertActivationRun(t, store, scheduleID, start.Add(time.Minute), "roll", 0)
	oldQueued, err := store.CompleteScheduleRunAndEnqueueActivation(db.CompleteScheduleRunParams{
		RunID: oldRun, Status: "succeeded", ExitCode: intPtr(0), FinishedAt: start.Add(time.Minute),
	})
	if err != nil {
		t.Fatal(err)
	}
	if !oldQueued.DueAt.Equal(start.Add(time.Minute)) {
		t.Fatalf("old zero-damper due_at=%s, want %s", oldQueued.DueAt, start.Add(time.Minute))
	}

	newRun := insertActivationRun(t, store, scheduleID, start.Add(10*time.Minute), "roll", 3600)
	newQueued, err := store.CompleteScheduleRunAndEnqueueActivation(db.CompleteScheduleRunParams{
		RunID: newRun, Status: "succeeded", ExitCode: intPtr(0), FinishedAt: start.Add(10 * time.Minute),
	})
	if err != nil {
		t.Fatal(err)
	}
	wantDue := start.Add(time.Hour)
	if newQueued.Status != "deferred_interval" || !newQueued.DueAt.Equal(wantDue) {
		t.Fatalf("new one-hour policy = status %q due %s, want deferred_interval at %s",
			newQueued.Status, newQueued.DueAt, wantDue)
	}
	oldQueued, err = store.GetScheduleActivation(oldQueued.ID)
	if err != nil {
		t.Fatal(err)
	}
	if oldQueued.Status != "superseded" {
		t.Fatalf("older queue status=%q, want superseded", oldQueued.Status)
	}
}

func TestRepairingActivationCannotBeSupersededAndIsClaimedFirst(t *testing.T) {
	store := newScheduleStore(t)
	appID := newScheduleAppFixture(t, store, "activation-repair-priority")
	scheduleID, err := store.CreateSchedule(db.CreateScheduleParams{
		AppID: appID, Name: "refresh", CronExpr: "*/15 * * * *", CommandJSON: `["true"]`,
		Enabled: true, TimeoutSeconds: 60, OverlapPolicy: "skip", MissedPolicy: "skip", OnSuccess: "roll",
	})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Second)
	firstRun := insertActivationRun(t, store, scheduleID, now, "roll", 0)
	first, err := store.CompleteScheduleRunAndEnqueueActivation(db.CompleteScheduleRunParams{
		RunID: firstRun, Status: "succeeded", ExitCode: intPtr(0), FinishedAt: now,
	})
	if err != nil {
		t.Fatal(err)
	}
	claimed, err := store.ClaimNextScheduleActivation(now)
	if err != nil || claimed.ID != first.ID {
		t.Fatalf("claim first = %+v, %v", claimed, err)
	}
	if err := store.UpdateScheduleActivationProgress(first.ID, "starting_slot", 1, 0); err != nil {
		t.Fatal(err)
	}
	if err := store.DeferScheduleActivation(first.ID, "repairing", "replacement stop unconfirmed", now, now); err != nil {
		t.Fatal(err)
	}

	secondRun := insertActivationRun(t, store, scheduleID, now.Add(time.Minute), "roll", 0)
	second, err := store.CompleteScheduleRunAndEnqueueActivation(db.CompleteScheduleRunParams{
		RunID: secondRun, Status: "succeeded", ExitCode: intPtr(0), FinishedAt: now.Add(time.Minute),
	})
	if err != nil {
		t.Fatal(err)
	}
	first, err = store.GetScheduleActivation(first.ID)
	if err != nil {
		t.Fatal(err)
	}
	if first.Status != "repairing" || first.SupersededByID != nil || first.Phase != "starting_slot" {
		t.Fatalf("repair activation mutated by newer success: %+v", first)
	}
	if second.Status != "pending" {
		t.Fatalf("new activation status=%q, want pending behind repair", second.Status)
	}
	latest, err := store.LatestScheduleActivationsByApp(appID)
	if err != nil || len(latest) != 1 || latest[0].ID != first.ID {
		t.Fatalf("operator-visible activation=%+v err=%v, want active repair %d ahead of newer queue", latest, err, first.ID)
	}
	next, err := store.ClaimNextScheduleActivation(now.Add(time.Minute))
	if err != nil || next.ID != first.ID || next.Phase != "recovering" {
		t.Fatalf("next claim=%+v err=%v, want repairing activation %d first", next, err, first.ID)
	}
}

func TestClaimNextScheduleActivation_ReclaimsAbandonedRunningRow(t *testing.T) {
	store := newScheduleStore(t)
	appID := newScheduleAppFixture(t, store, "activation-reclaim")
	scheduleID, err := store.CreateSchedule(db.CreateScheduleParams{
		AppID: appID, Name: "refresh", CronExpr: "0 * * * *", CommandJSON: `["true"]`,
		Enabled: true, TimeoutSeconds: 60, OverlapPolicy: "skip", MissedPolicy: "skip", OnSuccess: "roll",
	})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Second)
	runID := insertActivationRun(t, store, scheduleID, now, "roll", 0)
	_, err = store.CompleteScheduleRunAndEnqueueActivation(db.CompleteScheduleRunParams{
		RunID: runID, Status: "succeeded", ExitCode: intPtr(0), FinishedAt: now,
	})
	if err != nil {
		t.Fatal(err)
	}
	first, err := store.ClaimNextScheduleActivation(now)
	if err != nil {
		t.Fatal(err)
	}
	reclaimed, err := store.ClaimNextScheduleActivation(now.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if reclaimed.ID != first.ID || reclaimed.Phase != "recovering" || reclaimed.Attempts != first.Attempts {
		t.Fatalf("reclaimed=%+v, want same running action in recovery", reclaimed)
	}
}

func TestCapacityDeferral_DoesNotConsumeRollAttemptBudget(t *testing.T) {
	store := newScheduleStore(t)
	appID := newScheduleAppFixture(t, store, "activation-capacity")
	scheduleID, err := store.CreateSchedule(db.CreateScheduleParams{
		AppID: appID, Name: "refresh", CronExpr: "0 * * * *", CommandJSON: `["true"]`,
		Enabled: true, TimeoutSeconds: 60, OverlapPolicy: "skip", MissedPolicy: "skip", OnSuccess: "roll",
	})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Second)
	runID := insertActivationRun(t, store, scheduleID, now, "roll", 0)
	created, err := store.CompleteScheduleRunAndEnqueueActivation(db.CompleteScheduleRunParams{
		RunID: runID, Status: "succeeded", ExitCode: intPtr(0), FinishedAt: now,
	})
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 5; i++ {
		a, err := store.ClaimNextScheduleActivation(now.Add(time.Duration(i) * time.Minute))
		if err != nil {
			t.Fatal(err)
		}
		deferredAt := now.Add(time.Duration(i) * time.Minute)
		if err := store.DeferScheduleActivation(a.ID, "deferred_capacity", "host pressure", now.Add(time.Duration(i+1)*time.Minute), deferredAt); err != nil {
			t.Fatal(err)
		}
	}
	a, err := store.GetScheduleActivation(created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if a.Attempts != 0 {
		t.Fatalf("attempts=%d after capacity-only deferrals, want 0", a.Attempts)
	}
	if a.CapacityDeferrals != 5 {
		t.Fatalf("capacity_deferrals=%d, want 5", a.CapacityDeferrals)
	}
	if a.CapacityDeferredAt == nil || !a.CapacityDeferredAt.Equal(now) {
		t.Fatalf("capacity_deferred_at=%v, want first deferral at %s", a.CapacityDeferredAt, now)
	}
}

func TestActivationPolicyIsSnapshottedFromRun(t *testing.T) {
	store := newScheduleStore(t)
	appID := newScheduleAppFixture(t, store, "activation-policy-snapshot")
	scheduleID, err := store.CreateSchedule(db.CreateScheduleParams{
		AppID: appID, Name: "refresh", CronExpr: "0 * * * *", CommandJSON: `["true"]`,
		Enabled: true, TimeoutSeconds: 60, OverlapPolicy: "skip", MissedPolicy: "skip", OnSuccess: "roll",
		RollFallback: "restart", MaxDeferAgeSeconds: 21600,
	})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Second)
	runID, err := store.InsertScheduleRun(db.InsertScheduleRunParams{
		ScheduleID: scheduleID, Status: "running", Trigger: "schedule", StartedAt: now,
		OnSuccess: "roll", RollFallback: "restart", MaxDeferAgeSeconds: 21600,
	})
	if err != nil {
		t.Fatal(err)
	}
	a, err := store.CompleteScheduleRunAndEnqueueActivation(db.CompleteScheduleRunParams{
		RunID: runID, Status: "succeeded", ExitCode: intPtr(0), FinishedAt: now,
	})
	if err != nil {
		t.Fatal(err)
	}
	if a.RollFallback != "restart" || a.MaxDeferAgeSeconds != 21600 {
		t.Fatalf("activation policy=%q/%d, want restart/21600", a.RollFallback, a.MaxDeferAgeSeconds)
	}
	run, err := store.GetScheduleRun(runID)
	if err != nil {
		t.Fatal(err)
	}
	if run.RollFallback != "restart" || run.MaxDeferAgeSeconds != 21600 {
		t.Fatalf("run policy=%q/%d, want restart/21600", run.RollFallback, run.MaxDeferAgeSeconds)
	}
}

func TestCancelQueuedScheduleActivation(t *testing.T) {
	store := newScheduleStore(t)
	appID := newScheduleAppFixture(t, store, "activation-cancel")
	scheduleID, err := store.CreateSchedule(db.CreateScheduleParams{
		AppID: appID, Name: "refresh", CronExpr: "0 * * * *", CommandJSON: `["true"]`,
		Enabled: true, TimeoutSeconds: 60, OverlapPolicy: "skip", MissedPolicy: "skip", OnSuccess: "roll",
	})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Second)
	runID := insertActivationRun(t, store, scheduleID, now, "roll", 0)
	created, err := store.CompleteScheduleRunAndEnqueueActivation(db.CompleteScheduleRunParams{
		RunID: runID, Status: "succeeded", ExitCode: intPtr(0), FinishedAt: now,
	})
	if err != nil {
		t.Fatal(err)
	}
	cancelled, err := store.CancelQueuedScheduleActivation(scheduleID, now.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if cancelled.ID != created.ID || cancelled.Status != "cancelled" || cancelled.LastError != "" ||
		cancelled.FinishedAt == nil || cancelled.Attempts != 0 {
		t.Fatalf("cancelled activation=%+v", cancelled)
	}
	if _, err := store.CancelQueuedScheduleActivation(scheduleID, now.Add(2*time.Minute)); !errors.Is(err, db.ErrNotFound) {
		t.Fatalf("second cancel error=%v, want not found", err)
	}
	secondRun := insertActivationRun(t, store, scheduleID, now.Add(3*time.Minute), "roll", 0)
	if _, err := store.CompleteScheduleRunAndEnqueueActivation(db.CompleteScheduleRunParams{
		RunID: secondRun, Status: "succeeded", ExitCode: intPtr(0), FinishedAt: now.Add(3 * time.Minute),
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ClaimNextScheduleActivation(now.Add(3 * time.Minute)); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CancelQueuedScheduleActivation(scheduleID, now.Add(4*time.Minute)); !errors.Is(err, db.ErrScheduleActivationBusy) {
		t.Fatalf("cancel running activation error=%v, want busy", err)
	}
}

func TestScheduleActivationInFlight_FencesRetryIdentityButNotUntouchedQueue(t *testing.T) {
	store := newScheduleStore(t)
	appID := newScheduleAppFixture(t, store, "activation-runtime-fence")
	scheduleID, err := store.CreateSchedule(db.CreateScheduleParams{
		AppID: appID, Name: "refresh", CronExpr: "0 * * * *", CommandJSON: `["true"]`,
		Enabled: true, TimeoutSeconds: 60, OverlapPolicy: "skip", MissedPolicy: "skip", OnSuccess: "roll",
	})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Second)
	runID := insertActivationRun(t, store, scheduleID, now, "roll", 0)
	a, err := store.CompleteScheduleRunAndEnqueueActivation(db.CompleteScheduleRunParams{
		RunID: runID, Status: "succeeded", ExitCode: intPtr(0), FinishedAt: now,
	})
	if err != nil {
		t.Fatal(err)
	}
	if owned, err := store.ScheduleActivationInFlight(appID); err != nil || owned {
		t.Fatalf("untouched pending queue owns runtime=%v err=%v, want false", owned, err)
	}
	pid, port := 2147483647, 22000
	if err := store.UpsertActivationReplica(db.UpsertReplicaParams{
		AppID: appID, Index: 1, PID: &pid, Port: &port, Status: "starting",
		Provider: "native", Tier: "default", EndpointURL: "http://127.0.0.1:22000",
	}, a.TargetGeneration, a.ID); err != nil {
		t.Fatal(err)
	}
	if owned, err := store.ScheduleActivationInFlight(appID); err != nil || !owned {
		t.Fatalf("pending retry identity owns runtime=%v err=%v, want true", owned, err)
	}
	if err := store.ClearReplicaRuntimeIdentity(appID, 1); err != nil {
		t.Fatal(err)
	}
	if owned, err := store.ScheduleActivationInFlight(appID); err != nil || owned {
		t.Fatalf("confirmed stop tombstone owns runtime=%v err=%v, want false", owned, err)
	}
}

func TestRollAttemptIsChargedOnlyWithDurableOutcome(t *testing.T) {
	store := newScheduleStore(t)
	appID := newScheduleAppFixture(t, store, "activation-attempt-accounting")
	scheduleID, err := store.CreateSchedule(db.CreateScheduleParams{
		AppID: appID, Name: "refresh", CronExpr: "0 * * * *", CommandJSON: `["true"]`,
		Enabled: true, TimeoutSeconds: 60, OverlapPolicy: "skip", MissedPolicy: "skip", OnSuccess: "roll",
	})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Second)
	runID := insertActivationRun(t, store, scheduleID, now, "roll", 0)
	created, err := store.CompleteScheduleRunAndEnqueueActivation(db.CompleteScheduleRunParams{
		RunID: runID, Status: "succeeded", ExitCode: intPtr(0), FinishedAt: now,
	})
	if err != nil {
		t.Fatal(err)
	}
	a, err := store.ClaimNextScheduleActivation(now)
	if err != nil {
		t.Fatal(err)
	}
	if a.Attempts != 0 {
		t.Fatalf("claim charged attempts=%d, want 0 until an outcome commits", a.Attempts)
	}
	if err := store.DeferScheduleActivation(a.ID, "pending", "retry", now.Add(time.Minute), now); err != nil {
		t.Fatal(err)
	}
	a, err = store.GetScheduleActivation(created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if a.Attempts != 1 {
		t.Fatalf("attempts=%d after durable retry outcome, want 1", a.Attempts)
	}
}

func TestDeleteScheduleIfIdle_AtomicallyPreservesRunAndNonterminalActivation(t *testing.T) {
	store := newScheduleStore(t)
	appID := newScheduleAppFixture(t, store, "schedule-delete-active")
	scheduleID, err := store.CreateSchedule(db.CreateScheduleParams{
		AppID: appID, Name: "refresh", CronExpr: "0 * * * *", CommandJSON: `["true"]`,
		Enabled: true, TimeoutSeconds: 60, OverlapPolicy: "skip", MissedPolicy: "skip", OnSuccess: "roll",
	})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Second)
	runID := insertActivationRun(t, store, scheduleID, now, "roll", 0)
	deleted, err := store.DeleteScheduleIfIdle(scheduleID)
	if err != nil || deleted {
		t.Fatalf("delete with active run = %v, %v; want false, nil", deleted, err)
	}
	created, err := store.CompleteScheduleRunAndEnqueueActivation(db.CompleteScheduleRunParams{
		RunID: runID, Status: "succeeded", ExitCode: intPtr(0), FinishedAt: now,
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, status := range []string{"pending", "deferred_interval", "deferred_capacity", "repairing", "running"} {
		if _, err := store.DB().Exec(`UPDATE schedule_activations SET status = ? WHERE id = ?`, status, created.ID); err != nil {
			t.Fatalf("set activation status %q: %v", status, err)
		}
		deleted, err = store.DeleteScheduleIfIdle(scheduleID)
		if err != nil || deleted {
			t.Fatalf("delete with %s activation = %v, %v; want false, nil", status, deleted, err)
		}
	}
	if err := store.FinishScheduleActivation(created.ID, "not_needed", "", now.Add(time.Second), true); err != nil {
		t.Fatal(err)
	}
	deleted, err = store.DeleteScheduleIfIdle(scheduleID)
	if err != nil || !deleted {
		t.Fatalf("delete after terminal activation = %v, %v; want true, nil", deleted, err)
	}
	retained, err := store.GetScheduleActivation(created.ID)
	if err != nil {
		t.Fatalf("terminal activation lost after schedule deletion: %v", err)
	}
	if retained.ScheduleID == nil || *retained.ScheduleID != scheduleID ||
		retained.ScheduleRunID == nil || *retained.ScheduleRunID != runID ||
		retained.ScheduleName != "refresh" {
		t.Fatalf("terminal activation attribution not retained: %+v", retained)
	}
	latest, err := store.LatestScheduleActivationsByApp(appID)
	if err != nil || len(latest) != 1 || latest[0].ID != created.ID {
		t.Fatalf("deleted-schedule activation visibility=%+v err=%v", latest, err)
	}
}

func insertActivationRun(t *testing.T, store *db.Store, scheduleID int64, started time.Time, action string, interval int) int64 {
	t.Helper()
	runID, err := store.InsertScheduleRun(db.InsertScheduleRunParams{
		ScheduleID: scheduleID, Status: "running", Trigger: "schedule", StartedAt: started,
		OnSuccess: action, MinRollIntervalSeconds: interval,
	})
	if err != nil {
		t.Fatalf("insert run: %v", err)
	}
	return runID
}

func intPtr(v int) *int { return &v }
