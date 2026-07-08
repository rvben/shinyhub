package api

import (
	"fmt"
	"strings"
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

	events, _ := store.ListAuditEvents("", 10, 0)
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
	srv, store, _ := newServerWithOwnedAppAndMaxReplicas(t, "alpha", 2)
	app, _ := store.GetAppBySlug("alpha")

	if ve := srv.validateManifestForServer(app, deploy.AppSettings{Replicas: ptrIntAPI(5)}); ve == nil {
		t.Fatal("expected validation error for replicas > MaxReplicas")
	}
}

func TestValidateManifestForServer_WithinPolicyPasses(t *testing.T) {
	srv, store, _ := newServerWithOwnedAppAndMaxReplicas(t, "alpha", 4)
	app, _ := store.GetAppBySlug("alpha")

	if ve := srv.validateManifestForServer(app, deploy.AppSettings{Replicas: ptrIntAPI(3)}); ve != nil {
		t.Errorf("unexpected validation error: %v", ve)
	}
}

func TestValidateManifestForServer_ZeroManifestIsNoop(t *testing.T) {
	srv, store, _ := newServerWithOwnedAppAndMaxReplicas(t, "alpha", 1)
	app, _ := store.GetAppBySlug("alpha")

	if ve := srv.validateManifestForServer(app, deploy.AppSettings{}); ve != nil {
		t.Errorf("expected nil for zero manifest, got %v", ve)
	}
}

// TestValidateManifestForServer_RejectsReplicasWhenPlacementStored proves a
// manifest that sets [app].replicas is rejected when the app already carries a
// tier placement. Honoring the manifest replicas would leave app.Replicas
// drifting from the placement sum, because deploy.Run derives the pool from
// the stored placement and ignores the replicas column.
func TestValidateManifestForServer_RejectsReplicasWhenPlacementStored(t *testing.T) {
	srv, store, _ := newServerWithOwnedApp(t, "alpha")
	app, _ := store.GetAppBySlug("alpha")
	if err := store.SetAppPlacement(app.ID, `{"local":2}`, 2); err != nil {
		t.Fatal(err)
	}
	app, _ = store.GetAppBySlug("alpha")

	if ve := srv.validateManifestForServer(app, deploy.AppSettings{Replicas: ptrIntAPI(5)}); ve == nil {
		t.Fatal("expected validation error: manifest replicas vs stored tier placement")
	}
}

// TestValidateManifestForServer_PlacementWithoutReplicasPasses proves a
// manifest that leaves replicas untouched is accepted even when a placement is
// stored — only a replicas change conflicts with placement.
func TestValidateManifestForServer_PlacementWithoutReplicasPasses(t *testing.T) {
	srv, store, _ := newServerWithOwnedApp(t, "alpha")
	app, _ := store.GetAppBySlug("alpha")
	if err := store.SetAppPlacement(app.ID, `{"local":2}`, 2); err != nil {
		t.Fatal(err)
	}
	app, _ = store.GetAppBySlug("alpha")

	if ve := srv.validateManifestForServer(app, deploy.AppSettings{MaxSessionsPerReplica: ptrIntAPI(10)}); ve != nil {
		t.Errorf("unexpected validation error: %v", ve)
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
	events, _ := store.ListAuditEvents("", 10, 0)
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
	events, _ = store.ListAuditEvents("", 10, 0)
	if !auditEventsContain(events, "schedule_update", scheduleID) {
		t.Errorf("expected schedule_update audit event for schedule %s", scheduleID)
	}
}

// TestApplyManifestSchedules_AuditDetailIncludesEffectiveTimezone asserts that
// the audit event emitted by applyManifestSchedules includes effective_timezone
// in its JSON detail — matching the shape emitted by the API create handler.
func TestApplyManifestSchedules_AuditDetailIncludesEffectiveTimezone(t *testing.T) {
	srv, store, ownerID := newServerWithOwnedApp(t, "alpha")
	app, _ := store.GetAppBySlug("alpha")
	r := newAuthedManifestRequest(t, ownerID, "POST", "/api/apps/alpha/deploy")

	tz := "Europe/Amsterdam"
	specs := []deploy.ScheduleSpec{{
		Name:           "nightly",
		Cron:           "0 0 * * *",
		Command:        []string{"echo", "a"},
		TimeoutSeconds: ptrIntAPI(60),
		Overlap:        "skip",
		Missed:         "skip",
		Timezone:       tz,
	}}
	if _, err := srv.applyManifestSchedules(r, app, specs); err != nil {
		t.Fatal(err)
	}

	events, _ := store.ListAuditEvents("", 10, 0)
	var found bool
	for _, e := range events {
		if e.Action == "schedule_create" {
			if !strings.Contains(e.Detail, `"effective_timezone"`) {
				t.Errorf("audit detail missing effective_timezone: %q", e.Detail)
			}
			if !strings.Contains(e.Detail, "Europe/Amsterdam") {
				t.Errorf("audit detail missing timezone value: %q", e.Detail)
			}
			found = true
			break
		}
	}
	if !found {
		t.Error("no schedule_create audit event found")
	}
}

// TestApplyManifestSchedules_AuditDetailEffectiveTimezoneInherited asserts that
// when no per-schedule timezone is set, the audit detail effective_timezone
// reflects the server default (UTC in test fixture).
func TestApplyManifestSchedules_AuditDetailEffectiveTimezoneInherited(t *testing.T) {
	srv, store, ownerID := newServerWithOwnedApp(t, "alpha")
	app, _ := store.GetAppBySlug("alpha")
	r := newAuthedManifestRequest(t, ownerID, "POST", "/api/apps/alpha/deploy")

	// No timezone in spec — should inherit server default (UTC in fixture).
	specs := []deploy.ScheduleSpec{{
		Name:           "nightly",
		Cron:           "0 0 * * *",
		Command:        []string{"echo", "a"},
		TimeoutSeconds: ptrIntAPI(60),
		Overlap:        "skip",
		Missed:         "skip",
	}}
	if _, err := srv.applyManifestSchedules(r, app, specs); err != nil {
		t.Fatal(err)
	}

	events, _ := store.ListAuditEvents("", 10, 0)
	var found bool
	for _, e := range events {
		if e.Action == "schedule_create" {
			if !strings.Contains(e.Detail, `"effective_timezone"`) {
				t.Errorf("audit detail missing effective_timezone: %q", e.Detail)
			}
			// UTC is the server default in the test fixture.
			if !strings.Contains(e.Detail, "UTC") {
				t.Errorf("audit detail should include UTC (server default): %q", e.Detail)
			}
			found = true
			break
		}
	}
	if !found {
		t.Error("no schedule_create audit event found")
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

func TestApplyManifestAppSettings_IdentityHeadersAlwaysReconciled(t *testing.T) {
	f := false
	srv, store, ownerID := newServerWithOwnedApp(t, "alpha")
	app, _ := store.GetAppBySlug("alpha")
	r := newAuthedManifestRequest(t, ownerID, "POST", "/api/apps/alpha/deploy")

	// Declared: identity_headers = false => column must be written non-nil false.
	if err := srv.applyManifestAppSettings(r, app, deploy.AppSettings{IdentityHeaders: &f}); err != nil {
		t.Fatal(err)
	}
	got, _ := store.GetAppBySlug("alpha")
	if got.IdentityHeaders == nil || *got.IdentityHeaders != false {
		t.Errorf("after declare false: identity_headers = %v, want non-nil false", got.IdentityHeaders)
	}

	// Absent on next apply (empty AppSettings) => column must revert to NULL.
	app, _ = store.GetAppBySlug("alpha")
	if err := srv.applyManifestAppSettings(r, app, deploy.AppSettings{}); err != nil {
		t.Fatal(err)
	}
	got, _ = store.GetAppBySlug("alpha")
	if got.IdentityHeaders != nil {
		t.Errorf("after absent key: identity_headers = %v, want nil (NULL)", got.IdentityHeaders)
	}
}

// TestApplyManifestAppSettings_ZeroAppNoAuditEvent asserts that applying a
// zero AppSettings (manifest present but [app] section empty/absent) reconciles
// identity_headers to NULL but does NOT emit an update_app audit event. Audit
// is gated on !m.IsZero() to avoid noise on every manifest-present deploy that
// omits the [app] section.
func TestApplyManifestAppSettings_ZeroAppNoAuditEvent(t *testing.T) {
	srv, store, ownerID := newServerWithOwnedApp(t, "alpha")
	app, _ := store.GetAppBySlug("alpha")
	r := newAuthedManifestRequest(t, ownerID, "POST", "/api/apps/alpha/deploy")

	if err := srv.applyManifestAppSettings(r, app, deploy.AppSettings{}); err != nil {
		t.Fatal(err)
	}

	// identity_headers column must be NULL (written unconditionally).
	got, _ := store.GetAppBySlug("alpha")
	if got.IdentityHeaders != nil {
		t.Errorf("identity_headers = %v, want nil (NULL)", got.IdentityHeaders)
	}

	// No update_app audit event must have been emitted for this no-op apply.
	events, _ := store.ListAuditEvents("", 10, 0)
	if auditEventsContain(events, "update_app", "alpha") {
		t.Error("unexpected update_app audit event for zero-App manifest apply")
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

// TestApplyManifestAppSettings_MinWarmReplicasPersisted confirms that
// min_warm_replicas declared in the [app] section is written to the DB.
func TestApplyManifestAppSettings_MinWarmReplicasPersisted(t *testing.T) {
	srv, store, ownerID := newServerWithOwnedApp(t, "alpha")
	app, _ := store.GetAppBySlug("alpha")
	r := newAuthedManifestRequest(t, ownerID, "POST", "/api/apps/alpha/deploy")

	if err := srv.applyManifestAppSettings(r, app, deploy.AppSettings{
		MinWarmReplicas: ptrIntAPI(2),
	}); err != nil {
		t.Fatal(err)
	}

	got, _ := store.GetAppBySlug("alpha")
	if got.MinWarmReplicas != 2 {
		t.Errorf("MinWarmReplicas = %d, want 2", got.MinWarmReplicas)
	}
}

// TestApplyManifestAppSettings_MinWarmReplicasAbsentLeavesStoredValue confirms
// declared-only semantics: when min_warm_replicas is absent from the manifest,
// a previously stored value is not cleared.
func TestApplyManifestAppSettings_MinWarmReplicasAbsentLeavesStoredValue(t *testing.T) {
	srv, store, ownerID := newServerWithOwnedApp(t, "alpha")
	app, _ := store.GetAppBySlug("alpha")
	r := newAuthedManifestRequest(t, ownerID, "POST", "/api/apps/alpha/deploy")

	// Persist an initial value via the store directly.
	if err := store.UpdateAppMinWarmReplicas(app.ID, 3); err != nil {
		t.Fatalf("seed: %v", err)
	}
	app, _ = store.GetAppBySlug("alpha")

	// Apply with min_warm_replicas absent; the stored value must be untouched.
	if err := srv.applyManifestAppSettings(r, app, deploy.AppSettings{}); err != nil {
		t.Fatal(err)
	}

	got, _ := store.GetAppBySlug("alpha")
	if got.MinWarmReplicas != 3 {
		t.Errorf("MinWarmReplicas = %d after absent-key apply, want 3 (untouched)", got.MinWarmReplicas)
	}
}

// TestValidateManifestForServer_ClusteredRejectsPerSession confirms that a
// per_session worker isolation manifest is rejected on a clustered (Postgres)
// server, matching the existing PATCH /api/apps path.
func TestValidateManifestForServer_ClusteredRejectsPerSession(t *testing.T) {
	srv, store, _ := newServerWithOwnedApp(t, "alpha")
	srv.SetCluster("test-instance")
	app, _ := store.GetAppBySlug("alpha")

	iso := "per_session"
	maxWorkers := 2
	m := deploy.AppSettings{
		Worker: &deploy.WorkerManifest{
			Isolation:  &iso,
			MaxWorkers: &maxWorkers,
		},
	}
	verr := srv.validateManifestForServer(app, m)
	if verr == nil {
		t.Fatal("expected validation error for per_session on clustered server, got nil")
	}
	if !strings.Contains(verr.Error(), "clustered") && !strings.Contains(verr.Error(), "Postgres") {
		t.Errorf("expected clustered/Postgres mention in error, got: %v", verr)
	}
}

// TestValidateManifestForServer_WorkerGroupedValidPasses confirms that a valid
// grouped worker block passes server-side validation on a non-clustered server.
func TestValidateManifestForServer_WorkerGroupedValidPasses(t *testing.T) {
	srv, store, _ := newServerWithOwnedApp(t, "alpha")
	app, _ := store.GetAppBySlug("alpha")

	iso := "grouped"
	groupedSize := 5
	maxWorkers := 2
	m := deploy.AppSettings{
		Worker: &deploy.WorkerManifest{
			Isolation:   &iso,
			GroupedSize: &groupedSize,
			MaxWorkers:  &maxWorkers,
		},
	}
	if verr := srv.validateManifestForServer(app, m); verr != nil {
		t.Errorf("expected valid grouped worker block to pass, got: %v", verr)
	}
}

// TestValidateManifestForServer_WorkerBudgetMergesStoredState pins that the
// worker budget math validates the POST-deploy state: stored worker columns
// overlaid with the declared manifest fields, isolation resolved through the
// fleet default, and a declared memory limit replacing the stored one.
func TestValidateManifestForServer_WorkerBudgetMergesStoredState(t *testing.T) {
	srv, store, _ := newServerWithOwnedAppCfg(t, "alpha", manifestServerCfg{
		DefaultWorkerIsolation: "per_session",
		HostBudgetMB:           2000,
	})
	app, _ := store.GetAppBySlug("alpha")
	// Clear the stored isolation to "" so the app inherits the fleet default
	// (created apps start at the explicit 'multiplex' column default).
	if err := store.ApplyAppManifestSettings(db.ApplyAppManifestSettingsParams{
		AppID: app.ID, SetWorkerIsolation: true, WorkerIsolation: "",
	}); err != nil {
		t.Fatal(err)
	}
	app, _ = store.GetAppBySlug("alpha")

	// Stored isolation is empty (inherits per_session); the manifest declares
	// max_workers and a memory limit whose worst case busts the budget:
	// 4 x (600 + 150) = 3000 > 2000.
	ve := srv.validateManifestForServer(app, deploy.AppSettings{
		MemoryLimitMB: ptrIntAPI(600),
		Worker:        &deploy.WorkerManifest{MaxWorkers: ptrIntAPI(4)},
	})
	if ve == nil {
		t.Fatal("expected validation error for a budget-busting inherited-elastic manifest")
	}

	// The same manifest fits when the budget can hold it: 2 x (600+150) = 1500.
	ve = srv.validateManifestForServer(app, deploy.AppSettings{
		MemoryLimitMB: ptrIntAPI(600),
		Worker:        &deploy.WorkerManifest{MaxWorkers: ptrIntAPI(2)},
	})
	if ve != nil {
		t.Errorf("expected the fitting manifest to pass, got %v", ve)
	}
}
