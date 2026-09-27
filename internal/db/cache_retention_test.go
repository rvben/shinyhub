package db_test

import (
	"maps"
	"slices"
	"testing"

	"github.com/rvben/shinyhub/internal/db"
)

func retainedIDs(t *testing.T, store *db.Store, appID int64) ([]int64, int64) {
	t.Helper()
	r, err := store.CacheRetention(appID)
	if err != nil {
		t.Fatalf("CacheRetention: %v", err)
	}
	ids := slices.Sorted(maps.Keys(r.Retained))
	return ids, r.MaxID
}

func TestCacheRetention_EmptyApp(t *testing.T) {
	store := mustOpenDB(t)
	u := mustCreateUser(t, store, "owner", "developer")
	app := mustCreateApp(t, store, "demo", u.ID)

	ids, maxID := retainedIDs(t, store, app.ID)
	if len(ids) != 0 || maxID != 0 {
		t.Fatalf("never-deployed app retained %v with max %d, want nothing and 0", ids, maxID)
	}
}

// Every way a process or job can still be running an activation keeps that
// activation's namespace; an activation nothing references is released.
func TestCacheRetention_EverySource(t *testing.T) {
	store := mustOpenDB(t)
	u := mustCreateUser(t, store, "owner", "developer")
	app := mustCreateApp(t, store, "demo", u.ID)
	other := mustCreateApp(t, store, "other", u.ID)

	// Released: promoted, then superseded, and referenced by nothing.
	released := promoteConvergenceDeployment(t, store, app.ID, "v1", "sha256:1")
	// Kept by a legacy replica row (e.g. a stop that was never confirmed).
	byReplica := promoteConvergenceDeployment(t, store, app.ID, "v2", "sha256:2")
	// Kept by a draining generation.
	byGeneration := promoteConvergenceDeployment(t, store, app.ID, "v3", "sha256:3")
	// Kept by a running schedule run.
	byRun := promoteConvergenceDeployment(t, store, app.ID, "v4", "sha256:4")
	// Kept as the active deployment.
	active := promoteConvergenceDeployment(t, store, app.ID, "v5", "sha256:5")
	// Kept as a pending candidate that has not been promoted yet.
	pending, err := store.BeginDeployment(app.ID, "v6", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	// Another app's deployment must not leak into this app's set.
	otherDep := promoteConvergenceDeployment(t, store, other.ID, "v1", "sha256:o")

	depID := byReplica.ID
	if err := store.UpsertReplica(db.UpsertReplicaParams{
		AppID: app.ID, Index: 0, Status: "running", DeploymentID: &depID,
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.UpsertDeploymentReplica(db.UpsertDeploymentReplicaParams{
		AppID: app.ID, DeploymentID: byGeneration.ID, Index: 0, Status: "running",
	}); err != nil {
		t.Fatal(err)
	}
	cmd := `["python","produce.py"]`
	sched := createConvergenceSchedule(t, store, app.ID, cmd, "never")
	insertProducerRun(t, store, sched, byRun, cmd)
	otherReplica := otherDep.ID
	if err := store.UpsertReplica(db.UpsertReplicaParams{
		AppID: other.ID, Index: 0, Status: "running", DeploymentID: &otherReplica,
	}); err != nil {
		t.Fatal(err)
	}

	ids, maxID := retainedIDs(t, store, app.ID)
	want := []int64{byReplica.ID, byGeneration.ID, byRun.ID, active.ID, pending.ID}
	slices.Sort(want)
	if !slices.Equal(ids, want) {
		t.Fatalf("retained %v, want %v (released %d must be absent)", ids, want, released.ID)
	}
	if maxID != pending.ID {
		t.Fatalf("MaxID = %d, want the newest deployment %d", maxID, pending.ID)
	}
}
