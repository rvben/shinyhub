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

// TestRunningReplicaWorkersForSlug asserts the colocation query reports the
// distinct workers currently hosting an app's running replicas, so a
// shared-mount consumer can be pinned to a worker that also hosts its source.
// Only running replicas count (a lost/crashed replica's former worker is not a
// valid mount host), and a native replica with no worker id is excluded.
func TestRunningReplicaWorkersForSlug(t *testing.T) {
	store := mustOpenDB(t)
	owner := mustCreateUser(t, store, "owner", "admin")
	src := mustCreateApp(t, store, "src", owner.ID)

	seed := func(idx int, worker, status string) {
		t.Helper()
		if err := store.UpsertReplica(db.UpsertReplicaParams{
			AppID: src.ID, Index: idx, Status: status,
			Provider: "remote_docker", Tier: "remote", WorkerID: worker,
		}); err != nil {
			t.Fatalf("seed replica: %v", err)
		}
	}

	seed(0, "node-a", db.ReplicaStatusRunning)
	seed(1, "node-b", db.ReplicaStatusRunning)
	seed(2, "node-a", db.ReplicaStatusRunning) // duplicate worker collapses
	seed(3, "node-c", db.ReplicaStatusLost)    // lost: excluded
	seed(4, "", db.ReplicaStatusRunning)       // native (no worker): excluded

	workers, err := store.RunningReplicaWorkersForSlug("src")
	if err != nil {
		t.Fatalf("RunningReplicaWorkersForSlug: %v", err)
	}

	got := map[string]bool{}
	for _, w := range workers {
		got[w] = true
	}
	if len(workers) != 2 || !got["node-a"] || !got["node-b"] {
		t.Errorf("workers = %v, want exactly {node-a, node-b}", workers)
	}

	// An app with no running remote replicas yields no workers.
	none, err := store.RunningReplicaWorkersForSlug("does-not-exist")
	if err != nil {
		t.Fatalf("RunningReplicaWorkersForSlug(missing): %v", err)
	}
	if len(none) != 0 {
		t.Errorf("missing app workers = %v, want empty", none)
	}
}
