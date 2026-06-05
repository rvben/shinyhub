package db

import (
	"testing"
	"time"
)

// backdateWorker forces a worker's last_heartbeat to a fixed UTC instant so
// staleness can be exercised deterministically (UpsertWorker always stamps the
// current time).
func backdateWorker(t *testing.T, store *Store, nodeID string, at time.Time) {
	t.Helper()
	if _, err := store.db.Exec(
		`UPDATE workers SET last_heartbeat = ? WHERE node_id = ?`,
		at.UTC().Format("2006-01-02 15:04:05"), nodeID); err != nil {
		t.Fatalf("backdate %q: %v", nodeID, err)
	}
}

// TestDeleteStaleWorkers asserts the reap pass removes only down, non-revoked
// workers whose heartbeat predates the cutoff and that have no non-terminal
// (running/crashed) replicas, while preserving revoked rows (audit), recently
// seen workers, still-up workers, and workers still hosting live replicas.
func TestDeleteStaleWorkers(t *testing.T) {
	store := mustOpenStore(t)

	old := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	recent := time.Date(2026, 5, 27, 0, 0, 0, 0, time.UTC)
	cutoff := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)

	// app provides app_id values for the replica rows that pin some workers.
	if err := store.CreateUser(CreateUserParams{Username: "owner", PasswordHash: "h", Role: "admin"}); err != nil {
		t.Fatalf("create user: %v", err)
	}
	owner, err := store.GetUserByUsername("owner")
	if err != nil {
		t.Fatalf("get user: %v", err)
	}
	if err := store.CreateApp(CreateAppParams{Slug: "demo", Name: "demo", OwnerID: owner.ID, Access: "private"}); err != nil {
		t.Fatalf("create app: %v", err)
	}
	app, err := store.GetAppBySlug("demo")
	if err != nil {
		t.Fatalf("get app: %v", err)
	}

	type workerSpec struct {
		node      string
		status    string
		revoke    bool
		heartbeat time.Time
		// replicaStatus pins a single replica to this worker when non-empty.
		replicaStatus string
		wantDeleted   bool
	}
	specs := []workerSpec{
		{node: "reap-me", status: "down", heartbeat: old, wantDeleted: true},
		{node: "reap-lost", status: "down", heartbeat: old, replicaStatus: "lost", wantDeleted: true},
		{node: "reap-stopped", status: "down", heartbeat: old, replicaStatus: "stopped", wantDeleted: true},
		{node: "keep-revoked", status: "down", revoke: true, heartbeat: old, wantDeleted: false},
		{node: "keep-recent", status: "down", heartbeat: recent, wantDeleted: false},
		{node: "keep-up", status: "up", heartbeat: old, wantDeleted: false},
		{node: "keep-running", status: "down", heartbeat: old, replicaStatus: "running", wantDeleted: false},
		{node: "keep-crashed", status: "down", heartbeat: old, replicaStatus: "crashed", wantDeleted: false},
	}

	idx := 0
	for _, s := range specs {
		if err := store.UpsertWorker(Worker{
			NodeID: s.node, AdvertiseAddr: "10.0.0.1:8443", Tier: "burst", Status: s.status,
		}); err != nil {
			t.Fatalf("upsert %q: %v", s.node, err)
		}
		backdateWorker(t, store, s.node, s.heartbeat)
		if s.revoke {
			if err := store.RevokeWorker(s.node); err != nil {
				t.Fatalf("revoke %q: %v", s.node, err)
			}
			// RevokeWorker stamps status=down but leaves last_heartbeat; re-backdate
			// to keep the worker stale for the cutoff.
			backdateWorker(t, store, s.node, s.heartbeat)
		}
		if s.replicaStatus != "" {
			if err := store.UpsertReplica(UpsertReplicaParams{
				AppID: app.ID, Index: idx, Status: s.replicaStatus,
				Provider: "remote", Tier: "burst", WorkerID: s.node,
			}); err != nil {
				t.Fatalf("upsert replica for %q: %v", s.node, err)
			}
			idx++
		}
	}

	deleted, err := store.DeleteStaleWorkers(cutoff)
	if err != nil {
		t.Fatalf("DeleteStaleWorkers: %v", err)
	}

	gotDeleted := map[string]bool{}
	for _, n := range deleted {
		gotDeleted[n] = true
	}
	for _, s := range specs {
		if gotDeleted[s.node] != s.wantDeleted {
			t.Errorf("returned reaped set: %q deleted=%v, want %v", s.node, gotDeleted[s.node], s.wantDeleted)
		}
	}

	for _, s := range specs {
		_, err := store.GetWorker(s.node)
		gone := err == ErrNotFound
		if gone != s.wantDeleted {
			t.Errorf("worker %q: gone=%v, wantDeleted=%v (err=%v)", s.node, gone, s.wantDeleted, err)
		}
	}
}
