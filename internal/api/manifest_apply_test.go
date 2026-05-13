package api

import (
	"errors"
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

func TestApplyManifestAppSettings_ExceedsMaxReplicas_ReturnsValidationError(t *testing.T) {
	srv, store, ownerID := newServerWithOwnedAppAndMaxReplicas(t, "alpha", 2)
	app, _ := store.GetAppBySlug("alpha")
	r := newAuthedManifestRequest(t, ownerID, "POST", "/api/apps/alpha/deploy")

	err := srv.applyManifestAppSettings(r, app, deploy.AppSettings{Replicas: ptrIntAPI(5)})
	if err == nil {
		t.Fatal("expected error")
	}
	var ve *validationError
	if !errors.As(err, &ve) {
		t.Errorf("expected *validationError, got %T: %v", err, err)
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
	if err := srv.applyManifestSchedules(r, app, specs); err != nil {
		t.Fatal(err)
	}
	first, _ := store.ListSchedulesByApp(app.ID)

	specs[0].Cron = "0 1 * * *"
	if err := srv.applyManifestSchedules(r, app, specs); err != nil {
		t.Fatal(err)
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
	if err := srv.applyManifestSchedules(r, app, specs); err != nil {
		t.Fatal(err)
	}
	all, _ := store.ListSchedulesByApp(app.ID)
	if len(all) != 2 {
		t.Errorf("expected 2 schedules (adhoc preserved + nightly); got %d", len(all))
	}
}

func TestApplyManifestSchedules_SchedulerNotStartedIsWarn(t *testing.T) {
	srv, store, ownerID := newServerWithOwnedApp_NoScheduler(t, "alpha")
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
	if err := srv.applyManifestSchedules(r, app, specs); err != nil {
		t.Errorf("scheduler-not-started must not fail apply: %v", err)
	}
}
