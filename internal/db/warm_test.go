package db_test

import (
	"testing"

	"github.com/rvben/shinyhub/internal/db"
)

// TestReplicaDesiredWarmConstant verifies the constant exists and has the
// correct string value that the warm-shrink path writes to desired_state.
func TestReplicaDesiredWarmConstant(t *testing.T) {
	if db.ReplicaDesiredWarm != "warm" {
		t.Errorf("ReplicaDesiredWarm = %q, want %q", db.ReplicaDesiredWarm, "warm")
	}
}

// TestUpdateAppMinWarmReplicas verifies the floor persists and is returned via
// GetAppBySlug. New apps start at the default of 0.
func TestUpdateAppMinWarmReplicas(t *testing.T) {
	store := mustOpenDB(t)
	u := mustCreateUser(t, store, "owner", "developer")

	// Default is 0.
	app := mustCreateApp(t, store, "warm-app", u.ID)
	if app.MinWarmReplicas != 0 {
		t.Errorf("fresh app.MinWarmReplicas = %d, want 0", app.MinWarmReplicas)
	}

	// Persist a non-zero floor.
	if err := store.UpdateAppMinWarmReplicas(app.ID, 2); err != nil {
		t.Fatalf("UpdateAppMinWarmReplicas: %v", err)
	}

	got, err := store.GetAppBySlug("warm-app")
	if err != nil {
		t.Fatalf("GetAppBySlug: %v", err)
	}
	if got.MinWarmReplicas != 2 {
		t.Errorf("MinWarmReplicas = %d, want 2", got.MinWarmReplicas)
	}

	// Reset back to zero.
	if err := store.UpdateAppMinWarmReplicas(app.ID, 0); err != nil {
		t.Fatalf("UpdateAppMinWarmReplicas(0): %v", err)
	}
	got, err = store.GetAppBySlug("warm-app")
	if err != nil {
		t.Fatalf("GetAppBySlug (reset): %v", err)
	}
	if got.MinWarmReplicas != 0 {
		t.Errorf("MinWarmReplicas after reset = %d, want 0", got.MinWarmReplicas)
	}
}

// TestUpsertReplica_DesiredStateWarm verifies that a replica upserted with
// DesiredState=ReplicaDesiredWarm round-trips correctly via ListReplicas.
func TestUpsertReplica_DesiredStateWarm(t *testing.T) {
	store := mustOpenDB(t)
	u := mustCreateUser(t, store, "owner", "developer")
	app := mustCreateApp(t, store, "warm-replica-app", u.ID)

	pid, port := 5001, 9001
	if err := store.UpsertReplica(db.UpsertReplicaParams{
		AppID:        app.ID,
		Index:        0,
		PID:          &pid,
		Port:         &port,
		Status:       "running",
		DesiredState: db.ReplicaDesiredWarm,
	}); err != nil {
		t.Fatalf("UpsertReplica: %v", err)
	}

	replicas, err := store.ListReplicas(app.ID)
	if err != nil {
		t.Fatalf("ListReplicas: %v", err)
	}
	if len(replicas) != 1 {
		t.Fatalf("expected 1 replica, got %d", len(replicas))
	}
	if replicas[0].DesiredState != db.ReplicaDesiredWarm {
		t.Errorf("DesiredState = %q, want %q", replicas[0].DesiredState, db.ReplicaDesiredWarm)
	}
}

// TestListWarmShrunkApps verifies the three-case filtering contract:
//
//   - shrunkRunning:  has a 'warm' desired_state replica + status 'running'   -> included
//   - shrunkStopped: has a 'warm' desired_state replica + status 'stopped'    -> excluded
//   - normalRunning: only 'running' desired_state replicas + status 'running' -> excluded
func TestListWarmShrunkApps(t *testing.T) {
	store := mustOpenDB(t)
	u := mustCreateUser(t, store, "owner", "developer")

	shrunkRunning := mustCreateApp(t, store, "shrunk-running", u.ID)
	shrunkStopped := mustCreateApp(t, store, "shrunk-stopped", u.ID)
	normalRunning := mustCreateApp(t, store, "normal-running", u.ID)

	// Set statuses.
	for slug, status := range map[string]string{
		"shrunk-running": "running",
		"shrunk-stopped": "stopped",
		"normal-running": "running",
	} {
		if err := store.UpdateAppStatus(db.UpdateAppStatusParams{Slug: slug, Status: status}); err != nil {
			t.Fatalf("UpdateAppStatus(%s, %s): %v", slug, status, err)
		}
	}

	// shrunk-running: one warm-parked replica.
	if err := store.UpsertReplica(db.UpsertReplicaParams{
		AppID:        shrunkRunning.ID,
		Index:        0,
		Status:       "running",
		DesiredState: db.ReplicaDesiredWarm,
	}); err != nil {
		t.Fatalf("UpsertReplica (shrunk-running warm): %v", err)
	}

	// shrunk-stopped: warm replica present but app is stopped -> not returned.
	if err := store.UpsertReplica(db.UpsertReplicaParams{
		AppID:        shrunkStopped.ID,
		Index:        0,
		Status:       "running",
		DesiredState: db.ReplicaDesiredWarm,
	}); err != nil {
		t.Fatalf("UpsertReplica (shrunk-stopped warm): %v", err)
	}

	// normal-running: running app but only desired_state='running' replicas.
	if err := store.UpsertReplica(db.UpsertReplicaParams{
		AppID:        normalRunning.ID,
		Index:        0,
		Status:       "running",
		DesiredState: "running",
	}); err != nil {
		t.Fatalf("UpsertReplica (normal-running): %v", err)
	}

	results, err := store.ListWarmShrunkApps()
	if err != nil {
		t.Fatalf("ListWarmShrunkApps: %v", err)
	}
	if len(results) != 1 {
		slugs := make([]string, len(results))
		for i, a := range results {
			slugs[i] = a.Slug
		}
		t.Fatalf("expected 1 result, got %d: %v", len(results), slugs)
	}
	if results[0].Slug != "shrunk-running" {
		t.Errorf("result[0].Slug = %q, want %q", results[0].Slug, "shrunk-running")
	}
}

// TestListWarmShrunkApps_DegradedIncluded verifies that apps with status
// 'degraded' (partially serving) are included in the warm-expansion set.
func TestListWarmShrunkApps_DegradedIncluded(t *testing.T) {
	store := mustOpenDB(t)
	u := mustCreateUser(t, store, "owner", "developer")

	degradedApp := mustCreateApp(t, store, "degraded-warm", u.ID)
	if err := store.UpdateAppStatus(db.UpdateAppStatusParams{Slug: "degraded-warm", Status: "degraded"}); err != nil {
		t.Fatalf("UpdateAppStatus: %v", err)
	}
	if err := store.UpsertReplica(db.UpsertReplicaParams{
		AppID:        degradedApp.ID,
		Index:        0,
		Status:       "running",
		DesiredState: db.ReplicaDesiredWarm,
	}); err != nil {
		t.Fatalf("UpsertReplica: %v", err)
	}

	results, err := store.ListWarmShrunkApps()
	if err != nil {
		t.Fatalf("ListWarmShrunkApps: %v", err)
	}
	if len(results) != 1 || results[0].Slug != "degraded-warm" {
		t.Errorf("expected [degraded-warm], got %v", results)
	}
}
