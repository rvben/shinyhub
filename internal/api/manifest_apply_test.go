package api

import (
	"fmt"
	"testing"

	"github.com/rvben/shinyhub/internal/db"
	"github.com/rvben/shinyhub/internal/deploy"
)

func TestApplyManifestAppSettings_UpdatesAllThreeFieldsAtomically(t *testing.T) {
	srv, store, ownerID := newServerWithOwnedApp(t, "alpha")
	app, _ := store.GetAppBySlug("alpha")
	r := newAuthedManifestRequest(t, ownerID, "POST", "/api/apps/alpha/deploy")

	err := srv.applyManifestAppSettings(r, app, deploy.AppSettings{
		HibernateTimeoutMinutes: ptrIntAPI(0),
		Replicas:                ptrIntAPI(3),
		MaxSessionsPerReplica:   ptrIntAPI(20),
	})
	if err != nil {
		t.Fatal(err)
	}

	got, _ := store.GetAppBySlug("alpha")
	if got.HibernateTimeoutMinutes == nil || *got.HibernateTimeoutMinutes != 0 {
		t.Errorf("hibernate = %v, want 0", got.HibernateTimeoutMinutes)
	}
	if got.Replicas != 3 {
		t.Errorf("replicas = %d, want 3", got.Replicas)
	}
	if got.MaxSessionsPerReplica != 20 {
		t.Errorf("max_sessions = %d, want 20", got.MaxSessionsPerReplica)
	}

	events, _ := store.ListAuditEvents(10, 0)
	if !auditEventsContain(events, "update_app", "alpha") {
		t.Errorf("expected update_app audit event for alpha")
	}
}

func TestApplyManifestAppSettings_HibernateResetSentinel(t *testing.T) {
	srv, store, ownerID := newServerWithOwnedApp(t, "alpha")
	app, _ := store.GetAppBySlug("alpha")
	r := newAuthedManifestRequest(t, ownerID, "POST", "/api/apps/alpha/deploy")

	// Set a concrete value first.
	_ = srv.applyManifestAppSettings(r, app, deploy.AppSettings{HibernateTimeoutMinutes: ptrIntAPI(7)})

	app, _ = store.GetAppBySlug("alpha")
	if err := srv.applyManifestAppSettings(r, app, deploy.AppSettings{HibernateResetToDefault: true}); err != nil {
		t.Fatal(err)
	}
	got, _ := store.GetAppBySlug("alpha")
	if got.HibernateTimeoutMinutes != nil {
		t.Errorf("expected nil (reset to default), got %v", got.HibernateTimeoutMinutes)
	}
}

func TestApplyManifestAppSettings_AbsentFieldsLeftAlone(t *testing.T) {
	srv, store, ownerID := newServerWithOwnedApp(t, "alpha")
	app, _ := store.GetAppBySlug("alpha")
	r := newAuthedManifestRequest(t, ownerID, "POST", "/api/apps/alpha/deploy")

	// Set all three.
	_ = srv.applyManifestAppSettings(r, app, deploy.AppSettings{
		HibernateTimeoutMinutes: ptrIntAPI(7),
		Replicas:                ptrIntAPI(2),
		MaxSessionsPerReplica:   ptrIntAPI(15),
	})
	app, _ = store.GetAppBySlug("alpha")

	// Update only replicas; hibernate and max_sessions must be untouched.
	if err := srv.applyManifestAppSettings(r, app, deploy.AppSettings{Replicas: ptrIntAPI(4)}); err != nil {
		t.Fatal(err)
	}
	got, _ := store.GetAppBySlug("alpha")
	if got.Replicas != 4 {
		t.Errorf("replicas = %d, want 4", got.Replicas)
	}
	if got.HibernateTimeoutMinutes == nil || *got.HibernateTimeoutMinutes != 7 {
		t.Errorf("hibernate clobbered: %v", got.HibernateTimeoutMinutes)
	}
	if got.MaxSessionsPerReplica != 15 {
		t.Errorf("max_sessions clobbered: %d", got.MaxSessionsPerReplica)
	}
}

func TestValidateManifestForServer_ExceedsMaxReplicas(t *testing.T) {
	srv, _, _ := newServerWithOwnedAppAndMaxReplicas(t, "alpha", 2)

	if ve := srv.validateManifestForServer(deploy.AppSettings{Replicas: ptrIntAPI(5)}); ve == nil {
		t.Fatal("expected validation error for replicas > MaxReplicas")
	}
}

func TestValidateManifestForServer_WithinPolicyPasses(t *testing.T) {
	srv, _, _ := newServerWithOwnedAppAndMaxReplicas(t, "alpha", 4)

	if ve := srv.validateManifestForServer(deploy.AppSettings{Replicas: ptrIntAPI(3)}); ve != nil {
		t.Errorf("unexpected validation error: %v", ve)
	}
}

func TestValidateManifestForServer_ZeroManifestIsNoop(t *testing.T) {
	srv, _, _ := newServerWithOwnedAppAndMaxReplicas(t, "alpha", 1)

	if ve := srv.validateManifestForServer(deploy.AppSettings{}); ve != nil {
		t.Errorf("expected nil for zero manifest, got %v", ve)
	}
}

func TestApplyManifestSchedules_UpsertsAndReusesID(t *testing.T) {
	srv, store, ownerID := newServerWithOwnedApp(t, "alpha")
	app, _ := store.GetAppBySlug("alpha")
	r := newAuthedManifestRequest(t, ownerID, "POST", "/api/apps/alpha/deploy")

	specs := []deploy.ScheduleSpec{{
		Name:           "nightly",
		Cron:           "0 0 * * *",
		Command:        []string{"echo", "a"},
		TimeoutSeconds: ptrIntAPI(60),
		Overlap:        "skip",
		Missed:         "skip",
	}}
	results, err := srv.applyManifestSchedules(r, app, specs)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 || results[0].Name != "nightly" || results[0].Action != "created" {
		t.Errorf("first apply results = %+v, want one {nightly created}", results)
	}
	first, _ := store.ListSchedulesByApp(app.ID)

	// First apply must record a schedule_create audit event.
	events, _ := store.ListAuditEvents(10, 0)
	scheduleID := fmt.Sprintf("%d", first[0].ID)
	if !auditEventsContain(events, "schedule_create", scheduleID) {
		t.Errorf("expected schedule_create audit event for schedule %s", scheduleID)
	}

	specs[0].Cron = "0 1 * * *"
	results, err = srv.applyManifestSchedules(r, app, specs)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 || results[0].Action != "updated" {
		t.Errorf("second apply results = %+v, want one {nightly updated}", results)
	}
	second, _ := store.ListSchedulesByApp(app.ID)
	if len(second) != 1 {
		t.Fatalf("duplicate schedules: %d", len(second))
	}
	if second[0].ID != first[0].ID {
		t.Errorf("upsert lost id: %d → %d", first[0].ID, second[0].ID)
	}
	if second[0].CronExpr != "0 1 * * *" {
		t.Errorf("cron not updated: %q", second[0].CronExpr)
	}

	// Second apply (cron changed) must record a schedule_update audit event.
	events, _ = store.ListAuditEvents(10, 0)
	if !auditEventsContain(events, "schedule_update", scheduleID) {
		t.Errorf("expected schedule_update audit event for schedule %s", scheduleID)
	}
}

func TestApplyManifestSchedules_LeavesOrphansAlone(t *testing.T) {
	srv, store, ownerID := newServerWithOwnedApp(t, "alpha")
	app, _ := store.GetAppBySlug("alpha")
	r := newAuthedManifestRequest(t, ownerID, "POST", "/api/apps/alpha/deploy")

	// Pre-create a schedule that is not in the manifest.
	_, _ = store.CreateSchedule(db.CreateScheduleParams{
		AppID:          app.ID,
		Name:           "adhoc",
		CronExpr:       "0 * * * *",
		CommandJSON:    `["echo","adhoc"]`,
		Enabled:        true,
		TimeoutSeconds: 60,
		OverlapPolicy:  "skip",
		MissedPolicy:   "skip",
	})

	specs := []deploy.ScheduleSpec{{
		Name:           "nightly",
		Cron:           "0 0 * * *",
		Command:        []string{"echo", "n"},
		TimeoutSeconds: ptrIntAPI(60),
		Overlap:        "skip",
		Missed:         "skip",
	}}
	if _, err := srv.applyManifestSchedules(r, app, specs); err != nil {
		t.Fatal(err)
	}
	all, _ := store.ListSchedulesByApp(app.ID)
	if len(all) != 2 {
		t.Errorf("expected 2 schedules (adhoc preserved + nightly); got %d", len(all))
	}
}

func TestApplyManifestSchedules_SchedulerNotStartedIsWarn(t *testing.T) {
	// newServerWithOwnedApp wires a non-nil scheduler that has NOT been
	// started, so scheduler.Reload → register returns scheduler.ErrNotStarted.
	// applyManifestSchedules must soft-fail (log a warning, not return an error)
	// and still persist the schedule row so it activates on the next Start.
	srv, store, ownerID := newServerWithOwnedApp(t, "alpha")
	app, _ := store.GetAppBySlug("alpha")
	r := newAuthedManifestRequest(t, ownerID, "POST", "/api/apps/alpha/deploy")

	specs := []deploy.ScheduleSpec{{
		Name:           "nightly",
		Cron:           "0 0 * * *",
		Command:        []string{"echo", "n"},
		TimeoutSeconds: ptrIntAPI(60),
		Overlap:        "skip",
		Missed:         "skip",
	}}
	if _, err := srv.applyManifestSchedules(r, app, specs); err != nil {
		t.Errorf("scheduler-not-started must not fail apply: %v", err)
	}

	// The row must still have been written — it will activate when Start is called.
	rows, err := store.ListSchedulesByApp(app.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].Name != "nightly" {
		t.Errorf("expected schedule row to be persisted; got %d rows", len(rows))
	}
}
