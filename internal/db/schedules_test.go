package db

import (
	"errors"
	"testing"
	"time"
)

func newScheduleStore(t *testing.T) *Store {
	t.Helper()
	store, err := Open(":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := store.Migrate(); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	t.Cleanup(func() { store.Close() })
	return store
}

func newScheduleAppFixture(t *testing.T, store *Store, slug string) int64 {
	t.Helper()
	if err := store.CreateUser(CreateUserParams{
		Username: "owner-" + slug, PasswordHash: "h", Role: "developer",
	}); err != nil {
		t.Fatalf("create user: %v", err)
	}
	u, err := store.GetUserByUsername("owner-" + slug)
	if err != nil {
		t.Fatalf("get user: %v", err)
	}
	if err := store.CreateApp(CreateAppParams{
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

	id, err := store.CreateSchedule(CreateScheduleParams{
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

	if err := store.UpdateSchedule(id, UpdateScheduleParams{
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
	if _, err := store.GetSchedule(id); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound after delete, got %v", err)
	}
}

func TestSchedules_ListByApp_AndAllEnabled(t *testing.T) {
	store := newScheduleStore(t)
	a1 := newScheduleAppFixture(t, store, "fetch")
	a2 := newScheduleAppFixture(t, store, "report")

	mustCreate := func(appID int64, name string, enabled bool) {
		_, err := store.CreateSchedule(CreateScheduleParams{
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
	schedID, _ := store.CreateSchedule(CreateScheduleParams{
		AppID: appID, Name: "x", CronExpr: "* * * * *",
		CommandJSON: `["true"]`, Enabled: true, TimeoutSeconds: 10,
		OverlapPolicy: "skip", MissedPolicy: "skip",
	})

	started := time.Now().UTC().Truncate(time.Second)
	runID, err := store.InsertScheduleRun(InsertScheduleRunParams{
		ScheduleID: schedID, Status: "running", Trigger: "schedule",
		StartedAt: started, LogPath: "/var/log/x/run-1.log",
	})
	if err != nil {
		t.Fatalf("insert run: %v", err)
	}

	finished := started.Add(2 * time.Second)
	if err := store.FinishScheduleRun(FinishScheduleRunParams{
		RunID: runID, Status: "succeeded", ExitCode: 0, FinishedAt: finished,
	}); err != nil {
		t.Fatalf("finish: %v", err)
	}

	runs, err := store.ListScheduleRuns(schedID, 10, 0)
	if err != nil {
		t.Fatalf("list runs: %v", err)
	}
	if len(runs) != 1 || runs[0].Status != "succeeded" || runs[0].ExitCode != 0 {
		t.Fatalf("unexpected run: %+v", runs)
	}
}

func TestScheduleRuns_MarkInterrupted(t *testing.T) {
	store := newScheduleStore(t)
	appID := newScheduleAppFixture(t, store, "fetch")
	schedID, _ := store.CreateSchedule(CreateScheduleParams{
		AppID: appID, Name: "x", CronExpr: "* * * * *",
		CommandJSON: `["true"]`, Enabled: true, TimeoutSeconds: 10,
		OverlapPolicy: "skip", MissedPolicy: "skip",
	})
	_, _ = store.InsertScheduleRun(InsertScheduleRunParams{
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
	err := store.FinishScheduleRun(FinishScheduleRunParams{
		RunID: 999, Status: "succeeded", ExitCode: 0, FinishedAt: time.Now().UTC(),
	})
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

func ptrString(s string) *string { return &s }
func ptrBool(b bool) *bool       { return &b }
