package db_test

import (
	"testing"
	"time"

	"github.com/rvben/shinyhub/internal/db"
)

// TestMarkReplicaCrashedClearingIdentityIfCurrent_HappyPath asserts the confirmed-
// stop retry's normal case: the row still holds the PID the caller means to
// finally confirm, so the write applies, marking the replica crashed with the
// given exit diagnostics and clearing its runtime identity (pid, port,
// endpoint, worker) so the slot is free for the next placement.
func TestMarkReplicaCrashedClearingIdentityIfCurrent_HappyPath(t *testing.T) {
	store := mustOpenDB(t)
	owner := mustCreateUser(t, store, "owner", "admin")
	app := mustCreateApp(t, store, "app", owner.ID)

	pid, port := 4242, 39000
	if err := store.UpsertReplica(db.UpsertReplicaParams{
		AppID: app.ID, Index: 0, Status: db.ReplicaStatusRunning,
		Provider: "native", Tier: "default", PID: &pid, Port: &port,
		EndpointURL: "http://127.0.0.1:39000", WorkerID: "",
	}); err != nil {
		t.Fatalf("seed running replica: %v", err)
	}

	code := 137
	observedAt := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)
	wrote, err := store.MarkReplicaCrashedClearingIdentityIfCurrent(db.UpsertReplicaParams{
		AppID: app.ID, Index: 0, Reason: "confirmed stop: process exited",
		ExitCode: &code, Signal: "SIGTERM", ExitObservedAt: observedAt,
		ExitOOMKilled: false, ExitRunID: "run-1",
	}, db.ReplicaRuntimeIdentity{PID: pid, Port: port, EndpointURL: "http://127.0.0.1:39000"})
	if err != nil {
		t.Fatalf("MarkReplicaCrashedClearingIdentityIfCurrent: %v", err)
	}
	if !wrote {
		t.Fatal("wrote = false, want true (the row still carries the expected identity)")
	}

	reps, err := store.ListReplicas(app.ID)
	if err != nil {
		t.Fatalf("list replicas: %v", err)
	}
	if len(reps) != 1 {
		t.Fatalf("replicas = %d, want 1 (a conditional UPDATE must not insert a row)", len(reps))
	}
	r := reps[0]
	if r.Status != db.ReplicaStatusCrashed {
		t.Fatalf("status = %q, want crashed", r.Status)
	}
	if r.PID != nil || r.Port != nil {
		t.Fatalf("pid=%v port=%v, want both cleared", r.PID, r.Port)
	}
	if r.EndpointURL != "" {
		t.Fatalf("endpoint_url = %q, want cleared", r.EndpointURL)
	}
	if r.Reason != "confirmed stop: process exited" {
		t.Fatalf("reason = %q, want the confirmed-stop reason", r.Reason)
	}
	if r.ExitCode == nil || *r.ExitCode != code {
		t.Fatalf("exit_code = %v, want %d", r.ExitCode, code)
	}
	if r.Signal != "SIGTERM" {
		t.Fatalf("signal = %q, want SIGTERM", r.Signal)
	}
	if r.ExitRunID != "run-1" {
		t.Fatalf("exit_run_id = %q, want run-1", r.ExitRunID)
	}
	if r.ExitObservedAt == nil || !r.ExitObservedAt.Equal(observedAt) {
		t.Fatalf("exit_observed_at = %v, want %v", r.ExitObservedAt, observedAt)
	}
}

// TestMarkReplicaCrashedClearingIdentityIfCurrent_PIDMismatchIsNoOp asserts the
// identity guard: a queued retry racing a fast restart that already reused the row
// with a different PID must not crash-mark the NEW placement. This is what
// keeps a stale pending-stop entry from destroying a replacement it never
// meant to touch.
func TestMarkReplicaCrashedClearingIdentityIfCurrent_PIDMismatchIsNoOp(t *testing.T) {
	store := mustOpenDB(t)
	owner := mustCreateUser(t, store, "owner", "admin")
	app := mustCreateApp(t, store, "app", owner.ID)

	oldPID, newPID, port := 4242, 5151, 39100
	if err := store.UpsertReplica(db.UpsertReplicaParams{
		AppID: app.ID, Index: 0, Status: db.ReplicaStatusRunning,
		Provider: "native", Tier: "default", PID: &newPID, Port: &port,
		EndpointURL: "http://127.0.0.1:39100",
	}); err != nil {
		t.Fatalf("seed replacement replica: %v", err)
	}

	wrote, err := store.MarkReplicaCrashedClearingIdentityIfCurrent(db.UpsertReplicaParams{
		AppID: app.ID, Index: 0, Reason: "stale confirmed-stop retry",
	}, db.ReplicaRuntimeIdentity{PID: oldPID, Port: port, EndpointURL: "http://127.0.0.1:39100"})
	if err != nil {
		t.Fatalf("MarkReplicaCrashedClearingIdentityIfCurrent: %v", err)
	}
	if wrote {
		t.Fatal("wrote = true, want false (the expected pid does not match the row's current pid)")
	}

	reps, err := store.ListReplicas(app.ID)
	if err != nil {
		t.Fatalf("list replicas: %v", err)
	}
	if len(reps) != 1 {
		t.Fatalf("replicas = %d, want 1", len(reps))
	}
	r := reps[0]
	if r.Status != db.ReplicaStatusRunning {
		t.Fatalf("status = %q, want running (the replacement must survive a stale retry untouched)", r.Status)
	}
	if r.PID == nil || *r.PID != newPID {
		t.Fatalf("pid = %v, want %d preserved", r.PID, newPID)
	}
	if r.EndpointURL != "http://127.0.0.1:39100" {
		t.Fatalf("endpoint_url = %q, want the replacement's endpoint preserved", r.EndpointURL)
	}
}

// TestMarkReplicaCrashedClearingIdentityIfCurrent_ObsoleteRowIsNoOp asserts the
// other way a queued retry can find nothing left to confirm: the replica row
// itself is gone (index deleted, e.g. a scale-down shrank past it) by the time
// the retry finally lands. The conditional UPDATE affects zero rows and must
// not resurrect the slot by inserting one.
func TestMarkReplicaCrashedClearingIdentityIfCurrent_ObsoleteRowIsNoOp(t *testing.T) {
	store := mustOpenDB(t)
	owner := mustCreateUser(t, store, "owner", "admin")
	app := mustCreateApp(t, store, "app", owner.ID)

	wrote, err := store.MarkReplicaCrashedClearingIdentityIfCurrent(db.UpsertReplicaParams{
		AppID: app.ID, Index: 0, Reason: "retry for a row that no longer exists",
	}, db.ReplicaRuntimeIdentity{PID: 4242})
	if err != nil {
		t.Fatalf("MarkReplicaCrashedClearingIdentityIfCurrent: %v", err)
	}
	if wrote {
		t.Fatal("wrote = true, want false (no row exists to update)")
	}

	reps, err := store.ListReplicas(app.ID)
	if err != nil {
		t.Fatalf("list replicas: %v", err)
	}
	if len(reps) != 0 {
		t.Fatalf("replicas = %d, want 0 (a conditional UPDATE must never insert a row for an obsolete retry)", len(reps))
	}
}

// TestMarkReplicaCrashedClearingIdentityIfCurrent_PIDZeroReplacementIsNoOp
// asserts the guard holds for runtimes that persist no host PID. Docker,
// remote and Fargate placements all store pid 0, so a retry queued for one
// placement and a replacement written into the same slot share that PID; the
// endpoint, worker and deployment are what tell them apart, and a retry for
// the old placement must leave the replacement running.
func TestMarkReplicaCrashedClearingIdentityIfCurrent_PIDZeroReplacementIsNoOp(t *testing.T) {
	const endpoint = "http://192.0.2.10:39200"
	cases := []struct {
		name string
		// old derives the stale retry's identity from the replacement's.
		old func(cur db.ReplicaRuntimeIdentity, prevDeployment int64) db.ReplicaRuntimeIdentity
	}{
		{"other worker", func(cur db.ReplicaRuntimeIdentity, _ int64) db.ReplicaRuntimeIdentity {
			cur.WorkerID = "container-old"
			return cur
		}},
		{"other endpoint", func(cur db.ReplicaRuntimeIdentity, _ int64) db.ReplicaRuntimeIdentity {
			cur.Port, cur.EndpointURL = 39201, "http://192.0.2.10:39201"
			return cur
		}},
		{"other deployment", func(cur db.ReplicaRuntimeIdentity, prev int64) db.ReplicaRuntimeIdentity {
			cur.DeploymentID = prev
			return cur
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store := mustOpenDB(t)
			owner := mustCreateUser(t, store, "owner", "admin")
			app := mustCreateApp(t, store, "app", owner.ID)
			var deps [2]int64
			for i, v := range []string{"v1", "v2"} {
				d, err := store.CreateDeployment(db.CreateDeploymentParams{AppID: app.ID, Version: v, BundleDir: "/srv/bundles/" + v})
				if err != nil {
					t.Fatalf("create deployment %s: %v", v, err)
				}
				deps[i] = d.ID
			}

			pid, port := 0, 39200
			if err := store.UpsertReplica(db.UpsertReplicaParams{
				AppID: app.ID, Index: 0, Status: db.ReplicaStatusRunning,
				Provider: "docker", Tier: "default", PID: &pid, Port: &port,
				EndpointURL: endpoint, WorkerID: "container-new",
				DeploymentID: &deps[1],
			}); err != nil {
				t.Fatalf("seed replacement replica: %v", err)
			}
			cur := db.ReplicaRuntimeIdentity{Port: port, EndpointURL: endpoint, WorkerID: "container-new", DeploymentID: deps[1]}

			wrote, err := store.MarkReplicaCrashedClearingIdentityIfCurrent(db.UpsertReplicaParams{
				AppID: app.ID, Index: 0, Reason: "stale confirmed-stop retry",
			}, tc.old(cur, deps[0]))
			if err != nil {
				t.Fatalf("MarkReplicaCrashedClearingIdentityIfCurrent: %v", err)
			}
			if wrote {
				t.Fatal("wrote = true, want false (the row holds a different pid-0 placement)")
			}
			reps, err := store.ListReplicas(app.ID)
			if err != nil {
				t.Fatalf("list replicas: %v", err)
			}
			if len(reps) != 1 || reps[0].Status != db.ReplicaStatusRunning || reps[0].WorkerID != "container-new" {
				t.Fatalf("replicas = %+v, want the replacement running and untouched", reps)
			}

			// Positive control: the replacement's own identity does match.
			wrote, err = store.MarkReplicaCrashedClearingIdentityIfCurrent(db.UpsertReplicaParams{
				AppID: app.ID, Index: 0, Reason: "confirmed stop",
			}, cur)
			if err != nil || !wrote {
				t.Fatalf("matching identity: wrote=%v err=%v, want a write", wrote, err)
			}
		})
	}
}
