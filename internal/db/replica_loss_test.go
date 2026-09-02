package db_test

import (
	"testing"

	"github.com/rvben/shinyhub/internal/db"
)

// TestMarkReplicaLostIfOwnedBy asserts the conditional loss transition only
// fires for a replica that is still running or crashed in place and still
// attributed to the given worker. The ownership guard prevents a stale
// worker-loss pass (revoke or down-sweep) from clobbering a replica that a
// concurrent redeploy already re-placed onto a healthy worker.
func TestMarkReplicaLostIfOwnedBy(t *testing.T) {
	store := mustOpenDB(t)
	owner := mustCreateUser(t, store, "owner", "admin")
	app := mustCreateApp(t, store, "app", owner.ID)

	seed := func(t *testing.T, idx int, status, workerID string) {
		t.Helper()
		if err := store.UpsertReplica(db.UpsertReplicaParams{
			AppID: app.ID, Index: idx, Status: status,
			Provider: "remote_docker", Tier: "remote", WorkerID: workerID,
		}); err != nil {
			t.Fatalf("seed replica %d: %v", idx, err)
		}
	}
	statusOf := func(t *testing.T, idx int) string {
		t.Helper()
		reps, err := store.ListReplicas(app.ID)
		if err != nil {
			t.Fatalf("list replicas: %v", err)
		}
		for _, r := range reps {
			if r.Index == idx {
				return r.Status
			}
		}
		t.Fatalf("replica %d not found", idx)
		return ""
	}

	// Running, owned by node-a: transitions to lost, reports changed.
	seed(t, 0, db.ReplicaStatusRunning, "node-a")
	changed, err := store.MarkReplicaLostIfOwnedBy(app.ID, 0, "node-a")
	if err != nil {
		t.Fatalf("owned running: %v", err)
	}
	if !changed {
		t.Fatal("owned running replica was not marked lost")
	}
	if got := statusOf(t, 0); got != db.ReplicaStatusLost {
		t.Fatalf("replica 0 status = %q, want lost", got)
	}

	// Running, but re-placed onto node-b: a loss pass for node-a must not touch it.
	seed(t, 1, db.ReplicaStatusRunning, "node-b")
	changed, err = store.MarkReplicaLostIfOwnedBy(app.ID, 1, "node-a")
	if err != nil {
		t.Fatalf("re-placed: %v", err)
	}
	if changed {
		t.Fatal("loss pass for node-a clobbered a replica now owned by node-b")
	}
	if got := statusOf(t, 1); got != db.ReplicaStatusRunning {
		t.Fatalf("re-placed replica status = %q, want running (untouched)", got)
	}

	// Already lost: the status guard makes it a no-op.
	seed(t, 2, db.ReplicaStatusLost, "node-a")
	changed, err = store.MarkReplicaLostIfOwnedBy(app.ID, 2, "node-a")
	if err != nil {
		t.Fatalf("already lost: %v", err)
	}
	if changed {
		t.Fatal("already-lost replica reported as newly changed")
	}

	// Crashed in place, still owned by node-a: transitions to lost. Crash
	// detection on a dying worker beats the heartbeat sweep, so by the time the
	// loss pass runs the row typically reads crashed, not running; skipping it
	// would strand the replica on the dead worker forever.
	seed(t, 3, db.ReplicaStatusCrashed, "node-a")
	changed, err = store.MarkReplicaLostIfOwnedBy(app.ID, 3, "node-a")
	if err != nil {
		t.Fatalf("owned crashed: %v", err)
	}
	if !changed {
		t.Fatal("owned crashed replica was not marked lost")
	}
	if got := statusOf(t, 3); got != db.ReplicaStatusLost {
		t.Fatalf("replica 3 status = %q, want lost", got)
	}

	// Crashed, but re-placed onto node-b: the ownership guard still applies.
	seed(t, 4, db.ReplicaStatusCrashed, "node-b")
	changed, err = store.MarkReplicaLostIfOwnedBy(app.ID, 4, "node-a")
	if err != nil {
		t.Fatalf("re-placed crashed: %v", err)
	}
	if changed {
		t.Fatal("loss pass for node-a clobbered a crashed replica owned by node-b")
	}
	if got := statusOf(t, 4); got != db.ReplicaStatusCrashed {
		t.Fatalf("re-placed crashed replica status = %q, want crashed (untouched)", got)
	}

	// Stopped: terminal for the loss pass; no runtime on the worker to lose.
	seed(t, 5, "stopped", "node-a")
	changed, err = store.MarkReplicaLostIfOwnedBy(app.ID, 5, "node-a")
	if err != nil {
		t.Fatalf("stopped: %v", err)
	}
	if changed {
		t.Fatal("stopped replica reported as newly changed")
	}
	if got := statusOf(t, 5); got != "stopped" {
		t.Fatalf("stopped replica status = %q, want stopped (untouched)", got)
	}
}
