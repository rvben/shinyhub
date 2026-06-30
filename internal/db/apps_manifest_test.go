package db_test

import (
	"testing"

	"github.com/rvben/shinyhub/internal/db"
)

func ptrInt(v int) *int { return &v }

// TestApplyAppManifestSettings_AllThreeFields verifies that all three manifest
// settings (hibernate, replicas, max_sessions_per_replica) are written in one
// round-trip and all values stick.
func TestApplyAppManifestSettings_AllThreeFields(t *testing.T) {
	store := mustOpenDB(t)
	u := mustCreateUser(t, store, "owner", "developer")
	app := mustCreateApp(t, store, "alpha", u.ID)

	err := store.ApplyAppManifestSettings(db.ApplyAppManifestSettingsParams{
		AppID:                    app.ID,
		Slug:                     "alpha",
		SetHibernate:             true,
		HibernateMinutes:         ptrInt(10),
		SetReplicas:              true,
		Replicas:                 3,
		PreviousReplicas:         app.Replicas,
		SetMaxSessionsPerReplica: true,
		MaxSessionsPerReplica:    15,
	})
	if err != nil {
		t.Fatal(err)
	}

	got, err := store.GetAppBySlug("alpha")
	if err != nil {
		t.Fatal(err)
	}
	if got.HibernateTimeoutMinutes == nil || *got.HibernateTimeoutMinutes != 10 {
		t.Errorf("hibernate = %v, want 10", got.HibernateTimeoutMinutes)
	}
	if got.Replicas != 3 {
		t.Errorf("replicas = %d, want 3", got.Replicas)
	}
	if got.MaxSessionsPerReplica != 15 {
		t.Errorf("max_sessions_per_replica = %d, want 15", got.MaxSessionsPerReplica)
	}
}

// TestApplyAppManifestSettings_HibernateResetToNull verifies that passing
// SetHibernate=true with HibernateMinutes=nil clears the column to NULL
// (reset-to-default sentinel).
func TestApplyAppManifestSettings_HibernateResetToNull(t *testing.T) {
	store := mustOpenDB(t)
	u := mustCreateUser(t, store, "owner", "developer")
	app := mustCreateApp(t, store, "alpha", u.ID)

	// First set a concrete value.
	if err := store.ApplyAppManifestSettings(db.ApplyAppManifestSettingsParams{
		AppID: app.ID, Slug: "alpha",
		SetHibernate: true, HibernateMinutes: ptrInt(30),
	}); err != nil {
		t.Fatal(err)
	}

	// Then reset to NULL.
	if err := store.ApplyAppManifestSettings(db.ApplyAppManifestSettingsParams{
		AppID: app.ID, Slug: "alpha",
		SetHibernate: true, HibernateMinutes: nil,
	}); err != nil {
		t.Fatal(err)
	}

	got, err := store.GetAppBySlug("alpha")
	if err != nil {
		t.Fatal(err)
	}
	if got.HibernateTimeoutMinutes != nil {
		t.Errorf("expected nil (reset to default), got %v", got.HibernateTimeoutMinutes)
	}
}

// TestApplyAppManifestSettings_ReplicaShrink verifies that shrinking the
// replica count deletes the orphaned replica rows and updates apps.replicas
// atomically in the same transaction.
func TestApplyAppManifestSettings_ReplicaShrink(t *testing.T) {
	store := mustOpenDB(t)
	u := mustCreateUser(t, store, "owner", "developer")
	app := mustCreateApp(t, store, "alpha", u.ID)

	// Seed two replica rows (idx=0 and idx=1).
	for _, idx := range []int{0, 1} {
		pid, port := idx+1000, idx+9000
		if err := store.UpsertReplica(db.UpsertReplicaParams{
			AppID:  app.ID,
			Index:  idx,
			PID:    &pid,
			Port:   &port,
			Status: "running",
		}); err != nil {
			t.Fatalf("seed replica idx=%d: %v", idx, err)
		}
	}

	// Set apps.replicas = 2 so PreviousReplicas is accurate.
	if err := store.ApplyAppManifestSettings(db.ApplyAppManifestSettingsParams{
		AppID: app.ID, Slug: "alpha",
		SetReplicas: true, Replicas: 2, PreviousReplicas: 1,
	}); err != nil {
		t.Fatal(err)
	}

	// Shrink to 1 replica — idx=1 must be pruned.
	if err := store.ApplyAppManifestSettings(db.ApplyAppManifestSettingsParams{
		AppID: app.ID, Slug: "alpha",
		SetReplicas: true, Replicas: 1, PreviousReplicas: 2,
	}); err != nil {
		t.Fatal(err)
	}

	got, err := store.GetAppBySlug("alpha")
	if err != nil {
		t.Fatal(err)
	}
	if got.Replicas != 1 {
		t.Errorf("apps.replicas = %d, want 1", got.Replicas)
	}

	replicas, err := store.ListReplicas(app.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(replicas) != 1 {
		t.Fatalf("expected 1 replica row, got %d", len(replicas))
	}
	if replicas[0].Index != 0 {
		t.Errorf("surviving replica idx = %d, want 0", replicas[0].Index)
	}
}

// TestApplyAppManifestSettings_AbsentFieldsUntouched verifies that fields
// with their Set* flag false are not modified.
func TestApplyAppManifestSettings_AbsentFieldsUntouched(t *testing.T) {
	store := mustOpenDB(t)
	u := mustCreateUser(t, store, "owner", "developer")
	app := mustCreateApp(t, store, "alpha", u.ID)

	// Set all three fields.
	if err := store.ApplyAppManifestSettings(db.ApplyAppManifestSettingsParams{
		AppID: app.ID, Slug: "alpha",
		SetHibernate: true, HibernateMinutes: ptrInt(7),
		SetReplicas: true, Replicas: 2, PreviousReplicas: app.Replicas,
		SetMaxSessionsPerReplica: true, MaxSessionsPerReplica: 15,
	}); err != nil {
		t.Fatal(err)
	}

	// Apply only replicas change — other fields must survive.
	if err := store.ApplyAppManifestSettings(db.ApplyAppManifestSettingsParams{
		AppID: app.ID, Slug: "alpha",
		SetReplicas: true, Replicas: 4, PreviousReplicas: 2,
	}); err != nil {
		t.Fatal(err)
	}

	got, err := store.GetAppBySlug("alpha")
	if err != nil {
		t.Fatal(err)
	}
	if got.Replicas != 4 {
		t.Errorf("replicas = %d, want 4", got.Replicas)
	}
	if got.HibernateTimeoutMinutes == nil || *got.HibernateTimeoutMinutes != 7 {
		t.Errorf("hibernate clobbered: %v", got.HibernateTimeoutMinutes)
	}
	if got.MaxSessionsPerReplica != 15 {
		t.Errorf("max_sessions_per_replica clobbered: %d", got.MaxSessionsPerReplica)
	}
}

// TestApplyAppManifestSettings_ResourceLimits verifies that memory_limit_mb and
// cpu_quota_percent are written when their Set* flag is set, left untouched when
// it is not, and cleared to NULL when the pointer is nil (the failed-deploy
// revert restores a pre-manifest NULL this way).
func TestApplyAppManifestSettings_ResourceLimits(t *testing.T) {
	store := mustOpenDB(t)
	u := mustCreateUser(t, store, "owner", "developer")
	app := mustCreateApp(t, store, "alpha", u.ID)

	// Set both limits.
	if err := store.ApplyAppManifestSettings(db.ApplyAppManifestSettingsParams{
		AppID: app.ID, Slug: "alpha",
		SetMemoryLimitMB: true, MemoryLimitMB: ptrInt(2048),
		SetCPUQuotaPercent: true, CPUQuotaPercent: ptrInt(150),
	}); err != nil {
		t.Fatal(err)
	}
	got, err := store.GetAppBySlug("alpha")
	if err != nil {
		t.Fatal(err)
	}
	if got.MemoryLimitMB == nil || *got.MemoryLimitMB != 2048 {
		t.Errorf("memory_limit_mb = %v, want 2048", got.MemoryLimitMB)
	}
	if got.CPUQuotaPercent == nil || *got.CPUQuotaPercent != 150 {
		t.Errorf("cpu_quota_percent = %v, want 150", got.CPUQuotaPercent)
	}

	// Change only memory; cpu must survive (declared-only, like replicas).
	if err := store.ApplyAppManifestSettings(db.ApplyAppManifestSettingsParams{
		AppID: app.ID, Slug: "alpha",
		SetMemoryLimitMB: true, MemoryLimitMB: ptrInt(512),
	}); err != nil {
		t.Fatal(err)
	}
	got, err = store.GetAppBySlug("alpha")
	if err != nil {
		t.Fatal(err)
	}
	if got.MemoryLimitMB == nil || *got.MemoryLimitMB != 512 {
		t.Errorf("memory_limit_mb = %v, want 512", got.MemoryLimitMB)
	}
	if got.CPUQuotaPercent == nil || *got.CPUQuotaPercent != 150 {
		t.Errorf("cpu_quota_percent clobbered: %v, want 150", got.CPUQuotaPercent)
	}

	// Revert path: a nil pointer with Set=true clears the column to NULL.
	if err := store.ApplyAppManifestSettings(db.ApplyAppManifestSettingsParams{
		AppID: app.ID, Slug: "alpha",
		SetMemoryLimitMB: true, MemoryLimitMB: nil,
		SetCPUQuotaPercent: true, CPUQuotaPercent: nil,
	}); err != nil {
		t.Fatal(err)
	}
	got, err = store.GetAppBySlug("alpha")
	if err != nil {
		t.Fatal(err)
	}
	if got.MemoryLimitMB != nil {
		t.Errorf("memory_limit_mb = %v, want nil (NULL)", got.MemoryLimitMB)
	}
	if got.CPUQuotaPercent != nil {
		t.Errorf("cpu_quota_percent = %v, want nil (NULL)", got.CPUQuotaPercent)
	}
}

// TestApplyAppManifestSettings_IdentityHeadersTriState verifies the tri-state
// reconcile semantics for identity_headers:
//   - SetIdentityHeaders=true + non-nil false => column set to false
//   - SetIdentityHeaders=true + nil => column reset to NULL (inherit global)
//   - SetIdentityHeaders=false => column untouched
func TestApplyAppManifestSettings_IdentityHeadersTriState(t *testing.T) {
	store := mustOpenDB(t)
	u := mustCreateUser(t, store, "owner", "developer")
	app := mustCreateApp(t, store, "alpha", u.ID)

	// Set to explicit false.
	if err := store.ApplyAppManifestSettings(db.ApplyAppManifestSettingsParams{
		AppID:              app.ID,
		Slug:               "alpha",
		SetIdentityHeaders: true,
		IdentityHeaders:    ptrBool(false),
	}); err != nil {
		t.Fatal(err)
	}
	got, err := store.GetAppBySlug("alpha")
	if err != nil {
		t.Fatal(err)
	}
	if got.IdentityHeaders == nil || *got.IdentityHeaders != false {
		t.Errorf("identity_headers = %v, want non-nil false", got.IdentityHeaders)
	}

	// Reset to NULL (key removed from manifest => inherit global).
	if err := store.ApplyAppManifestSettings(db.ApplyAppManifestSettingsParams{
		AppID:              app.ID,
		Slug:               "alpha",
		SetIdentityHeaders: true,
		IdentityHeaders:    nil,
	}); err != nil {
		t.Fatal(err)
	}
	got, err = store.GetAppBySlug("alpha")
	if err != nil {
		t.Fatal(err)
	}
	if got.IdentityHeaders != nil {
		t.Errorf("identity_headers = %v, want nil (NULL)", got.IdentityHeaders)
	}

	// With SetIdentityHeaders=false the column must not be touched.
	// First write a known value, then call without the flag, then verify.
	if err := store.ApplyAppManifestSettings(db.ApplyAppManifestSettingsParams{
		AppID:              app.ID,
		Slug:               "alpha",
		SetIdentityHeaders: true,
		IdentityHeaders:    ptrBool(true),
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.ApplyAppManifestSettings(db.ApplyAppManifestSettingsParams{
		AppID:              app.ID,
		Slug:               "alpha",
		SetIdentityHeaders: false,
		// IdentityHeaders intentionally omitted; column must survive.
	}); err != nil {
		t.Fatal(err)
	}
	got, err = store.GetAppBySlug("alpha")
	if err != nil {
		t.Fatal(err)
	}
	if got.IdentityHeaders == nil || *got.IdentityHeaders != true {
		t.Errorf("identity_headers clobbered: %v, want non-nil true", got.IdentityHeaders)
	}
}

// TestListRoutableReplicas_CarriesIdentityHeaders verifies that
// ListRoutableReplicas carries apps.identity_headers through the JOIN so the
// pool syncer can apply per-app overrides without a second query.
func TestListRoutableReplicas_CarriesIdentityHeaders(t *testing.T) {
	store := mustOpenDB(t)
	owner := mustCreateUser(t, store, "owner", "admin")

	// appWith: identity_headers = false (explicit override via reconcile)
	appWith := mustCreateApp(t, store, "with-identity", owner.ID)
	if err := store.ApplyAppManifestSettings(db.ApplyAppManifestSettingsParams{
		AppID:              appWith.ID,
		Slug:               "with-identity",
		SetIdentityHeaders: true,
		IdentityHeaders:    ptrBool(false),
	}); err != nil {
		t.Fatalf("set identity_headers: %v", err)
	}
	if err := store.UpsertReplica(db.UpsertReplicaParams{
		AppID:       appWith.ID,
		Index:       0,
		Status:      db.ReplicaStatusRunning,
		EndpointURL: "http://192.0.2.1:9000",
		Provider:    "fargate",
		Tier:        "fargate",
	}); err != nil {
		t.Fatalf("UpsertReplica (appWith): %v", err)
	}

	// appNull: identity_headers = NULL (inherit global)
	appNull := mustCreateApp(t, store, "null-identity", owner.ID)
	if err := store.UpsertReplica(db.UpsertReplicaParams{
		AppID:       appNull.ID,
		Index:       0,
		Status:      db.ReplicaStatusRunning,
		EndpointURL: "http://192.0.2.2:9000",
		Provider:    "fargate",
		Tier:        "fargate",
	}); err != nil {
		t.Fatalf("UpsertReplica (appNull): %v", err)
	}

	rows, err := store.ListRoutableReplicas()
	if err != nil {
		t.Fatalf("ListRoutableReplicas: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("expected 2 routable replicas, got %d", len(rows))
	}

	bySlug := map[string]db.RoutableReplica{}
	for _, rr := range rows {
		bySlug[rr.Slug] = rr
	}

	withRR, ok := bySlug["with-identity"]
	if !ok {
		t.Fatal("with-identity replica missing from routable set")
	}
	if withRR.AppIdentityHeaders == nil || *withRR.AppIdentityHeaders != false {
		t.Errorf("with-identity AppIdentityHeaders = %v, want non-nil false", withRR.AppIdentityHeaders)
	}

	nullRR, ok := bySlug["null-identity"]
	if !ok {
		t.Fatal("null-identity replica missing from routable set")
	}
	if nullRR.AppIdentityHeaders != nil {
		t.Errorf("null-identity AppIdentityHeaders = %v, want nil", nullRR.AppIdentityHeaders)
	}
}
