package db_test

import (
	"testing"
	"time"

	"github.com/rvben/shinyhub/internal/db"
)

func TestStore_WorkerStaleDetectionAndReplicaLost(t *testing.T) {
	store := mustOpenDB(t)

	if err := store.UpsertWorker(db.Worker{
		NodeID: "node-a", AdvertiseAddr: "w:8443", Tier: "remote", Status: "up",
	}); err != nil {
		t.Fatalf("upsert worker: %v", err)
	}
	// Backdate the heartbeat to look stale (last_heartbeat is a UTC datetime string).
	old := time.Now().Add(-5 * time.Minute).UTC().Format("2006-01-02 15:04:05")
	if _, err := store.DB().Exec(`UPDATE workers SET last_heartbeat = ? WHERE node_id = ?`, old, "node-a"); err != nil {
		t.Fatalf("backdate heartbeat: %v", err)
	}

	stale, err := store.ListWorkersStale(time.Now().Add(-1 * time.Minute))
	if err != nil {
		t.Fatalf("ListWorkersStale: %v", err)
	}
	if len(stale) != 1 || stale[0].NodeID != "node-a" {
		t.Fatalf("stale = %+v, want [node-a]", stale)
	}

	// A worker already marked down is not reported stale.
	if err := store.SetWorkerStatus("node-a", "down"); err != nil {
		t.Fatalf("SetWorkerStatus: %v", err)
	}
	stale, err = store.ListWorkersStale(time.Now().Add(-1 * time.Minute))
	if err != nil {
		t.Fatalf("ListWorkersStale after down: %v", err)
	}
	if len(stale) != 0 {
		t.Fatalf("down worker still reported stale: %+v", stale)
	}

	// A running replica owned by node-a can be listed and transitioned to lost.
	owner := mustCreateUser(t, store, "owner", "admin")
	app := mustCreateApp(t, store, "app", owner.ID)
	if err := store.UpsertReplica(db.UpsertReplicaParams{
		AppID: app.ID, Index: 0, Status: db.ReplicaStatusRunning,
		Provider: "remote_docker", Tier: "remote", WorkerID: "node-a",
	}); err != nil {
		t.Fatalf("seed replica: %v", err)
	}
	owned, err := store.ListReplicasByWorker("node-a")
	if err != nil || len(owned) != 1 || owned[0].Index != 0 {
		t.Fatalf("ListReplicasByWorker = %+v, err=%v", owned, err)
	}
	if err := store.UpdateReplicaStatus(app.ID, 0, db.ReplicaStatusLost); err != nil {
		t.Fatalf("UpdateReplicaStatus: %v", err)
	}
	reps, _ := store.ListReplicas(app.ID)
	if len(reps) != 1 || reps[0].Status != db.ReplicaStatusLost {
		t.Fatalf("replica status = %+v, want lost", reps)
	}
}
