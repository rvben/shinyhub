package db_test

import (
	"testing"

	"github.com/rvben/shinyhub/internal/db"
)

// TestRunningReplicaLoadByWorker asserts the placement load query reports, per
// worker, the count of running replicas it hosts and how many belong to the
// candidate app, so least-loaded placement can spread work and break ties by
// avoiding co-locating an app's own replicas. Only running replicas count; a
// lost/crashed replica's former worker must not appear inflated.
func TestRunningReplicaLoadByWorker(t *testing.T) {
	store := mustOpenDB(t)
	owner := mustCreateUser(t, store, "owner", "admin")
	appX := mustCreateApp(t, store, "app-x", owner.ID)
	appY := mustCreateApp(t, store, "app-y", owner.ID)

	seed := func(appID int64, idx int, worker, status string) {
		t.Helper()
		if err := store.UpsertReplica(db.UpsertReplicaParams{
			AppID: appID, Index: idx, Status: status,
			Provider: "remote_docker", Tier: "remote", WorkerID: worker,
		}); err != nil {
			t.Fatalf("seed replica: %v", err)
		}
	}

	// node-a hosts two running app-x replicas.
	seed(appX.ID, 0, "node-a", db.ReplicaStatusRunning)
	seed(appX.ID, 1, "node-a", db.ReplicaStatusRunning)
	// node-b hosts one running app-x replica and one running app-y replica.
	seed(appX.ID, 2, "node-b", db.ReplicaStatusRunning)
	seed(appY.ID, 0, "node-b", db.ReplicaStatusRunning)
	// node-c hosts only a lost replica, so it must not appear.
	seed(appY.ID, 1, "node-c", db.ReplicaStatusLost)

	loads, err := store.RunningReplicaLoadByWorker("app-x")
	if err != nil {
		t.Fatalf("RunningReplicaLoadByWorker: %v", err)
	}

	if got := loads["node-a"]; got.Total != 2 || got.SameApp != 2 {
		t.Errorf("node-a load = %+v, want {Total:2 SameApp:2}", got)
	}
	if got := loads["node-b"]; got.Total != 2 || got.SameApp != 1 {
		t.Errorf("node-b load = %+v, want {Total:2 SameApp:1}", got)
	}
	if _, ok := loads["node-c"]; ok {
		t.Errorf("node-c has only a lost replica; it must not appear in the load map: %+v", loads["node-c"])
	}
}
