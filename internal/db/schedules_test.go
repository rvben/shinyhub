package db_test

import (
	"errors"
	"testing"
	"time"

	"github.com/rvben/shinyhub/internal/db"
	"github.com/rvben/shinyhub/internal/dbtest"
)

func newScheduleStore(t *testing.T) *db.Store {
	t.Helper()
	return dbtest.New(t)
}

func newScheduleAppFixture(t *testing.T, store *db.Store, slug string) int64 {
	t.Helper()
	if err := store.CreateUser(db.CreateUserParams{
		Username: "owner-" + slug, PasswordHash: "h", Role: "developer",
	}); err != nil {
		t.Fatalf("create user: %v", err)
	}
	u, err := store.GetUserByUsername("owner-" + slug)
	if err != nil {
		t.Fatalf("get user: %v", err)
	}
	if err := store.CreateApp(db.CreateAppParams{
		Slug: slug, Name: slug, OwnerID: u.ID,
	}); err != nil {
		t.Fatalf("create app: %v", err)
	}
	app, err := store.GetAppBySlug(slug)
	if err != nil {
		t.Fatalf("get app: %v", err)
	}
	return app.ID
}

func TestSchedules_CreateGetUpdateDelete(t *testing.T) {
	store := newScheduleStore(t)
	appID := newScheduleAppFixture(t, store, "fetch")

	id, err := store.CreateSchedule(db.CreateScheduleParams{
		AppID: appID, Name: "daily-fetch", CronExpr: "0 6 * * *",
		CommandJSON: `["python","fetch.py"]`, Enabled: true, TimeoutSeconds: 600,
		OverlapPolicy: "skip", MissedPolicy: "run_once",
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if id == 0 {
		t.Fatal("expected non-zero id")
	}

	got, err := store.GetSchedule(id)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Name != "daily-fetch" || got.CronExpr != "0 6 * * *" || !got.Enabled {
		t.Fatalf("unexpected: %+v", got)
	}

	if err := store.UpdateSchedule(id, db.UpdateScheduleParams{
		CronExpr: ptrString("*/5 * * * *"), Enabled: ptrBool(false),
	}); err != nil {
		t.Fatalf("update: %v", err)
	}
	again, _ := store.GetSchedule(id)
	if again.CronExpr != "*/5 * * * *" || again.Enabled {
		t.Fatalf("update did not stick: %+v", again)
	}

	if err := store.DeleteSchedule(id); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := store.GetSchedule(id); !errors.Is(err, db.ErrNotFound) {
		t.Fatalf("expected ErrNotFound after delete, got %v", err)
	}
}

func TestSchedules_ListByApp_AndAllEnabled(t *testing.T) {
	store := newScheduleStore(t)
	a1 := newScheduleAppFixture(t, store, "fetch")
	a2 := newScheduleAppFixture(t, store, "report")

	mustCreate := func(appID int64, name string, enabled bool) {
		_, err := store.CreateSchedule(db.CreateScheduleParams{
			AppID: appID, Name: name, CronExpr: "* * * * *",
			CommandJSON: `["true"]`, Enabled: enabled, TimeoutSeconds: 60,
			OverlapPolicy: "skip", MissedPolicy: "skip",
		})
		if err != nil {
			t.Fatalf("create %s: %v", name, err)
		}
	}
	mustCreate(a1, "a", true)
	mustCreate(a1, "b", false)
	mustCreate(a2, "c", true)

	list, err := store.ListSchedulesByApp(a1)
	if err != nil {
		t.Fatalf("list app: %v", err)
	}
	if len(list) != 2 {
		t.Fatalf("expected 2 schedules for app1, got %d", len(list))
	}

	enabled, err := store.ListEnabledSchedules()
	if err != nil {
		t.Fatalf("list enabled: %v", err)
	}
	if len(enabled) != 2 { // a + c
		t.Fatalf("expected 2 enabled across all apps, got %d", len(enabled))
	}
}

func TestScheduleRuns_InsertUpdateList(t *testing.T) {
	store := newScheduleStore(t)
	appID := newScheduleAppFixture(t, store, "fetch")
	schedID, _ := store.CreateSchedule(db.CreateScheduleParams{
		AppID: appID, Name: "x", CronExpr: "* * * * *",
		CommandJSON: `["true"]`, Enabled: true, TimeoutSeconds: 10,
		OverlapPolicy: "skip", MissedPolicy: "skip",
	})

	started := time.Now().UTC().Truncate(time.Second)
	runID, err := store.InsertScheduleRun(db.InsertScheduleRunParams{
		ScheduleID: schedID, Status: "running", Trigger: "schedule",
		StartedAt: started, LogPath: "/var/log/x/run-1.log",
	})
	if err != nil {
		t.Fatalf("insert run: %v", err)
	}

	finished := started.Add(2 * time.Second)
	if err := store.FinishScheduleRun(db.FinishScheduleRunParams{
		RunID: runID, Status: "succeeded", ExitCode: ptrInt(0), FinishedAt: finished,
	}); err != nil {
		t.Fatalf("finish: %v", err)
	}

	runs, err := store.ListScheduleRuns(schedID, 10, 0)
	if err != nil {
		t.Fatalf("list runs: %v", err)
	}
	if len(runs) != 1 || runs[0].Status != "succeeded" || runs[0].ExitCode == nil || *runs[0].ExitCode != 0 {
		t.Fatalf("unexpected run: %+v", runs)
	}
}

// TestScheduleRuns_ExitCodeNullableLifecycle asserts exit_code is NULL (nil)
// for a run that has not reached a terminal state, and is populated only once
// the run finishes. A still-running run with exit_code defaulted to 0 reads as
// "succeeded" to any caller that checks the code without also checking status.
func TestScheduleRuns_ExitCodeNullableLifecycle(t *testing.T) {
	store := newScheduleStore(t)
	appID := newScheduleAppFixture(t, store, "fetch")
	schedID, _ := store.CreateSchedule(db.CreateScheduleParams{
		AppID: appID, Name: "x", CronExpr: "* * * * *",
		CommandJSON: `["true"]`, Enabled: true, TimeoutSeconds: 10,
		OverlapPolicy: "skip", MissedPolicy: "skip",
	})

	runID, err := store.InsertScheduleRun(db.InsertScheduleRunParams{
		ScheduleID: schedID, Status: "running", Trigger: "register",
		StartedAt: time.Now().UTC(), LogPath: "x.log",
	})
	if err != nil {
		t.Fatalf("insert: %v", err)
	}

	// While running: exit_code must be nil via both Get and List.
	got, err := store.GetScheduleRun(runID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.ExitCode != nil {
		t.Fatalf("running run exit_code must be nil, got %d", *got.ExitCode)
	}
	runs, _ := store.ListScheduleRuns(schedID, 10, 0)
	if len(runs) != 1 || runs[0].ExitCode != nil {
		t.Fatalf("running run exit_code must be nil in list, got %+v", runs)
	}

	// Finishing with a real non-zero code populates it.
	if err := store.FinishScheduleRun(db.FinishScheduleRunParams{
		RunID: runID, Status: "failed", ExitCode: ptrInt(2), FinishedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("finish: %v", err)
	}
	got, _ = store.GetScheduleRun(runID)
	if got.ExitCode == nil || *got.ExitCode != 2 {
		t.Fatalf("finished run exit_code must be 2, got %+v", got.ExitCode)
	}
}

// TestSchedulesNeedingFirstFireRetry covers the startup-reconcile gate: a
// run_on_register first-fire (trigger='register') that was interrupted by a
// service restart and has never succeeded must be re-fired, while a schedule
// that already succeeded, was operator-cancelled, failed, is disabled, or never
// had a first-fire must not be.
func TestSchedulesNeedingFirstFireRetry(t *testing.T) {
	store := newScheduleStore(t)
	appID := newScheduleAppFixture(t, store, "fetch")

	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	mkSched := func(name string, enabled bool) int64 {
		id, err := store.CreateSchedule(db.CreateScheduleParams{
			AppID: appID, Name: name, CronExpr: "0 5 * * *",
			CommandJSON: `["true"]`, Enabled: enabled, TimeoutSeconds: 60,
			OverlapPolicy: "skip", MissedPolicy: "skip",
		})
		if err != nil {
			t.Fatalf("create schedule %s: %v", name, err)
		}
		return id
	}
	// run inserts a finished run with a controlled started_at so "most recent
	// register run" ordering is deterministic.
	run := func(schedID int64, trigger, status string, offset time.Duration) {
		rid, err := store.InsertScheduleRun(db.InsertScheduleRunParams{
			ScheduleID: schedID, Status: status, Trigger: trigger,
			StartedAt: base.Add(offset), LogPath: "x",
		})
		if err != nil {
			t.Fatalf("insert run: %v", err)
		}
		_ = rid
	}

	// A: interrupted first-fire, never succeeded -> re-fire.
	a := mkSched("a-needs-retry", true)
	run(a, "register", "interrupted", time.Minute)

	// B: interrupted first-fire but a later success exists -> do not re-fire.
	b := mkSched("b-succeeded", true)
	run(b, "register", "interrupted", time.Minute)
	run(b, "register", "succeeded", 2*time.Minute)

	// C: latest register run was operator-cancelled -> terminal, do not re-fire.
	c := mkSched("c-cancelled", true)
	run(c, "register", "interrupted", time.Minute)
	run(c, "register", "cancelled", 2*time.Minute)

	// D: first-fire failed (app error) -> self-heals on next deploy, not restart.
	d := mkSched("d-failed", true)
	run(d, "register", "failed", time.Minute)

	// E: disabled schedule -> not registered, do not re-fire.
	e := mkSched("e-disabled", false)
	run(e, "register", "interrupted", time.Minute)

	// F: only a cron-triggered interrupted run, never a first-fire -> ignore.
	f := mkSched("f-no-register", true)
	run(f, "schedule", "interrupted", time.Minute)

	ids, err := store.SchedulesNeedingFirstFireRetry()
	if err != nil {
		t.Fatalf("SchedulesNeedingFirstFireRetry: %v", err)
	}
	got := map[int64]bool{}
	for _, id := range ids {
		got[id] = true
	}
	if !got[a] {
		t.Errorf("schedule A (interrupted, never succeeded) must be selected for re-fire")
	}
	for name, id := range map[string]int64{"B-succeeded": b, "C-cancelled": c, "D-failed": d, "E-disabled": e, "F-no-register": f} {
		if got[id] {
			t.Errorf("schedule %s must NOT be selected for re-fire", name)
		}
	}
	if len(ids) != 1 {
		t.Errorf("expected exactly schedule A, got %d ids: %v", len(ids), ids)
	}
}

func TestScheduleRuns_MarkInterrupted(t *testing.T) {
	store := newScheduleStore(t)
	appID := newScheduleAppFixture(t, store, "fetch")
	schedID, _ := store.CreateSchedule(db.CreateScheduleParams{
		AppID: appID, Name: "x", CronExpr: "* * * * *",
		CommandJSON: `["true"]`, Enabled: true, TimeoutSeconds: 10,
		OverlapPolicy: "skip", MissedPolicy: "skip",
	})
	_, _ = store.InsertScheduleRun(db.InsertScheduleRunParams{
		ScheduleID: schedID, Status: "running", Trigger: "schedule",
		StartedAt: time.Now().UTC(), LogPath: "x.log",
	})
	n, err := store.MarkRunningSchedulesInterrupted()
	if err != nil {
		t.Fatalf("mark interrupted: %v", err)
	}
	if n != 1 {
		t.Fatalf("expected 1 row affected, got %d", n)
	}
	runs, _ := store.ListScheduleRuns(schedID, 10, 0)
	if runs[0].Status != "interrupted" {
		t.Fatalf("expected interrupted, got %s", runs[0].Status)
	}
	// An interrupted run never observed a process exit, so its exit_code is NULL.
	if runs[0].ExitCode != nil {
		t.Fatalf("interrupted run exit_code must be nil, got %d", *runs[0].ExitCode)
	}
}

func TestSharedData_GrantAndList(t *testing.T) {
	store := newScheduleStore(t)
	consumer := newScheduleAppFixture(t, store, "report")
	source := newScheduleAppFixture(t, store, "fetch")

	if err := store.GrantSharedData(consumer, source); err != nil {
		t.Fatalf("grant: %v", err)
	}
	list, err := store.ListSharedDataSources(consumer)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(list) != 1 || list[0].SourceAppID != source {
		t.Fatalf("unexpected list: %+v", list)
	}

	if err := store.RevokeSharedData(consumer, source); err != nil {
		t.Fatalf("revoke: %v", err)
	}
	list, _ = store.ListSharedDataSources(consumer)
	if len(list) != 0 {
		t.Fatalf("expected empty list, got %+v", list)
	}
}

func TestSharedData_RejectsSelfMount(t *testing.T) {
	store := newScheduleStore(t)
	appID := newScheduleAppFixture(t, store, "report")
	if err := store.GrantSharedData(appID, appID); err == nil {
		t.Fatal("expected error for self-mount, got nil")
	}
}

func TestScheduleRuns_FinishMissing_ReturnsErrNotFound(t *testing.T) {
	store := newScheduleStore(t)
	err := store.FinishScheduleRun(db.FinishScheduleRunParams{
		RunID: 999, Status: "succeeded", ExitCode: ptrInt(0), FinishedAt: time.Now().UTC(),
	})
	if !errors.Is(err, db.ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

func TestScheduleRuns_SetLogPath(t *testing.T) {
	store := newScheduleStore(t)
	appID := newScheduleAppFixture(t, store, "fetch")
	schedID, _ := store.CreateSchedule(db.CreateScheduleParams{
		AppID: appID, Name: "x", CronExpr: "* * * * *",
		CommandJSON: `["true"]`, Enabled: true, TimeoutSeconds: 10,
		OverlapPolicy: "skip", MissedPolicy: "skip",
	})
	runID, err := store.InsertScheduleRun(db.InsertScheduleRunParams{
		ScheduleID: schedID, Status: "running", Trigger: "schedule",
		StartedAt: time.Now().UTC(), LogPath: "",
	})
	if err != nil {
		t.Fatalf("insert: %v", err)
	}

	if err := store.SetScheduleRunLogPath(runID, "/var/log/x/run-1.log"); err != nil {
		t.Fatalf("set log path: %v", err)
	}
	got, _ := store.GetScheduleRun(runID)
	if got.LogPath != "/var/log/x/run-1.log" {
		t.Fatalf("expected /var/log/x/run-1.log, got %q", got.LogPath)
	}

	if err := store.SetScheduleRunLogPath(999, "x"); !errors.Is(err, db.ErrNotFound) {
		t.Fatalf("expected ErrNotFound for missing row, got %v", err)
	}
}

// TestSchedules_CreateDuplicate_ReturnsErrScheduleNameExists verifies that
// inserting a second schedule with the same (app_id, name) returns
// ErrScheduleNameExists and that errors.Is matches the sentinel.
func TestSchedules_CreateDuplicate_ReturnsErrScheduleNameExists(t *testing.T) {
	store := newScheduleStore(t)
	appID := newScheduleAppFixture(t, store, "dup")

	params := db.CreateScheduleParams{
		AppID: appID, Name: "daily", CronExpr: "0 6 * * *",
		CommandJSON: `["python","run.py"]`, Enabled: true, TimeoutSeconds: 300,
		OverlapPolicy: "skip", MissedPolicy: "skip",
	}
	if _, err := store.CreateSchedule(params); err != nil {
		t.Fatalf("first create: %v", err)
	}

	_, err := store.CreateSchedule(params)
	if err == nil {
		t.Fatal("expected error on duplicate name, got nil")
	}
	if !errors.Is(err, db.ErrScheduleNameExists) {
		t.Fatalf("expected errors.Is(err, ErrScheduleNameExists), got: %v", err)
	}
}

func ptrString(s string) *string { return &s }
func ptrBool(b bool) *bool       { return &b }

func TestUpsertScheduleByName_InsertsWhenAbsent(t *testing.T) {
	store := newScheduleStore(t)
	appID := newScheduleAppFixture(t, store, "alpha")

	id, created, err := store.UpsertScheduleByName(db.UpsertScheduleByNameParams{
		AppID: appID, Name: "daily", CronExpr: "0 6 * * *",
		CommandJSON: `["echo","hi"]`, Enabled: true,
		TimeoutSeconds: 600, OverlapPolicy: "skip", MissedPolicy: "skip",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !created {
		t.Errorf("expected created=true on first insert")
	}
	if id == 0 {
		t.Errorf("expected non-zero id")
	}

	sc, err := store.GetSchedule(id)
	if err != nil {
		t.Fatal(err)
	}
	if sc.CronExpr != "0 6 * * *" || sc.CommandJSON != `["echo","hi"]` ||
		!sc.Enabled || sc.TimeoutSeconds != 600 ||
		sc.OverlapPolicy != "skip" || sc.MissedPolicy != "skip" {
		t.Errorf("inserted row mismatch: %+v", sc)
	}
}

func TestUpsertScheduleByName_UpdatesWhenPresent(t *testing.T) {
	store := newScheduleStore(t)
	appID := newScheduleAppFixture(t, store, "alpha")

	first, createdFirst, err := store.UpsertScheduleByName(db.UpsertScheduleByNameParams{
		AppID: appID, Name: "daily", CronExpr: "0 6 * * *",
		CommandJSON: `["echo","v1"]`, Enabled: true,
		TimeoutSeconds: 600, OverlapPolicy: "skip", MissedPolicy: "skip",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !createdFirst {
		t.Fatal("first call should report created=true")
	}

	second, created, err := store.UpsertScheduleByName(db.UpsertScheduleByNameParams{
		AppID: appID, Name: "daily", CronExpr: "0 7 * * *",
		CommandJSON: `["echo","v2"]`, Enabled: false,
		TimeoutSeconds: 900, OverlapPolicy: "queue", MissedPolicy: "run_once",
	})
	if err != nil {
		t.Fatal(err)
	}
	if created {
		t.Errorf("expected created=false on update")
	}
	if second != first {
		t.Errorf("expected same id on upsert; got %d -> %d", first, second)
	}

	sc, err := store.GetSchedule(second)
	if err != nil {
		t.Fatal(err)
	}
	if sc.CronExpr != "0 7 * * *" || sc.OverlapPolicy != "queue" ||
		sc.MissedPolicy != "run_once" || sc.TimeoutSeconds != 900 ||
		sc.CommandJSON != `["echo","v2"]` || sc.Enabled {
		t.Errorf("update did not stick: %+v", sc)
	}
}

func TestUpsertScheduleByName_ScopedPerApp(t *testing.T) {
	store := newScheduleStore(t)
	appA := newScheduleAppFixture(t, store, "a")
	appB := newScheduleAppFixture(t, store, "b")

	idA, _, err := store.UpsertScheduleByName(db.UpsertScheduleByNameParams{
		AppID: appA, Name: "daily", CronExpr: "0 6 * * *",
		CommandJSON: `["x"]`, Enabled: true, TimeoutSeconds: 60,
		OverlapPolicy: "skip", MissedPolicy: "skip",
	})
	if err != nil {
		t.Fatal(err)
	}
	idB, createdB, err := store.UpsertScheduleByName(db.UpsertScheduleByNameParams{
		AppID: appB, Name: "daily", CronExpr: "0 7 * * *",
		CommandJSON: `["y"]`, Enabled: true, TimeoutSeconds: 60,
		OverlapPolicy: "skip", MissedPolicy: "skip",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !createdB {
		t.Errorf("same name on a different app should be a create, not collide")
	}
	if idA == idB {
		t.Errorf("expected distinct ids across apps; both = %d", idA)
	}
}

func TestSchedules_Timezone_NullRoundTrip(t *testing.T) {
	store := newScheduleStore(t)
	appID := newScheduleAppFixture(t, store, "tz-null")

	// Create with no timezone (nil = inherit).
	id, err := store.CreateSchedule(db.CreateScheduleParams{
		AppID: appID, Name: "no-tz", CronExpr: "0 6 * * *",
		CommandJSON: `["x"]`, Enabled: true, TimeoutSeconds: 60,
		OverlapPolicy: "skip", MissedPolicy: "skip",
		Timezone: nil,
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	got, err := store.GetSchedule(id)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Timezone != nil {
		t.Errorf("Timezone: got %v, want nil (inherit)", got.Timezone)
	}
}

func TestSchedules_Timezone_ExplicitRoundTrip(t *testing.T) {
	store := newScheduleStore(t)
	appID := newScheduleAppFixture(t, store, "tz-explicit")

	tz := "Europe/Amsterdam"
	id, err := store.CreateSchedule(db.CreateScheduleParams{
		AppID: appID, Name: "with-tz", CronExpr: "0 6 * * *",
		CommandJSON: `["x"]`, Enabled: true, TimeoutSeconds: 60,
		OverlapPolicy: "skip", MissedPolicy: "skip",
		Timezone: &tz,
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	got, err := store.GetSchedule(id)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Timezone == nil || *got.Timezone != "Europe/Amsterdam" {
		t.Errorf("Timezone: got %v, want Europe/Amsterdam", got.Timezone)
	}
}

func TestSchedules_Timezone_UpsertRoundTrip(t *testing.T) {
	store := newScheduleStore(t)
	appID := newScheduleAppFixture(t, store, "tz-upsert")

	tz := "America/New_York"
	// Insert with explicit timezone.
	id, created, err := store.UpsertScheduleByName(db.UpsertScheduleByNameParams{
		AppID: appID, Name: "tz-sched", CronExpr: "0 9 * * *",
		CommandJSON: `["x"]`, Enabled: true, TimeoutSeconds: 60,
		OverlapPolicy: "skip", MissedPolicy: "skip",
		Timezone: &tz,
	})
	if err != nil {
		t.Fatalf("upsert insert: %v", err)
	}
	if !created {
		t.Error("expected created=true on first upsert")
	}
	got, _ := store.GetSchedule(id)
	if got.Timezone == nil || *got.Timezone != "America/New_York" {
		t.Errorf("after insert: Timezone = %v, want America/New_York", got.Timezone)
	}

	// Update to a different timezone via upsert.
	tz2 := "Asia/Tokyo"
	id2, created2, err := store.UpsertScheduleByName(db.UpsertScheduleByNameParams{
		AppID: appID, Name: "tz-sched", CronExpr: "0 9 * * *",
		CommandJSON: `["x"]`, Enabled: true, TimeoutSeconds: 60,
		OverlapPolicy: "skip", MissedPolicy: "skip",
		Timezone: &tz2,
	})
	if err != nil {
		t.Fatalf("upsert update: %v", err)
	}
	if created2 {
		t.Error("expected created=false on second upsert (update)")
	}
	if id2 != id {
		t.Errorf("upsert update changed id: got %d, want %d", id2, id)
	}
	updated, _ := store.GetSchedule(id)
	if updated.Timezone == nil || *updated.Timezone != "Asia/Tokyo" {
		t.Errorf("after update: Timezone = %v, want Asia/Tokyo", updated.Timezone)
	}

	// Upsert with nil clears to NULL (inherit).
	id3, _, err := store.UpsertScheduleByName(db.UpsertScheduleByNameParams{
		AppID: appID, Name: "tz-sched", CronExpr: "0 9 * * *",
		CommandJSON: `["x"]`, Enabled: true, TimeoutSeconds: 60,
		OverlapPolicy: "skip", MissedPolicy: "skip",
		Timezone: nil,
	})
	if err != nil {
		t.Fatalf("upsert clear: %v", err)
	}
	cleared, _ := store.GetSchedule(id3)
	if cleared.Timezone != nil {
		t.Errorf("after clear: Timezone = %v, want nil", cleared.Timezone)
	}
}

func TestSchedules_Timezone_UpdateClearsToInherit(t *testing.T) {
	store := newScheduleStore(t)
	appID := newScheduleAppFixture(t, store, "tz-update")

	tz := "Pacific/Auckland"
	id, _ := store.CreateSchedule(db.CreateScheduleParams{
		AppID: appID, Name: "tz-sched", CronExpr: "0 9 * * *",
		CommandJSON: `["x"]`, Enabled: true, TimeoutSeconds: 60,
		OverlapPolicy: "skip", MissedPolicy: "skip",
		Timezone: &tz,
	})

	// Patch to clear timezone (empty string = set to NULL).
	empty := ""
	if err := store.UpdateSchedule(id, db.UpdateScheduleParams{
		SetTimezone: true,
		Timezone:    &empty,
	}); err != nil {
		t.Fatalf("update: %v", err)
	}
	got, _ := store.GetSchedule(id)
	if got.Timezone != nil {
		t.Errorf("after clear: Timezone = %v, want nil", got.Timezone)
	}
}

func TestSchedule_EffectiveLocation_Inherit(t *testing.T) {
	amsterdam, _ := time.LoadLocation("Europe/Amsterdam")
	sched := &db.Schedule{Timezone: nil}
	loc := sched.EffectiveLocation(amsterdam)
	if loc != amsterdam {
		t.Errorf("expected Amsterdam, got %v", loc)
	}
}

func TestSchedule_EffectiveLocation_Explicit(t *testing.T) {
	tz := "America/New_York"
	sched := &db.Schedule{Timezone: &tz}
	loc := sched.EffectiveLocation(time.UTC)
	if loc.String() != "America/New_York" {
		t.Errorf("expected New_York, got %v", loc)
	}
}

func TestSchedule_EffectiveLocation_EmptyStringInherits(t *testing.T) {
	empty := ""
	sched := &db.Schedule{Timezone: &empty}
	loc := sched.EffectiveLocation(time.UTC)
	if loc != time.UTC {
		t.Errorf("empty string should inherit UTC, got %v", loc)
	}
}

func TestSchedule_EffectiveLocation_NilDefaultFallsBackToUTC(t *testing.T) {
	sched := &db.Schedule{Timezone: nil}
	loc := sched.EffectiveLocation(nil)
	if loc != time.UTC {
		t.Errorf("nil default should fall back to UTC, got %v", loc)
	}
}

// TestSchedules_Timezone_UpdateSchedule_SetTimezoneFalse_LeavesUnchanged asserts
// that calling UpdateSchedule with SetTimezone=false on a schedule that has a
// stored timezone leaves the stored timezone intact. Pins the "field not provided
// in PATCH" path through the SetTimezone sentinel.
func TestSchedules_Timezone_UpdateSchedule_SetTimezoneFalse_LeavesUnchanged(t *testing.T) {
	store := newScheduleStore(t)
	appID := newScheduleAppFixture(t, store, "tz-setfalse")

	tz := "Europe/Amsterdam"
	id, err := store.CreateSchedule(db.CreateScheduleParams{
		AppID: appID, Name: "tz-sched", CronExpr: "0 9 * * *",
		CommandJSON: `["x"]`, Enabled: true, TimeoutSeconds: 60,
		OverlapPolicy: "skip", MissedPolicy: "skip",
		Timezone: &tz,
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	// Update only Enabled; SetTimezone=false means timezone must be untouched.
	enabled := false
	if err := store.UpdateSchedule(id, db.UpdateScheduleParams{
		Enabled:     &enabled,
		SetTimezone: false, // explicit: do not touch timezone column
	}); err != nil {
		t.Fatalf("update: %v", err)
	}

	got, err := store.GetSchedule(id)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Enabled {
		t.Errorf("expected Enabled=false after update")
	}
	if got.Timezone == nil || *got.Timezone != "Europe/Amsterdam" {
		t.Errorf("Timezone should be unchanged: got %v, want Europe/Amsterdam", got.Timezone)
	}
}
