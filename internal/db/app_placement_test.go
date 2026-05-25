package db_test

import (
	"testing"

	"github.com/rvben/shinyhub/internal/db"
)

func TestUpsertReplica_PersistsDeploymentID(t *testing.T) {
	store := mustOpenDB(t)
	u := mustCreateUser(t, store, "owner", "developer")
	app := mustCreateApp(t, store, "demo", u.ID)
	depID := int64(42)
	if err := store.UpsertReplica(db.UpsertReplicaParams{
		AppID: app.ID, Index: 0, Status: "running", Tier: "burst",
		Provider: "docker", DeploymentID: &depID,
	}); err != nil {
		t.Fatalf("UpsertReplica: %v", err)
	}
	reps, err := store.ListReplicas(app.ID)
	if err != nil || len(reps) != 1 {
		t.Fatalf("ListReplicas: %v len=%d", err, len(reps))
	}
	if reps[0].DeploymentID == nil || *reps[0].DeploymentID != 42 {
		t.Fatalf("DeploymentID = %v, want 42", reps[0].DeploymentID)
	}
}

func TestUpsertReplica_NilDeploymentID(t *testing.T) {
	store := mustOpenDB(t)
	u := mustCreateUser(t, store, "owner", "developer")
	app := mustCreateApp(t, store, "demo", u.ID)
	if err := store.UpsertReplica(db.UpsertReplicaParams{
		AppID: app.ID, Index: 0, Status: "running",
	}); err != nil {
		t.Fatalf("UpsertReplica: %v", err)
	}
	reps, _ := store.ListReplicas(app.ID)
	if len(reps) != 1 || reps[0].DeploymentID != nil {
		t.Fatalf("expected nil DeploymentID, got %v", reps[0].DeploymentID)
	}
}

func TestSetAppPlacement_RoundTrip(t *testing.T) {
	store := mustOpenDB(t)
	u := mustCreateUser(t, store, "owner", "developer")
	app := mustCreateApp(t, store, "demo", u.ID)

	if err := store.SetAppPlacement(app.ID, `{"local":1,"burst":2}`, 3); err != nil {
		t.Fatalf("SetAppPlacement: %v", err)
	}
	got, err := store.GetApp("demo")
	if err != nil {
		t.Fatalf("GetApp: %v", err)
	}
	if got.ReplicaPlacement != `{"local":1,"burst":2}` {
		t.Fatalf("ReplicaPlacement = %q", got.ReplicaPlacement)
	}
	if got.Replicas != 3 {
		t.Fatalf("Replicas total = %d, want 3", got.Replicas)
	}
}

func TestSetAppPlacement_DefaultEmptyOnCreate(t *testing.T) {
	store := mustOpenDB(t)
	u := mustCreateUser(t, store, "owner", "developer")
	app := mustCreateApp(t, store, "demo", u.ID)
	if app.ReplicaPlacement != "" {
		t.Fatalf("new app placement = %q, want empty", app.ReplicaPlacement)
	}
}
