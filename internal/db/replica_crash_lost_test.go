package db_test

import (
	"testing"
	"time"

	"github.com/rvben/shinyhub/internal/db"
)

// TestRecordReplicaCrash_PreservesLost asserts a crash write cannot pull a
// replica back out of the lost state. The worker-down sweep rules a replica
// lost without holding the app lease, so a restart attempt already in flight
// can report its failure after the loss verdict landed; the loss is the later,
// authoritative fact (the worker is gone) and must win.
func TestRecordReplicaCrash_PreservesLost(t *testing.T) {
	store := mustOpenDB(t)
	owner := mustCreateUser(t, store, "owner", "admin")
	app := mustCreateApp(t, store, "app", owner.ID)

	if err := store.UpsertReplica(db.UpsertReplicaParams{
		AppID: app.ID, Index: 0, Status: db.ReplicaStatusLost,
		Provider: "remote_docker", Tier: "remote", WorkerID: "node-a",
	}); err != nil {
		t.Fatalf("seed lost replica: %v", err)
	}

	if err := store.RecordReplicaCrash(db.UpsertReplicaParams{
		AppID: app.ID, Index: 0, Reason: "late crash report from the dead worker",
	}); err != nil {
		t.Fatalf("record crash: %v", err)
	}

	reps, err := store.ListReplicas(app.ID)
	if err != nil {
		t.Fatalf("list replicas: %v", err)
	}
	if len(reps) != 1 {
		t.Fatalf("replicas = %d, want 1 (a guarded no-op must not insert a second row)", len(reps))
	}
	if reps[0].Status != db.ReplicaStatusLost {
		t.Fatalf("replica status = %q, want lost (crash write undid the loss verdict)", reps[0].Status)
	}
	if reps[0].WorkerID != "node-a" {
		t.Fatalf("replica worker = %q, want node-a (row must be untouched)", reps[0].WorkerID)
	}
}

// TestRecordReplicaCrashFromLost_OverwritesLost pins the one deliberate
// exception: a restart launched FROM a lost row records its failure over the
// lost state. That transition is correct - the re-placement onto a healthy
// tier failed, which is an app fault with real diagnostics - and safe, because
// the caller holds the app lease and the loss sweep no-ops on lost rows.
func TestRecordReplicaCrashFromLost_OverwritesLost(t *testing.T) {
	store := mustOpenDB(t)
	owner := mustCreateUser(t, store, "owner", "admin")
	app := mustCreateApp(t, store, "app", owner.ID)

	if err := store.UpsertReplica(db.UpsertReplicaParams{
		AppID: app.ID, Index: 0, Status: db.ReplicaStatusLost,
		Provider: "remote_docker", Tier: "remote", WorkerID: "node-a",
	}); err != nil {
		t.Fatalf("seed lost replica: %v", err)
	}

	if err := store.RecordReplicaCrashFromLost(db.UpsertReplicaParams{
		AppID: app.ID, Index: 0, Reason: "re-placement failed: bad bundle",
	}); err != nil {
		t.Fatalf("record crash from lost: %v", err)
	}

	reps, err := store.ListReplicas(app.ID)
	if err != nil {
		t.Fatalf("list replicas: %v", err)
	}
	if len(reps) != 1 {
		t.Fatalf("replicas = %d, want 1", len(reps))
	}
	if reps[0].Status != db.ReplicaStatusCrashed {
		t.Fatalf("replica status = %q, want crashed", reps[0].Status)
	}
	if reps[0].Reason != "re-placement failed: bad bundle" {
		t.Fatalf("replica reason = %q, want the re-placement failure", reps[0].Reason)
	}
}

// TestRecordReplicaCrashFromLost_DetachesDeadWorker asserts the lost-to-crashed
// overwrite clears the placement identity the loss verdict left behind. The
// lost row keeps its worker_id so the loss stays attributable, but once a
// re-placement launched from that row fails, the resulting crashed row belongs
// to no placement: the crash happened attempting a NEW placement, and a crashed
// row still naming the dead worker pins that worker's record forever, because
// the stale-worker reap refuses to delete a worker with running or crashed
// replicas and the worker-down sweep fires only once per death.
func TestRecordReplicaCrashFromLost_DetachesDeadWorker(t *testing.T) {
	store := mustOpenDB(t)
	owner := mustCreateUser(t, store, "owner", "admin")
	app := mustCreateApp(t, store, "app", owner.ID)

	old := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	cutoff := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	if err := store.UpsertWorker(db.Worker{
		NodeID: "node-a", AdvertiseAddr: "10.0.0.1:8443", Tier: "remote", Status: "down",
	}); err != nil {
		t.Fatalf("upsert worker: %v", err)
	}
	backdateWorker(t, store, "node-a", old)

	pid, port := 4242, 39000
	if err := store.UpsertReplica(db.UpsertReplicaParams{
		AppID: app.ID, Index: 0, Status: db.ReplicaStatusLost,
		Provider: "remote_docker", Tier: "remote", WorkerID: "node-a",
		EndpointURL: "http://10.0.0.1:39000", PID: &pid, Port: &port,
	}); err != nil {
		t.Fatalf("seed lost replica: %v", err)
	}

	if err := store.RecordReplicaCrashFromLost(db.UpsertReplicaParams{
		AppID: app.ID, Index: 0, Reason: "re-placement failed: bad bundle",
	}); err != nil {
		t.Fatalf("record crash from lost: %v", err)
	}

	reps, err := store.ListReplicas(app.ID)
	if err != nil {
		t.Fatalf("list replicas: %v", err)
	}
	if len(reps) != 1 {
		t.Fatalf("replicas = %d, want 1", len(reps))
	}
	r := reps[0]
	if r.Status != db.ReplicaStatusCrashed {
		t.Fatalf("replica status = %q, want crashed", r.Status)
	}
	if r.WorkerID != "" {
		t.Fatalf("replica worker = %q, want cleared (crashed row must not stay attributed to the dead worker)", r.WorkerID)
	}
	if r.EndpointURL != "" || r.PID != nil || r.Port != nil {
		t.Fatalf("replica placement = url %q pid %v port %v, want all cleared (the dead placement no longer exists)", r.EndpointURL, r.PID, r.Port)
	}

	reaped, err := store.DeleteStaleWorkers(cutoff)
	if err != nil {
		t.Fatalf("DeleteStaleWorkers: %v", err)
	}
	if len(reaped) != 1 || reaped[0] != "node-a" {
		t.Fatalf("reaped = %v, want [node-a] (detached crash must unpin the dead worker)", reaped)
	}
}

// TestRecordReplicaCrashFromLost_KeepsReplacementIdentity bounds the detach
// from the other side: a lost-slot restart that got far enough to persist its
// replacement as starting (the ReplicaStarted callback) and then failed on
// health or registration may have left that new runtime alive under
// unconfirmed cleanup. The persisted identity of THAT placement is the fence
// that stops a later start from launching a duplicate beside it, so the crash
// write must keep it: only a row still in the original lost state carries a
// dead placement worth detaching.
func TestRecordReplicaCrashFromLost_KeepsReplacementIdentity(t *testing.T) {
	store := mustOpenDB(t)
	owner := mustCreateUser(t, store, "owner", "admin")
	app := mustCreateApp(t, store, "app", owner.ID)

	pid, port := 5151, 39100
	if err := store.UpsertReplica(db.UpsertReplicaParams{
		AppID: app.ID, Index: 0, Status: "starting",
		Provider: "remote_docker", Tier: "remote", WorkerID: "node-b",
		EndpointURL: "http://10.0.0.2:39100", PID: &pid, Port: &port,
	}); err != nil {
		t.Fatalf("seed starting replacement: %v", err)
	}

	if err := store.RecordReplicaCrashFromLost(db.UpsertReplicaParams{
		AppID: app.ID, Index: 0, Reason: "health: gave up after 60s",
	}); err != nil {
		t.Fatalf("record crash from lost: %v", err)
	}

	reps, err := store.ListReplicas(app.ID)
	if err != nil {
		t.Fatalf("list replicas: %v", err)
	}
	if len(reps) != 1 {
		t.Fatalf("replicas = %d, want 1", len(reps))
	}
	r := reps[0]
	if r.Status != db.ReplicaStatusCrashed {
		t.Fatalf("replica status = %q, want crashed", r.Status)
	}
	if r.WorkerID != "node-b" {
		t.Fatalf("replica worker = %q, want node-b (clearing it erases the duplicate-runtime fence for an unconfirmed replacement)", r.WorkerID)
	}
	if r.EndpointURL != "http://10.0.0.2:39100" || r.PID == nil || *r.PID != pid || r.Port == nil || *r.Port != port {
		t.Fatalf("replica placement = url %q pid %v port %v, want the replacement's identity preserved", r.EndpointURL, r.PID, r.Port)
	}
}

// TestRecordReplicaCrash_KeepsLiveWorkerAttribution pins the scope of the
// detach: an ordinary crash on a live worker keeps its worker_id, because that
// placement still exists and the worker report reconciliation and loss sweep
// both key on it.
func TestRecordReplicaCrash_KeepsLiveWorkerAttribution(t *testing.T) {
	store := mustOpenDB(t)
	owner := mustCreateUser(t, store, "owner", "admin")
	app := mustCreateApp(t, store, "app", owner.ID)

	if err := store.UpsertReplica(db.UpsertReplicaParams{
		AppID: app.ID, Index: 0, Status: db.ReplicaStatusRunning,
		Provider: "remote_docker", Tier: "remote", WorkerID: "node-a",
	}); err != nil {
		t.Fatalf("seed running replica: %v", err)
	}

	if err := store.RecordReplicaCrash(db.UpsertReplicaParams{
		AppID: app.ID, Index: 0, Reason: "process exited",
	}); err != nil {
		t.Fatalf("record crash: %v", err)
	}

	reps, err := store.ListReplicas(app.ID)
	if err != nil {
		t.Fatalf("list replicas: %v", err)
	}
	if len(reps) != 1 || reps[0].Status != db.ReplicaStatusCrashed {
		t.Fatalf("replicas = %+v, want one crashed row", reps)
	}
	if reps[0].WorkerID != "node-a" {
		t.Fatalf("replica worker = %q, want node-a (a crash on a live placement keeps its attribution)", reps[0].WorkerID)
	}
}

// TestRecordReplicaCrash_InsertsMissingRow asserts the lost guard does not
// break the pre-existing fallback: a crash observed before the normal replica
// upsert reached the database still inserts a minimal crashed row.
func TestRecordReplicaCrash_InsertsMissingRow(t *testing.T) {
	store := mustOpenDB(t)
	owner := mustCreateUser(t, store, "owner", "admin")
	app := mustCreateApp(t, store, "app", owner.ID)

	if err := store.RecordReplicaCrash(db.UpsertReplicaParams{
		AppID: app.ID, Index: 0, Reason: "crashed before first upsert",
	}); err != nil {
		t.Fatalf("record crash: %v", err)
	}

	reps, err := store.ListReplicas(app.ID)
	if err != nil {
		t.Fatalf("list replicas: %v", err)
	}
	if len(reps) != 1 {
		t.Fatalf("replicas = %d, want 1 (missing row must still be inserted)", len(reps))
	}
	if reps[0].Status != db.ReplicaStatusCrashed {
		t.Fatalf("replica status = %q, want crashed", reps[0].Status)
	}
}
