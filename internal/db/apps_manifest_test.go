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
