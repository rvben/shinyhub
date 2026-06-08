package db_test

import (
	"testing"

	"github.com/rvben/shinyhub/internal/db"
	"github.com/rvben/shinyhub/internal/dbtest"
)

// TestSetReplicaDesiredState exercises SetReplicaDesiredState on both SQLite
// (always) and Postgres (when SHINYHUB_TEST_POSTGRES_DSN is set, handled
// transparently by dbtest.New).
func TestSetReplicaDesiredState(t *testing.T) {
	s := dbtest.New(t)

	owner := mustCreateUser(t, s, "ds-owner", "developer")
	app := mustCreateApp(t, s, "ds-app", owner.ID)
	appID := app.ID

	depID := int64(0)
	pid := 1001
	port := 9001

	// Seed a replica row with desired_state='running'.
	if err := s.UpsertReplica(db.UpsertReplicaParams{
		AppID:        appID,
		Index:        0,
		PID:          &pid,
		Port:         &port,
		Status:       "running",
		Provider:     "native",
		Tier:         "default",
		AppVersion:   "v1",
		DesiredState: "running",
		DeploymentID: &depID,
	}); err != nil {
		t.Fatalf("UpsertReplica: %v", err)
	}

	// Verify initial desired_state is 'running'.
	reps, err := s.ListReplicas(appID)
	if err != nil {
		t.Fatalf("ListReplicas: %v", err)
	}
	if len(reps) != 1 {
		t.Fatalf("expected 1 replica, got %d", len(reps))
	}
	if reps[0].DesiredState != "running" {
		t.Errorf("initial desired_state = %q; want 'running'", reps[0].DesiredState)
	}

	// -- Set to 'draining' --
	if err := s.SetReplicaDesiredState(appID, 0, "draining"); err != nil {
		t.Fatalf("SetReplicaDesiredState draining: %v", err)
	}
	reps, err = s.ListReplicas(appID)
	if err != nil {
		t.Fatalf("ListReplicas after draining: %v", err)
	}
	if reps[0].DesiredState != "draining" {
		t.Errorf("desired_state after set draining = %q; want 'draining'", reps[0].DesiredState)
	}

	// -- Revert to 'running' --
	if err := s.SetReplicaDesiredState(appID, 0, "running"); err != nil {
		t.Fatalf("SetReplicaDesiredState running: %v", err)
	}
	reps, err = s.ListReplicas(appID)
	if err != nil {
		t.Fatalf("ListReplicas after running: %v", err)
	}
	if reps[0].DesiredState != "running" {
		t.Errorf("desired_state after revert = %q; want 'running'", reps[0].DesiredState)
	}

	// -- No-op on non-existent (app_id, idx): must not error --
	if err := s.SetReplicaDesiredState(appID, 99, "draining"); err != nil {
		t.Errorf("SetReplicaDesiredState on non-existent row returned error: %v", err)
	}

	// -- Wrong appID: must not affect our row --
	if err := s.SetReplicaDesiredState(appID+999, 0, "draining"); err != nil {
		t.Errorf("SetReplicaDesiredState with wrong appID returned error: %v", err)
	}
	reps, err = s.ListReplicas(appID)
	if err != nil {
		t.Fatalf("ListReplicas after wrong appID: %v", err)
	}
	if reps[0].DesiredState != "running" {
		t.Errorf("desired_state changed by wrong-appID call = %q; want 'running'", reps[0].DesiredState)
	}
}
