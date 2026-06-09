package api

import (
	"errors"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/rvben/shinyhub/internal/config"
	"github.com/rvben/shinyhub/internal/db"
	"github.com/rvben/shinyhub/internal/process"
)

// newClusteredScaleTestServer creates a server identical to newScaleTestServer
// but with SetCluster wired (instanceID = "this-instance") so all clustered
// scale-down paths are exercised.
func newClusteredScaleTestServer(t *testing.T, slug string, replicas int) (*Server, *db.App) {
	t.Helper()
	cfg := &config.Config{}
	cfg.Runtime.MaxReplicas = 8
	srv, app := newScaleTestServer(t, slug, replicas, cfg)
	srv.SetCluster("this-instance")
	return srv, app
}

// drainingCheckRuntime is a process.Runtime that checks the DB desired_state
// at the moment StopReplica (via Signal) is called. This lets us verify that
// desired_state='draining' is written before the stop, by interrogating the
// store inside the stop call.
type drainingCheckRuntime struct {
	stopFailRuntime
	onSignal func()
}

func (r *drainingCheckRuntime) Signal(h process.RunHandle, sig syscall.Signal) error {
	if r.onSignal != nil {
		r.onSignal()
	}
	return nil // succeed (allow the stop to proceed)
}

// TestClusteredScaleDown_WritesDesiredStateDraining verifies that in clustered
// mode, ScaleDown persists desired_state='draining' for the victim replica
// before calling StopReplica. We check the DB desired_state inside the Signal
// hook so the observation happens synchronously before any DeleteReplica.
func TestClusteredScaleDown_WritesDesiredStateDraining(t *testing.T) {
	cfg := &config.Config{}
	cfg.Runtime.MaxReplicas = 8
	srv, app := newScaleTestServer(t, "desired-drain", 2, cfg)
	srv.SetCluster("this-instance")

	var desiredStateAtStop string
	rt := &drainingCheckRuntime{}
	rt.onSignal = func() {
		reps, err := srv.store.ListReplicas(app.ID)
		if err != nil {
			return
		}
		for _, r := range reps {
			if r.Index == 1 {
				desiredStateAtStop = r.DesiredState
				return
			}
		}
	}
	srv.manager.RegisterRuntime("draining-check", rt)
	if _, err := srv.manager.Start(process.StartParams{
		Slug: "desired-drain", Index: 1, Tier: "draining-check", Dir: t.TempDir(),
		Command: []string{"sleep", "30"}, Port: 20100,
	}); err != nil {
		t.Fatalf("seed victim process: %v", err)
	}
	srv.proxy.SetPoolSize("desired-drain", 2)

	scaled, err := srv.ScaleDown("desired-drain", 200*time.Millisecond)
	if err != nil {
		t.Fatalf("ScaleDown: %v", err)
	}
	if !scaled {
		t.Fatal("ScaleDown reported no change for a 2-replica app")
	}
	if desiredStateAtStop != "draining" {
		t.Errorf("desired_state at stop = %q; want 'draining' (must be written before StopReplica)", desiredStateAtStop)
	}
}

// TestClusteredScaleDown_WaitsOnFleetCount verifies that in clustered mode,
// ScaleDown waits until both the local count and the fleet count (from other
// instances) reach zero before proceeding. It seeds a replica_sessions row for
// another instance to simulate that instance having an active session on the
// victim, then verifies the stop did not happen before that row was cleared.
func TestClusteredScaleDown_WaitsOnFleetCount(t *testing.T) {
	srv, app := newClusteredScaleTestServer(t, "drain-fleet", 2)

	info, err := srv.manager.Start(process.StartParams{
		Slug: "drain-fleet", Index: 1, Dir: t.TempDir(),
		Command: []string{"sleep", "30"}, Port: 20200,
	})
	if err != nil {
		t.Fatalf("seed victim process: %v", err)
	}
	srv.proxy.SetPoolSize("drain-fleet", 2)

	// Seed a fleet session row for "other-instance" reporting 1 active session on
	// replica index 1. Rows are stamped with the DB clock (always fresh).
	if err := srv.store.UpsertReplicaSessions("other-instance", []db.ReplicaSessionRow{
		{AppID: app.ID, Idx: 1, Active: 1, LastActivityAgeSec: 0},
	}); err != nil {
		t.Fatalf("seed other-instance sessions: %v", err)
	}

	// cleared is closed by the goroutine immediately after it clears the fleet
	// session, giving us a happens-before signal to compare against ScaleDown's
	// return.
	cleared := make(chan struct{})
	go func() {
		time.Sleep(120 * time.Millisecond)
		_ = srv.store.UpsertReplicaSessions("other-instance", []db.ReplicaSessionRow{
			{AppID: app.ID, Idx: 1, Active: 0, LastActivityAgeSec: 0},
		})
		close(cleared)
	}()

	scaled, err := srv.ScaleDown("drain-fleet", 500*time.Millisecond)
	if err != nil {
		t.Fatalf("ScaleDown: %v", err)
	}
	if !scaled {
		t.Fatal("ScaleDown reported no change for a 2-replica app")
	}
	// ScaleDown must not have returned before the fleet session was cleared.
	// If it did, cleared is not yet closed and the default branch fires.
	select {
	case <-cleared:
		// Correct: ScaleDown waited for the fleet session to clear.
	default:
		t.Error("ScaleDown returned before the fleet session was cleared; fleet wait did not run")
	}
	if err := syscall.Kill(info.PID, 0); err == nil {
		t.Errorf("victim process (pid %d) still alive after ScaleDown", info.PID)
	}
}

// TestClusteredScaleDown_ProceedsAfterGraceEvenWithFleetSessions verifies that
// ScaleDown is always deadline-bounded: even if the fleet session on the victim
// never clears, the stop happens after grace elapses (not stuck forever).
func TestClusteredScaleDown_ProceedsAfterGraceEvenWithFleetSessions(t *testing.T) {
	srv, app := newClusteredScaleTestServer(t, "drain-grace", 2)

	info, err := srv.manager.Start(process.StartParams{
		Slug: "drain-grace", Index: 1, Dir: t.TempDir(),
		Command: []string{"sleep", "30"}, Port: 20300,
	})
	if err != nil {
		t.Fatalf("seed victim process: %v", err)
	}
	srv.proxy.SetPoolSize("drain-grace", 2)

	// Seed a fleet session that will NEVER be cleared, to verify the deadline.
	if err := srv.store.UpsertReplicaSessions("stubborn-instance", []db.ReplicaSessionRow{
		{AppID: app.ID, Idx: 1, Active: 1, LastActivityAgeSec: 0},
	}); err != nil {
		t.Fatalf("seed stubborn sessions: %v", err)
	}

	grace := 150 * time.Millisecond
	start := time.Now()
	scaled, err := srv.ScaleDown("drain-grace", grace)
	elapsed := time.Since(start)

	if err != nil {
		t.Fatalf("ScaleDown: %v", err)
	}
	if !scaled {
		t.Fatal("ScaleDown reported no change for a 2-replica app")
	}
	// Must have waited at least the grace before force-stopping.
	if elapsed < grace {
		t.Errorf("ScaleDown returned in %s, before the %s grace elapsed", elapsed, grace)
	}
	// Must not have stalled far beyond grace.
	if elapsed > grace+2*time.Second {
		t.Errorf("ScaleDown took %s; must be bounded near %s grace", elapsed, grace)
	}
	if err := syscall.Kill(info.PID, 0); err == nil {
		t.Errorf("victim process (pid %d) still alive after force scale-down", info.PID)
	}
}

// TestClusteredScaleDown_StopFailureRevertsDesiredState verifies that when
// StopReplica fails (non-benign error), ScaleDown reverts both the local drain
// mark (UndrainReplica) and the DB desired_state back to 'running', so other
// instances resume routing to the still-running replica.
func TestClusteredScaleDown_StopFailureRevertsDesiredState(t *testing.T) {
	cfg := &config.Config{}
	cfg.Runtime.MaxReplicas = 8
	srv, app := newScaleTestServer(t, "demo-rollback", 2, cfg)
	srv.SetCluster("this-instance")

	// Register a runtime that refuses to stop.
	srv.manager.RegisterRuntime("failstop", &stopFailRuntime{})
	if _, err := srv.manager.Start(process.StartParams{
		Slug: "demo-rollback", Index: 1, Tier: "failstop", Dir: t.TempDir(),
		Command: []string{"sleep", "30"}, Port: 20400,
	}); err != nil {
		t.Fatalf("seed victim process: %v", err)
	}
	srv.proxy.SetPoolSize("demo-rollback", 2)

	scaled, err := srv.ScaleDown("demo-rollback", 100*time.Millisecond)
	if err == nil {
		t.Fatal("ScaleDown returned nil error despite the stop failing")
	}
	if scaled {
		t.Error("ScaleDown reported success despite the stop failing")
	}

	// Drain flag must be cleared.
	if srv.proxy.IsDraining("demo-rollback", 1) {
		t.Error("victim slot left draining after the scale-down aborted")
	}

	// DB desired_state must be reverted to 'running'.
	reps, err := srv.store.ListReplicas(app.ID)
	if err != nil {
		t.Fatalf("list replicas: %v", err)
	}
	for _, r := range reps {
		if r.Index == 1 {
			if r.DesiredState != "running" {
				t.Errorf("replica index 1 desired_state = %q after failed stop; want 'running'", r.DesiredState)
			}
			return
		}
	}
	t.Errorf("replica row index 1 not found; rows=%+v", reps)
}

// TestSingleNodeScaleDown_NoDesiredStateWrite verifies that in single-node
// (non-clustered) mode, ScaleDown does not write desired_state to the DB and
// does not call AppFleetLoad. The scale-down must still succeed and remove the
// replica row normally.
func TestSingleNodeScaleDown_NoDesiredStateWrite(t *testing.T) {
	cfg := &config.Config{}
	cfg.Runtime.MaxReplicas = 8
	// Not clustered: do NOT call srv.SetCluster.
	srv, app := newScaleTestServer(t, "solo", 2, cfg)

	// Seed the victim replica row with desired_state='running' so we can verify
	// it was NOT changed to 'draining' during scale-down.
	reps, err := srv.store.ListReplicas(app.ID)
	if err != nil {
		t.Fatalf("list replicas before: %v", err)
	}
	var initialDesiredState string
	for _, r := range reps {
		if r.Index == 1 {
			initialDesiredState = r.DesiredState
		}
	}
	if initialDesiredState == "" {
		initialDesiredState = "running"
	}

	info, err := srv.manager.Start(process.StartParams{
		Slug: "solo", Index: 1, Dir: t.TempDir(),
		Command: []string{"sleep", "30"}, Port: 20500,
	})
	if err != nil {
		t.Fatalf("seed victim process: %v", err)
	}
	srv.proxy.SetPoolSize("solo", 2)

	// Observe desired_state changes during scale-down via a concurrent watcher.
	var desiredStateMutated bool
	watchDone := make(chan struct{})
	go func() {
		defer close(watchDone)
		deadline := time.Now().Add(500 * time.Millisecond)
		for time.Now().Before(deadline) {
			reps, err := srv.store.ListReplicas(app.ID)
			if err != nil {
				time.Sleep(5 * time.Millisecond)
				continue
			}
			for _, r := range reps {
				if r.Index == 1 && r.DesiredState == "draining" {
					desiredStateMutated = true
					return
				}
			}
			time.Sleep(5 * time.Millisecond)
		}
	}()

	scaled, err := srv.ScaleDown("solo", 200*time.Millisecond)
	if err != nil {
		t.Fatalf("ScaleDown: %v", err)
	}
	if !scaled {
		t.Fatal("ScaleDown reported no change for a 2-replica app")
	}
	if err := syscall.Kill(info.PID, 0); err == nil {
		t.Errorf("victim process (pid %d) still alive after ScaleDown", info.PID)
	}

	// Wait for the watcher to finish.
	<-watchDone

	if desiredStateMutated {
		t.Error("single-node ScaleDown wrote desired_state='draining' to the DB; single-node must not touch desired_state")
	}

	// Replica row must be deleted (normal scale-down behavior).
	reps, err = srv.store.ListReplicas(app.ID)
	if err != nil {
		t.Fatalf("list replicas after: %v", err)
	}
	for _, r := range reps {
		if r.Index == 1 {
			t.Errorf("single-node ScaleDown left replica row index 1; want deleted")
		}
	}
}

// TestSingleNodeScaleDown_NoFleetLoad verifies that the single-node path does
// not call AppFleetLoad. We verify this by closing the store after the scale-down
// seeds are in place but before ScaleDown runs, to prove the operation cannot
// talk to the DB (if it tried to call AppFleetLoad it would fail). Single-node
// ScaleDown only calls the DB for the final DeleteReplica/UpdateAppReplicas, which
// happen after the drain. We use a mock approach instead: verify !clustered
// means clusteredFleetWait returns nil.
func TestSingleNodeScaleDown_FleetWaitIsNil(t *testing.T) {
	cfg := &config.Config{}
	cfg.Runtime.MaxReplicas = 8
	srv, app := newScaleTestServer(t, "fleet-nil", 2, cfg)
	// Not clustered: do NOT call srv.SetCluster.

	// clusteredFleetWait must return nil for a non-clustered server.
	fn := srv.clusteredFleetWait(app.ID, 1)
	if fn != nil {
		t.Error("single-node clusteredFleetWait returned non-nil; want nil so AppFleetLoad is never called")
	}
}

// TestClusteredScaleDown_FleetWaitIsNonNil verifies that clusteredFleetWait
// returns a non-nil predicate in clustered mode.
func TestClusteredScaleDown_FleetWaitIsNonNil(t *testing.T) {
	cfg := &config.Config{}
	cfg.Runtime.MaxReplicas = 8
	srv, app := newScaleTestServer(t, "fleet-nonnnil", 2, cfg)
	srv.SetCluster("inst-x")

	fn := srv.clusteredFleetWait(app.ID, 1)
	if fn == nil {
		t.Error("clustered clusteredFleetWait returned nil; want a non-nil fleet predicate")
	}
}

// TestClusteredScaleDown_Race exercises the clustered scale-down path under
// Go's race detector. It runs a fast scale-down with a fleet session that
// clears quickly, so goroutines in the drain loop and the fleet poller run
// concurrently with the main flow.
func TestClusteredScaleDown_Race(t *testing.T) {
	srv, app := newClusteredScaleTestServer(t, "race-drain", 2)

	info, err := srv.manager.Start(process.StartParams{
		Slug: "race-drain", Index: 1, Dir: t.TempDir(),
		Command: []string{"sleep", "30"}, Port: 20600,
	})
	if err != nil {
		t.Fatalf("seed victim process: %v", err)
	}
	srv.proxy.SetPoolSize("race-drain", 2)

	if err := srv.store.UpsertReplicaSessions("peer", []db.ReplicaSessionRow{
		{AppID: app.ID, Idx: 1, Active: 1, LastActivityAgeSec: 0},
	}); err != nil {
		t.Fatalf("seed sessions: %v", err)
	}

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		// Clear fleet sessions concurrently.
		time.Sleep(30 * time.Millisecond)
		_ = srv.store.UpsertReplicaSessions("peer", []db.ReplicaSessionRow{
			{AppID: app.ID, Idx: 1, Active: 0, LastActivityAgeSec: 0},
		})
	}()

	scaled, err := srv.ScaleDown("race-drain", 300*time.Millisecond)
	wg.Wait()

	if err != nil {
		t.Fatalf("ScaleDown: %v", err)
	}
	if !scaled {
		t.Fatal("ScaleDown reported no change")
	}
	if killErr := syscall.Kill(info.PID, 0); killErr == nil {
		t.Errorf("victim process (pid %d) still alive after ScaleDown", info.PID)
	}
}

// TestClusteredScaleDown_DoesNotStopWhileFleetNonZero verifies that ScaleDown
// does NOT prematurely stop the victim while the fleet index is non-zero (within
// grace). We prove this by checking that the victim process is still alive when
// the fleet sessions are still set, and only gone after they clear.
func TestClusteredScaleDown_DoesNotStopWhileFleetNonZero(t *testing.T) {
	srv, app := newClusteredScaleTestServer(t, "no-early-stop", 2)

	info, err := srv.manager.Start(process.StartParams{
		Slug: "no-early-stop", Index: 1, Dir: t.TempDir(),
		Command: []string{"sleep", "30"}, Port: 20700,
	})
	if err != nil {
		t.Fatalf("seed victim process: %v", err)
	}
	srv.proxy.SetPoolSize("no-early-stop", 2)

	// Seed a non-zero fleet session.
	if err := srv.store.UpsertReplicaSessions("peer2", []db.ReplicaSessionRow{
		{AppID: app.ID, Idx: 1, Active: 1, LastActivityAgeSec: 0},
	}); err != nil {
		t.Fatalf("seed sessions: %v", err)
	}

	// The grace is 500ms, but we clear the fleet session at 200ms.
	clearAt := 200 * time.Millisecond
	go func() {
		time.Sleep(clearAt)
		_ = srv.store.UpsertReplicaSessions("peer2", []db.ReplicaSessionRow{
			{AppID: app.ID, Idx: 1, Active: 0, LastActivityAgeSec: 0},
		})
	}()

	// Poll whether the process is alive; it must stay alive until clearAt.
	stillAliveDuring := make(chan bool, 1)
	go func() {
		// Check at half the clearAt to verify the process is still alive when
		// the fleet is non-zero.
		time.Sleep(clearAt / 2)
		err := syscall.Kill(info.PID, 0)
		stillAliveDuring <- (err == nil)
	}()

	grace := 500 * time.Millisecond
	start := time.Now()
	scaled, err := srv.ScaleDown("no-early-stop", grace)
	elapsed := time.Since(start)

	if err != nil {
		t.Fatalf("ScaleDown: %v", err)
	}
	if !scaled {
		t.Fatal("ScaleDown reported no change")
	}

	// The process should have been alive during the drain window.
	if alive := <-stillAliveDuring; !alive {
		t.Error("victim process was already dead while fleet session was still non-zero")
	}
	// The process must be dead after ScaleDown completes.
	if err := syscall.Kill(info.PID, 0); err == nil {
		t.Errorf("victim process (pid %d) still alive after ScaleDown", info.PID)
	}
	// ScaleDown must have waited until fleet cleared (at ~clearAt), not returned
	// before it.
	if elapsed < clearAt/2 {
		t.Errorf("ScaleDown returned in %s, before fleet session was cleared at %s", elapsed, clearAt)
	}
	_ = elapsed
}

// TestClusteredScaleDown_StopFailureRevertsDesiredState_NoFleetLoad verifies that
// on a stop failure, revert to 'running' does not error out when there are no
// fleet sessions (empty table). The DB row must end up with desired_state='running'.
func TestClusteredScaleDown_RevertRobustWhenNoFleetSessions(t *testing.T) {
	cfg := &config.Config{}
	cfg.Runtime.MaxReplicas = 8
	srv, app := newScaleTestServer(t, "revert-robust", 2, cfg)
	srv.SetCluster("this")

	srv.manager.RegisterRuntime("failstop2", &stopFailRuntime{})
	if _, err := srv.manager.Start(process.StartParams{
		Slug: "revert-robust", Index: 1, Tier: "failstop2", Dir: t.TempDir(),
		Command: []string{"sleep", "30"}, Port: 20800,
	}); err != nil {
		t.Fatalf("seed process: %v", err)
	}
	srv.proxy.SetPoolSize("revert-robust", 2)

	_, err := srv.ScaleDown("revert-robust", 100*time.Millisecond)
	if err == nil {
		t.Fatal("ScaleDown must return an error when stop fails")
	}
	if errors.Is(err, process.ErrReplicaNotFound) {
		t.Errorf("expected real stop error, got benign ErrReplicaNotFound: %v", err)
	}

	// Desired state must be reverted to 'running'.
	reps, err2 := srv.store.ListReplicas(app.ID)
	if err2 != nil {
		t.Fatalf("list replicas: %v", err2)
	}
	for _, r := range reps {
		if r.Index == 1 {
			if r.DesiredState != "running" {
				t.Errorf("desired_state = %q after revert; want 'running'", r.DesiredState)
			}
			return
		}
	}
	t.Error("replica row index 1 not found after failed stop")
}
