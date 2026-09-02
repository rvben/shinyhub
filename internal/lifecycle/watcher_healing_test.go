package lifecycle

import (
	"errors"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rvben/shinyhub/internal/db"
	"github.com/rvben/shinyhub/internal/deploy"
	"github.com/rvben/shinyhub/internal/process"
)

// healingDeploy returns a deploy func that records the indices it was asked to
// deploy and returns a fresh remote endpoint/worker on success.
func healingDeploy(calls *[]int) func(slug, dir string, idx int) (*deploy.Result, error) {
	return func(slug, dir string, idx int) (*deploy.Result, error) {
		*calls = append(*calls, idx)
		return &deploy.Result{
			Index: idx, PID: 500 + idx, Port: 9000 + idx,
			Provider: "remote", Tier: "remote",
			EndpointURL: fmt.Sprintf("https://node-b/v1/data/%d", idx),
			WorkerID:    "node-b",
		}, nil
	}
}

func upsertFor(st *fakeStore, index int) (db.UpsertReplicaParams, bool) {
	st.mu.Lock()
	defer st.mu.Unlock()
	for i := len(st.upsertedReplicas) - 1; i >= 0; i-- {
		if st.upsertedReplicas[i].Index == index {
			return st.upsertedReplicas[i], true
		}
	}
	return db.UpsertReplicaParams{}, false
}

// Test 4: a lost replica is re-placed onto a healthy worker. The upsert records
// the new worker_id/endpoint_url and the current DeploymentID; status ends
// running once the slot is back.
func TestReconcileLostReplicas_ReplacesOntoHealthyWorker(t *testing.T) {
	st := newFakeStore(
		map[string]*db.App{"app": {ID: 1, Slug: "app", Status: "running", Replicas: 1}},
		[]*db.Deployment{{ID: 7, Version: "v1", BundleDir: "/bundles/v1"}},
	)
	st.replicas = map[int64][]*db.Replica{
		1: {{AppID: 1, Index: 0, Status: db.ReplicaStatusLost, Tier: "remote", WorkerID: "node-a"}},
	}
	var calls []int
	w := newTestWatcher(Config{RestartMaxAttempts: 5}, &fakeManager{}, newFakeProxy(), st, healingDeploy(&calls))
	w.EnableLostReplicaHealing(func(tier string) bool { return true })

	w.runOnce()

	if len(calls) != 1 || calls[0] != 0 {
		t.Fatalf("expected re-placement deploy for index 0, got %v", calls)
	}
	up, ok := upsertFor(st, 0)
	if !ok {
		t.Fatal("expected UpsertReplica for re-placed slot 0")
	}
	if up.Status != db.ReplicaStatusRunning {
		t.Errorf("status = %q, want running", up.Status)
	}
	if up.WorkerID != "node-b" || up.EndpointURL != "https://node-b/v1/data/0" {
		t.Errorf("re-placement did not persist new worker/endpoint: %+v", up)
	}
	if up.DeploymentID == nil || *up.DeploymentID != 7 {
		t.Errorf("DeploymentID = %v, want 7", up.DeploymentID)
	}
	if st.appStatus["app"] != "running" {
		t.Errorf("app status = %q, want running", st.appStatus["app"])
	}
}

// Test 5: with no healthy worker the gate is false, so re-placement is zero-cost:
// deploy is never entered, the restart budget is untouched, the replica stays
// lost, and the app is marked degraded.
func TestReconcileLostReplicas_NoWorkerIsZeroCost(t *testing.T) {
	st := newFakeStore(
		map[string]*db.App{"app": {ID: 1, Slug: "app", Status: "running", Replicas: 1}},
		[]*db.Deployment{{ID: 7, BundleDir: "/bundles/v1"}},
	)
	st.replicas = map[int64][]*db.Replica{
		1: {{AppID: 1, Index: 0, Status: db.ReplicaStatusLost, Tier: "remote", WorkerID: "node-a"}},
	}
	var deployCount int32
	w := newTestWatcher(Config{RestartMaxAttempts: 5}, &fakeManager{}, newFakeProxy(), st,
		func(slug, dir string, idx int) (*deploy.Result, error) {
			atomic.AddInt32(&deployCount, 1)
			return &deploy.Result{}, nil
		})
	w.EnableLostReplicaHealing(func(tier string) bool { return false })

	w.runOnce()

	if n := atomic.LoadInt32(&deployCount); n != 0 {
		t.Errorf("expected no deploy when no worker available, got %d", n)
	}
	k := replicaKey{"app", 0}
	w.mu.Lock()
	_, hasAttempts := w.attempts[k]
	_, hasRetry := w.nextRetry[k]
	w.mu.Unlock()
	if hasAttempts || hasRetry {
		t.Errorf("expected budget maps untouched, got attempts=%v retry=%v", hasAttempts, hasRetry)
	}
	if got := st.replicas[1][0].Status; got != db.ReplicaStatusLost {
		t.Errorf("replica status = %q, want lost (unchanged)", got)
	}
	if st.appStatus["app"] != "degraded" {
		t.Errorf("app status = %q, want degraded", st.appStatus["app"])
	}
}

// Test 6: a deploy that fails with ErrNoLiveWorker (the gate-vs-start TOCTOU) is
// classified zero-cost: the restart budget is not consumed.
func TestRestartSlot_NoLiveWorkerErrorIsZeroCost(t *testing.T) {
	assertZeroCostError(t, fmt.Errorf("tier %q: %w", "remote", process.ErrNoLiveWorker))
}

// Test 7: a deploy that loses the redeploy race (ErrReplicaAlreadyRunning) is
// also zero-cost. Covers both the lost and crashed callers.
func TestRestartSlot_AlreadyRunningIsZeroCost(t *testing.T) {
	assertZeroCostError(t, fmt.Errorf("start process: %w", process.ErrReplicaAlreadyRunning))
}

func assertZeroCostError(t *testing.T, deployErr error) {
	t.Helper()
	st := newFakeStore(
		map[string]*db.App{"app": {ID: 1, Slug: "app", Status: "running", Replicas: 1}},
		[]*db.Deployment{{ID: 7, BundleDir: "/bundles/v1"}},
	)
	w := newTestWatcher(Config{RestartMaxAttempts: 5}, &fakeManager{}, newFakeProxy(), st,
		func(slug, dir string, idx int) (*deploy.Result, error) { return nil, deployErr })

	w.restartSlot(st.apps["app"], 0, false)

	k := replicaKey{"app", 0}
	w.mu.Lock()
	defer w.mu.Unlock()
	if _, ok := w.attempts[k]; ok {
		t.Errorf("attempts[%v] set; classification should leave budget untouched", k)
	}
	if _, ok := w.nextRetry[k]; ok {
		t.Errorf("nextRetry[%v] set; classification should leave budget untouched", k)
	}
}

// Test 9: a lost replica auto-heals once a worker returns. Tick 1 has no worker
// (gate false) and the app goes degraded; tick 2 has a worker (gate true) and
// the slot is re-placed, returning the app to running. Proves degraded apps
// remain enumerable via ListReconcilableApps.
func TestReconcileLostReplicas_AutoHealsAfterWorkerReturns(t *testing.T) {
	st := newFakeStore(
		map[string]*db.App{"app": {ID: 1, Slug: "app", Status: "running", Replicas: 1}},
		[]*db.Deployment{{ID: 7, BundleDir: "/bundles/v1"}},
	)
	st.replicas = map[int64][]*db.Replica{
		1: {{AppID: 1, Index: 0, Status: db.ReplicaStatusLost, Tier: "remote", WorkerID: "node-a"}},
	}
	var workerUp atomic.Bool
	var calls []int
	w := newTestWatcher(Config{RestartMaxAttempts: 5}, &fakeManager{}, newFakeProxy(), st, healingDeploy(&calls))
	w.EnableLostReplicaHealing(func(tier string) bool { return workerUp.Load() })

	w.runOnce() // no worker yet
	if len(calls) != 0 {
		t.Fatalf("expected no re-placement before worker returns, got %v", calls)
	}
	if st.appStatus["app"] != "degraded" {
		t.Fatalf("expected degraded before worker returns, got %q", st.appStatus["app"])
	}

	workerUp.Store(true)
	w.runOnce() // worker back: re-place

	if len(calls) != 1 || calls[0] != 0 {
		t.Fatalf("expected re-placement of slot 0 after worker returns, got %v", calls)
	}
	if st.appStatus["app"] != "running" {
		t.Errorf("expected running after auto-heal, got %q", st.appStatus["app"])
	}
}

// Test 10: a degraded app whose crashed slot has no manager entry is still
// restarted (the rev-3 unification onto ListReconcilableApps closed this
// starvation hole; ListRunningApps would have excluded the degraded app).
func TestReconcileReplicas_RecoversCrashedSlotInDegradedApp(t *testing.T) {
	mgr := &fakeManager{entries: []*process.ProcessInfo{
		{Slug: "app", Index: 0, Status: process.StatusRunning},
	}}
	st := newFakeStore(
		map[string]*db.App{"app": {ID: 1, Slug: "app", Status: "degraded", Replicas: 2}},
		[]*db.Deployment{{ID: 7, BundleDir: "/bundles/v1"}},
	)
	st.replicas = map[int64][]*db.Replica{
		1: {
			{AppID: 1, Index: 0, Status: "running"},
			{AppID: 1, Index: 1, Status: "crashed"},
		},
	}
	var calls []int
	w := newTestWatcher(Config{RestartMaxAttempts: 5}, mgr, newFakeProxy(), st, healingDeploy(&calls))

	w.runOnce()

	if len(calls) != 1 || calls[0] != 1 {
		t.Fatalf("expected crashed slot 1 restarted in degraded app, got %v", calls)
	}
	if st.appStatus["app"] != "running" {
		t.Errorf("expected running after recovery, got %q", st.appStatus["app"])
	}
}

// Test 11: reconcileAppStatus is the running<->degraded authority and ignores
// non-reconcilable statuses.
func TestReconcileAppStatus_DegradedWhenAnySlotDown(t *testing.T) {
	st := newFakeStore(map[string]*db.App{"app": {ID: 1, Slug: "app", Status: "running", Replicas: 2}}, nil)
	st.replicas = map[int64][]*db.Replica{
		1: {{AppID: 1, Index: 0, Status: "running"}, {AppID: 1, Index: 1, Status: db.ReplicaStatusLost}},
	}
	w := newTestWatcher(Config{}, &fakeManager{}, newFakeProxy(), st, nil)

	w.reconcileAppStatus(st.apps["app"], st.replicas[1])

	if st.appStatus["app"] != "degraded" {
		t.Errorf("status = %q, want degraded", st.appStatus["app"])
	}
}

func TestReconcileAppStatus_RunningWhenAllRunning(t *testing.T) {
	st := newFakeStore(map[string]*db.App{"app": {ID: 1, Slug: "app", Status: "degraded", Replicas: 2}}, nil)
	st.replicas = map[int64][]*db.Replica{
		1: {{AppID: 1, Index: 0, Status: "running"}, {AppID: 1, Index: 1, Status: "running"}},
	}
	w := newTestWatcher(Config{}, &fakeManager{}, newFakeProxy(), st, nil)

	w.reconcileAppStatus(st.apps["app"], st.replicas[1])

	if st.appStatus["app"] != "running" {
		t.Errorf("status = %q, want running", st.appStatus["app"])
	}
}

func TestReconcileAppStatus_ElasticEmptyPoolIsHealthy(t *testing.T) {
	st := newFakeStore(map[string]*db.App{
		"app": {ID: 1, Slug: "app", Status: "degraded", Replicas: 1, WorkerIsolation: "grouped"},
	}, nil)
	w := newTestWatcher(Config{}, &fakeManager{}, newFakeProxy(), st, nil)
	w.reconcileAppStatus(st.apps["app"], nil)
	if st.appStatus["app"] != "running" {
		t.Fatalf("grouped app with an idle empty pool = %q, want running", st.appStatus["app"])
	}

	st = newFakeStore(map[string]*db.App{
		"app": {ID: 1, Slug: "app", Status: "degraded", Replicas: 1},
	}, nil)
	w = newTestWatcher(Config{DefaultWorkerIsolation: "per_session"}, &fakeManager{}, newFakeProxy(), st, nil)
	w.reconcileAppStatus(st.apps["app"], nil)
	if st.appStatus["app"] != "running" {
		t.Fatalf("inherited elastic app = %q, want running", st.appStatus["app"])
	}

	st = newFakeStore(map[string]*db.App{
		"app": {ID: 1, Slug: "app", Status: "degraded", Replicas: 1, WorkerIsolation: "grouped", LastDeploymentStatus: db.DeploymentFailed},
	}, nil)
	w = newTestWatcher(Config{}, &fakeManager{}, newFakeProxy(), st, nil)
	w.reconcileAppStatus(st.apps["app"], nil)
	if st.appStatus["app"] != "degraded" {
		t.Fatalf("real elastic deployment failure was erased: %q", st.appStatus["app"])
	}
}

func TestReconcileAppStatus_IgnoresHibernatedAndDeploying(t *testing.T) {
	for _, status := range []string{"hibernated", "deploying", "stopped"} {
		st := newFakeStore(map[string]*db.App{"app": {ID: 1, Slug: "app", Status: status, Replicas: 2}}, nil)
		st.replicas = map[int64][]*db.Replica{1: {{AppID: 1, Index: 0, Status: "running"}}}
		w := newTestWatcher(Config{}, &fakeManager{}, newFakeProxy(), st, nil)

		w.reconcileAppStatus(st.apps["app"], st.replicas[1])

		if st.appStatus["app"] != status {
			t.Errorf("status %q was changed to %q; should be left untouched", status, st.appStatus["app"])
		}
	}
}

// Test 12: a restart persists the current DeploymentID (a field the old crash
// path dropped, orphaning the row from its deployment for recovery matching).
func TestRestartSlot_PersistsDeploymentID(t *testing.T) {
	st := newFakeStore(
		map[string]*db.App{"app": {ID: 1, Slug: "app", Status: "running", Replicas: 1}},
		[]*db.Deployment{{ID: 42, Version: "v3", BundleDir: "/bundles/v3"}},
	)
	var calls []int
	w := newTestWatcher(Config{RestartMaxAttempts: 5}, &fakeManager{}, newFakeProxy(), st, healingDeploy(&calls))

	w.restartSlot(st.apps["app"], 0, false)

	up, ok := upsertFor(st, 0)
	if !ok {
		t.Fatal("expected UpsertReplica")
	}
	if up.DeploymentID == nil || *up.DeploymentID != 42 {
		t.Errorf("DeploymentID = %v, want 42", up.DeploymentID)
	}
}

// TestReconcileAppStatus_WarmRowsKeepRunning pins the oscillation blocker: an
// app with N=3 replicas where two are warm-parked must stay "running" across
// consecutive reconcile ticks, never oscillating to "degraded".
// Warm rows are deliberately stopped capacity; they are not failures.
func TestReconcileAppStatus_WarmRowsKeepRunning(t *testing.T) {
	st := newFakeStore(map[string]*db.App{
		"app": {ID: 1, Slug: "app", Status: "running", Replicas: 3},
	}, nil)
	st.replicas = map[int64][]*db.Replica{
		1: {
			{AppID: 1, Index: 0, Status: db.ReplicaStatusRunning, DesiredState: "running"},
			{AppID: 1, Index: 1, Status: "stopped", DesiredState: db.ReplicaDesiredWarm},
			{AppID: 1, Index: 2, Status: "stopped", DesiredState: db.ReplicaDesiredWarm},
		},
	}
	w := newTestWatcher(Config{}, &fakeManager{}, newFakeProxy(), st, nil)

	// Run twice to prove no oscillation.
	w.reconcileAppStatus(st.apps["app"], st.replicas[1])
	if st.appStatus["app"] != "running" {
		t.Errorf("tick 1: status = %q, want running (warm victims are healthy stopped capacity)", st.appStatus["app"])
	}
	w.reconcileAppStatus(st.apps["app"], st.replicas[1])
	if st.appStatus["app"] != "running" {
		t.Errorf("tick 2: status = %q, want running (oscillation detected)", st.appStatus["app"])
	}
}

// TestReconcileAppStatus_CrashStillDegrades verifies that a genuinely missing
// replica (not warm-parked) still drives the app to "degraded" even when other
// replicas are warm-parked.
func TestReconcileAppStatus_CrashStillDegrades(t *testing.T) {
	st := newFakeStore(map[string]*db.App{
		"app": {ID: 1, Slug: "app", Status: "running", Replicas: 3},
	}, nil)
	st.replicas = map[int64][]*db.Replica{
		1: {
			{AppID: 1, Index: 0, Status: "crashed", DesiredState: "running"},
			{AppID: 1, Index: 1, Status: "stopped", DesiredState: db.ReplicaDesiredWarm},
			{AppID: 1, Index: 2, Status: db.ReplicaStatusRunning, DesiredState: "running"},
		},
	}
	w := newTestWatcher(Config{}, &fakeManager{}, newFakeProxy(), st, nil)

	w.reconcileAppStatus(st.apps["app"], st.replicas[1])

	if st.appStatus["app"] != "degraded" {
		t.Errorf("status = %q, want degraded (one genuinely missing replica)", st.appStatus["app"])
	}
}

// TestReconcileReplicas_NeverRestartsWarm verifies that a warm-parked replica
// (status=stopped, desired_state='warm') passes through reconcileReplicas
// without triggering restartSlot. The switch only handles crashed/lost rows;
// warm rows are status=stopped which is not a matched case.
func TestReconcileReplicas_NeverRestartsWarm(t *testing.T) {
	st := newFakeStore(
		map[string]*db.App{"app": {ID: 1, Slug: "app", Status: "running", Replicas: 3}},
		[]*db.Deployment{{ID: 7, BundleDir: "/bundles/v1"}},
	)
	st.replicas = map[int64][]*db.Replica{
		1: {
			{AppID: 1, Index: 0, Status: db.ReplicaStatusRunning, DesiredState: "running"},
			{AppID: 1, Index: 1, Status: "stopped", DesiredState: db.ReplicaDesiredWarm},
			{AppID: 1, Index: 2, Status: "stopped", DesiredState: db.ReplicaDesiredWarm},
		},
	}
	var deployCount int32
	w := newTestWatcher(Config{RestartMaxAttempts: 5}, &fakeManager{}, newFakeProxy(), st,
		func(slug, dir string, idx int) (*deploy.Result, error) {
			atomic.AddInt32(&deployCount, 1)
			return &deploy.Result{Index: idx, PID: 100, Port: 9000}, nil
		})

	w.reconcileReplicas([]*db.App{st.apps["app"]}, st.replicas, map[replicaKey]bool{})

	if n := atomic.LoadInt32(&deployCount); n != 0 {
		t.Errorf("expected no restart for warm rows, got %d deploy calls", n)
	}
}

// An app terminalized as crashed while its replicas are lost is stranded
// twice over: crashed apps have left ListReconcilableApps, and the spent
// restart budget would block the slot even if they had not. Both can happen
// when a worker dies: restart attempts against the not-yet-swept dead worker
// fail as ordinary deploy errors (burning budget) until the heartbeat sweep
// rules the replicas lost. The reviver must return exactly this state to the
// normal healing path: revive to degraded, fresh budget, re-place, running.
func TestRunOnce_RevivesCrashedAppWithLostReplicas(t *testing.T) {
	st := newFakeStore(
		map[string]*db.App{"app": {ID: 1, Slug: "app", Status: "crashed", Replicas: 1}},
		[]*db.Deployment{{ID: 7, Version: "v1", BundleDir: "/bundles/v1"}},
	)
	st.replicas = map[int64][]*db.Replica{
		1: {{AppID: 1, Index: 0, Status: db.ReplicaStatusLost, Tier: "remote", WorkerID: "node-a"}},
	}
	var calls []int
	w := newTestWatcher(Config{RestartMaxAttempts: 5}, &fakeManager{}, newFakeProxy(), st, healingDeploy(&calls))
	w.EnableLostReplicaHealing(func(tier string) bool { return true })

	// The budget spent against the dead worker before the sweep ruled the
	// replica lost; without a reset it blocks the re-placement forever.
	k := replicaKey{"app", 0}
	w.mu.Lock()
	w.attempts[k] = 5
	w.mu.Unlock()

	w.runOnce()

	if len(calls) != 1 || calls[0] != 0 {
		t.Fatalf("expected re-placement deploy for slot 0, got %v", calls)
	}
	if st.appStatus["app"] != "running" {
		t.Errorf("app status = %q, want running after revive + re-place", st.appStatus["app"])
	}
	w.mu.Lock()
	_, hasAttempts := w.attempts[k]
	w.mu.Unlock()
	if hasAttempts {
		t.Error("restart budget spent against the dead worker was not cleared")
	}
}

// The reviver must NOT fire while the lost replica cannot actually be healed
// (no healthy worker for its tier): reviving then would trade a stable
// terminal state for a degraded app that makes no progress, and - with a
// crash-looping sibling slot - would undo every terminalization the looper
// earns, granting it unlimited restarts. The crashed state stands until a
// healthy worker exists; the next tick after that revives and heals together.
func TestRunOnce_DoesNotReviveWhileLostSlotUnhealable(t *testing.T) {
	st := newFakeStore(
		map[string]*db.App{"app": {ID: 1, Slug: "app", Status: "crashed", Replicas: 1}},
		[]*db.Deployment{{ID: 7, BundleDir: "/bundles/v1"}},
	)
	st.replicas = map[int64][]*db.Replica{
		1: {{AppID: 1, Index: 0, Status: db.ReplicaStatusLost, Tier: "remote", WorkerID: "node-a"}},
	}
	var calls []int
	w := newTestWatcher(Config{RestartMaxAttempts: 5}, &fakeManager{}, newFakeProxy(), st, healingDeploy(&calls))
	w.EnableLostReplicaHealing(func(tier string) bool { return false })

	k := replicaKey{"app", 0}
	w.mu.Lock()
	w.attempts[k] = 5
	w.mu.Unlock()

	w.runOnce()

	if len(calls) != 0 {
		t.Fatalf("unhealable lost slot was deployed: %v", calls)
	}
	if st.appStatus["app"] != "crashed" {
		t.Errorf("app status = %q, want crashed (revive deferred until a worker exists)", st.appStatus["app"])
	}
	w.mu.Lock()
	spent := w.attempts[k]
	w.mu.Unlock()
	if spent != 5 {
		t.Errorf("attempts = %d, want 5 (budget untouched while unhealable)", spent)
	}
}

// A revive clears the budget of the LOST slots only. A sibling slot that
// crash-looped its way to a spent budget on a healthy worker keeps that
// verdict; granting it fresh budget on every revive would let a persistent
// lost row fund unlimited restarts of a genuinely broken replica.
func TestRunOnce_ReviveClearsOnlyLostSlotBudget(t *testing.T) {
	st := newFakeStore(
		map[string]*db.App{"app": {ID: 1, Slug: "app", Status: "crashed", Replicas: 2}},
		[]*db.Deployment{{ID: 7, BundleDir: "/bundles/v1"}},
	)
	st.replicas = map[int64][]*db.Replica{
		1: {
			{AppID: 1, Index: 0, Status: "crashed", Tier: "remote", WorkerID: "node-b"},
			{AppID: 1, Index: 1, Status: db.ReplicaStatusLost, Tier: "remote", WorkerID: "node-a"},
		},
	}
	var calls []int
	w := newTestWatcher(Config{RestartMaxAttempts: 5}, &fakeManager{}, newFakeProxy(), st, healingDeploy(&calls))
	w.EnableLostReplicaHealing(func(tier string) bool { return true })

	crashLooper := replicaKey{"app", 0}
	lostSlot := replicaKey{"app", 1}
	w.mu.Lock()
	w.attempts[crashLooper] = 5
	w.attempts[lostSlot] = 5
	w.mu.Unlock()

	w.runOnce()

	if len(calls) != 1 || calls[0] != 1 {
		t.Fatalf("deploys = %v, want only the lost slot 1 re-placed", calls)
	}
	w.mu.Lock()
	looperSpent := w.attempts[crashLooper]
	_, lostHasBudget := w.attempts[lostSlot]
	w.mu.Unlock()
	if looperSpent != 5 {
		t.Errorf("crash-looper attempts = %d, want 5 (verdict preserved across revive)", looperSpent)
	}
	if lostHasBudget {
		t.Error("lost slot budget was not cleared by the revive")
	}
	if st.appStatus["app"] == "crashed" {
		t.Error("app was not revived despite a healable lost slot")
	}
}

// The reviver's negative control: a crashed app whose replicas merely crashed
// (a genuine app fault, e.g. a crash loop) is NOT revived - terminalization is
// the correct, deliberate outcome there and reviving would flap the app.
func TestRunOnce_LeavesCrashedAppWithoutLostReplicasAlone(t *testing.T) {
	st := newFakeStore(
		map[string]*db.App{"app": {ID: 1, Slug: "app", Status: "crashed", Replicas: 1}},
		[]*db.Deployment{{ID: 7, BundleDir: "/bundles/v1"}},
	)
	st.replicas = map[int64][]*db.Replica{
		1: {{AppID: 1, Index: 0, Status: "crashed", Tier: "remote", WorkerID: "node-a"}},
	}
	var calls []int
	w := newTestWatcher(Config{RestartMaxAttempts: 5}, &fakeManager{}, newFakeProxy(), st, healingDeploy(&calls))
	w.EnableLostReplicaHealing(func(tier string) bool { return true })

	w.runOnce()

	if len(calls) != 0 {
		t.Fatalf("crash-looped app was restarted: deploys %v", calls)
	}
	if st.appStatus["app"] != "crashed" {
		t.Errorf("app status = %q, want crashed (untouched)", st.appStatus["app"])
	}
}

// A re-placement launched FROM a lost row that fails with an app fault (not
// ErrNoLiveWorker) surfaces that failure on the row: lost becomes crashed with
// the deploy error's diagnostics. This is the one transition allowed to
// overwrite lost - the caller holds the app lease, and the loss sweep no-ops
// on rows already lost.
func TestRestartSlot_LostRestartFailureRecordsCrash(t *testing.T) {
	st := newFakeStore(
		map[string]*db.App{"app": {ID: 1, Slug: "app", Status: "running", Replicas: 1}},
		[]*db.Deployment{{ID: 7, BundleDir: "/bundles/v1"}},
	)
	st.replicas = map[int64][]*db.Replica{
		1: {{AppID: 1, Index: 0, Status: db.ReplicaStatusLost, Tier: "remote", WorkerID: "node-a"}},
	}
	w := newTestWatcher(Config{RestartMaxAttempts: 5}, &fakeManager{}, newFakeProxy(), st,
		func(slug, dir string, idx int) (*deploy.Result, error) {
			return nil, fmt.Errorf("bundle install failed")
		})
	w.EnableLostReplicaHealing(func(tier string) bool { return true })

	w.runOnce()

	if got := st.replicas[1][0].Status; got != "crashed" {
		t.Errorf("replica status = %q, want crashed (re-placement failure is an app fault)", got)
	}
}

// A crashed-slot restart whose failure lands AFTER the down-sweep ruled the
// row lost must not pull it back to crashed: the sweep writes the loss verdict
// without the app lease, so this interleaving is real, and the loss (the
// worker is gone) is the later, authoritative fact. The store-level guard in
// RecordReplicaCrash is what preserves it.
func TestRestartSlot_CrashRestartFailureCannotUndoLoss(t *testing.T) {
	st := newFakeStore(
		map[string]*db.App{"app": {ID: 1, Slug: "app", Status: "running", Replicas: 1}},
		[]*db.Deployment{{ID: 7, BundleDir: "/bundles/v1"}},
	)
	st.replicas = map[int64][]*db.Replica{
		1: {{AppID: 1, Index: 0, Status: "crashed", Tier: "remote", WorkerID: "node-a"}},
	}
	w := newTestWatcher(Config{RestartMaxAttempts: 5}, &fakeManager{}, newFakeProxy(), st,
		func(slug, dir string, idx int) (*deploy.Result, error) {
			// The sweep wins the race while the restart deploy is in flight.
			st.mu.Lock()
			st.replicas[1][0].Status = db.ReplicaStatusLost
			st.mu.Unlock()
			return nil, fmt.Errorf("dial worker: connection refused")
		})

	w.restartSlot(st.apps["app"], 0, false)

	if got := st.replicas[1][0].Status; got != db.ReplicaStatusLost {
		t.Errorf("replica status = %q, want lost (late crash write undid the loss verdict)", got)
	}
}

// A lost slot whose restart budget was spent BEFORE the sweep ruled it lost
// (retries against the dead-but-not-yet-swept worker fail as app faults) must
// still be re-placed. The crashed-app reviver cannot help here: a sibling
// replica is still running, so the app is degraded, not crashed, and degraded
// apps never enter reviveIfHealable. Without drive-time forgiveness the spent
// budget blocks restartSlot forever and the slot never heals.
func TestReconcileLostReplicas_SpentBudgetForgivenForLostSlot(t *testing.T) {
	st := newFakeStore(
		map[string]*db.App{"app": {ID: 1, Slug: "app", Status: "degraded", Replicas: 2}},
		[]*db.Deployment{{ID: 7, Version: "v1", BundleDir: "/bundles/v1"}},
	)
	st.replicas = map[int64][]*db.Replica{
		1: {
			{AppID: 1, Index: 0, Status: db.ReplicaStatusRunning, Tier: "remote", WorkerID: "node-b"},
			{AppID: 1, Index: 1, Status: db.ReplicaStatusLost, Tier: "remote", WorkerID: "node-a"},
		},
	}
	var calls []int
	w := newTestWatcher(Config{RestartMaxAttempts: 5}, &fakeManager{}, newFakeProxy(), st, healingDeploy(&calls))
	w.EnableLostReplicaHealing(func(tier string) bool { return true })

	// Budget and backoff spent against the dying worker before the sweep ruled
	// the slot lost, plus a runtime crash-loop count from the same dying worker.
	k := replicaKey{"app", 1}
	w.mu.Lock()
	w.attempts[k] = 5
	w.nextRetry[k] = time.Now().Add(time.Hour)
	w.crashCount[k] = 3
	w.mu.Unlock()

	w.runOnce()

	if len(calls) != 1 || calls[0] != 1 {
		t.Fatalf("deploys = %v, want lost slot 1 re-placed despite the spent budget", calls)
	}
	w.mu.Lock()
	_, hasCrashCount := w.crashCount[k]
	w.mu.Unlock()
	if hasCrashCount {
		t.Error("crash-loop count from the dead-worker era survived the loss verdict")
	}
}

// The reviver must skip elastic (grouped/per_session) apps entirely: they are
// demand-driven with no durable replica pool, so any lost row on a crashed
// elastic app is a stale multiplex-era leftover (an isolation change on a
// non-running app neither redeploys nor clears rows). Reviving from it would
// hand reconcileReplicas a row that boots a durable replica for an app that
// must not have one, and would flip a terminal crashed state to running with
// no healing event behind it.
func TestRunOnce_DoesNotReviveElasticApp(t *testing.T) {
	cases := []struct {
		name       string
		perApp     string
		defaultIso string
	}{
		{"explicit grouped", "grouped", ""},
		{"fleet default per_session", "", "per_session"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			st := newFakeStore(
				map[string]*db.App{"app": {ID: 1, Slug: "app", Status: "crashed", Replicas: 1, WorkerIsolation: tc.perApp}},
				[]*db.Deployment{{ID: 7, BundleDir: "/bundles/v1"}},
			)
			st.replicas = map[int64][]*db.Replica{
				1: {{AppID: 1, Index: 0, Status: db.ReplicaStatusLost, Tier: "remote", WorkerID: "node-a"}},
			}
			var calls []int
			w := newTestWatcher(Config{RestartMaxAttempts: 5, DefaultWorkerIsolation: tc.defaultIso},
				&fakeManager{}, newFakeProxy(), st, healingDeploy(&calls))
			w.EnableLostReplicaHealing(func(tier string) bool { return true })

			w.runOnce()

			if len(calls) != 0 {
				t.Fatalf("elastic app got a durable replica deployed: %v", calls)
			}
			if st.appStatus["app"] != "crashed" {
				t.Errorf("app status = %q, want crashed (elastic apps are not revived from replica rows)", st.appStatus["app"])
			}
		})
	}
}

// Test 13: with healing disabled (nil gate) lost slots are left alone, but
// crashed slots are still recovered.
func TestReconcileReplicas_HealingDisabledSkipsLostButStillHandlesCrashed(t *testing.T) {
	st := newFakeStore(
		map[string]*db.App{"app": {ID: 1, Slug: "app", Status: "running", Replicas: 2}},
		[]*db.Deployment{{ID: 7, BundleDir: "/bundles/v1"}},
	)
	st.replicas = map[int64][]*db.Replica{
		1: {
			{AppID: 1, Index: 0, Status: "crashed"},
			{AppID: 1, Index: 1, Status: db.ReplicaStatusLost, Tier: "remote"},
		},
	}
	var calls []int
	w := newTestWatcher(Config{RestartMaxAttempts: 5}, &fakeManager{}, newFakeProxy(), st, healingDeploy(&calls))
	// EnableLostReplicaHealing intentionally not called: tierHealthy stays nil.

	w.runOnce()

	if len(calls) != 1 || calls[0] != 0 {
		t.Fatalf("expected only crashed slot 0 restarted, got %v", calls)
	}
	if got := st.replicas[1][1].Status; got != db.ReplicaStatusLost {
		t.Errorf("lost slot 1 status = %q, want lost (untouched while healing disabled)", got)
	}
}

// Test 14: lost-slot budget forgiveness fires once per loss episode, not once
// per drive. When the re-placement deploy fails AND the lost-to-crashed crash
// write also fails (DB unavailable), the row stays lost; the next tick must
// respect the backoff recorded against that failure instead of re-forgiving it
// and retrying immediately. Once the episode's backoff expires and the DB is
// back, the slot still heals normally.
func TestRestartSlot_LostForgivenessOncePerEpisode(t *testing.T) {
	st := newFakeStore(
		map[string]*db.App{"app": {ID: 1, Slug: "app", Status: "running", Replicas: 1}},
		[]*db.Deployment{{ID: 7, BundleDir: "/bundles/v1"}},
	)
	st.replicas = map[int64][]*db.Replica{
		1: {{AppID: 1, Index: 0, Status: db.ReplicaStatusLost, Tier: "remote", WorkerID: "node-a"}},
	}
	// The crash write failure: RecordReplicaCrashFromLost persists through
	// UpsertReplica, so upsertErr makes it fail while leaving the row lost.
	st.upsertErr = errors.New("database unavailable")

	var deploys atomic.Int32
	var deployFails atomic.Bool
	deployFails.Store(true)
	w := newTestWatcher(Config{RestartMaxAttempts: 5}, &fakeManager{}, newFakeProxy(), st,
		func(slug, dir string, idx int) (*deploy.Result, error) {
			deploys.Add(1)
			if deployFails.Load() {
				return nil, errors.New("bundle boot failed")
			}
			return &deploy.Result{
				Index: idx, PID: 500 + idx, Port: 9000 + idx,
				Provider: "remote", Tier: "remote",
				EndpointURL: fmt.Sprintf("https://node-b/v1/data/%d", idx),
				WorkerID:    "node-b",
			}, nil
		})
	w.EnableLostReplicaHealing(func(tier string) bool { return true })

	// Tick 1: first sight of the loss forgives (empty budget), the deploy fails,
	// the crash write fails, the row stays lost, and one attempt + a backoff are
	// recorded against the failure.
	w.runOnce()
	if n := deploys.Load(); n != 1 {
		t.Fatalf("tick 1: deploys = %d, want 1", n)
	}
	if got := st.replicas[1][0].Status; got != db.ReplicaStatusLost {
		t.Fatalf("tick 1: replica status = %q, want lost (crash write failed)", got)
	}

	// Tick 2, immediately: still the same loss episode. Re-forgiving here would
	// erase the backoff and deploy again; the episode flag must suppress that.
	w.runOnce()
	if n := deploys.Load(); n != 1 {
		t.Fatalf("tick 2: deploys = %d, want 1 (backoff must survive the second drive)", n)
	}
	k := replicaKey{"app", 0}
	w.mu.Lock()
	attempts := w.attempts[k]
	_, hasRetry := w.nextRetry[k]
	w.mu.Unlock()
	if attempts != 1 || !hasRetry {
		t.Fatalf("tick 2: attempts=%d hasRetry=%v, want budget preserved (1, true)", attempts, hasRetry)
	}

	// Backoff expires and the DB comes back: the slot heals on the next tick,
	// and healing ends the episode so a future loss forgives afresh.
	st.mu.Lock()
	st.upsertErr = nil
	st.mu.Unlock()
	deployFails.Store(false)
	w.mu.Lock()
	w.nextRetry[k] = time.Now().Add(-time.Second)
	w.mu.Unlock()

	w.runOnce()
	if n := deploys.Load(); n != 2 {
		t.Fatalf("tick 3: deploys = %d, want 2 (heal after backoff expiry)", n)
	}
	if got := st.replicas[1][0].Status; got != db.ReplicaStatusRunning {
		t.Errorf("tick 3: replica status = %q, want running", got)
	}
	if st.appStatus["app"] != "running" {
		t.Errorf("tick 3: app status = %q, want running", st.appStatus["app"])
	}
}

// Test 15: reviving a crash-loop-terminalized app re-places the sibling rows the
// terminalization stopped but never rewrote. handleCrashedLocked stops the whole
// pool yet touches no sibling replica rows, so a healthy-at-that-moment sibling
// keeps status running with a dead process; after a revive that row must be
// normalized to crashed and re-placed in the same tick, while a sibling whose
// runtime IS live is left alone.
func TestRunOnce_ReviveNormalizesStoppedRunningSiblings(t *testing.T) {
	mgr := &fakeManager{entries: []*process.ProcessInfo{
		{Slug: "app", Index: 0, Status: process.StatusRunning}, // slot 0 genuinely live
	}}
	st := newFakeStore(
		map[string]*db.App{"app": {ID: 1, Slug: "app", Status: "crashed", Replicas: 3}},
		[]*db.Deployment{{ID: 7, Version: "v1", BundleDir: "/bundles/v1"}},
	)
	st.replicas = map[int64][]*db.Replica{
		1: {
			{AppID: 1, Index: 0, Status: "running"}, // live runtime: untouched
			{AppID: 1, Index: 1, Status: "running"}, // dead runtime: normalize + re-place
			{AppID: 1, Index: 2, Status: db.ReplicaStatusLost, Tier: "remote", WorkerID: "node-a"},
		},
	}
	var calls []int
	w := newTestWatcher(Config{RestartMaxAttempts: 5}, mgr, newFakeProxy(), st, healingDeploy(&calls))
	w.EnableLostReplicaHealing(func(tier string) bool { return true })

	w.runOnce()

	if len(calls) != 2 || calls[0] != 1 || calls[1] != 2 {
		t.Fatalf("expected re-placement of dead sibling 1 and lost slot 2, got %v", calls)
	}
	// The normalization itself: slot 1's first write is the crashed rewrite that
	// handed it to the ordinary heal path.
	st.mu.Lock()
	var slot1First *db.UpsertReplicaParams
	for i := range st.upsertedReplicas {
		if st.upsertedReplicas[i].Index == 1 {
			slot1First = &st.upsertedReplicas[i]
			break
		}
	}
	var slot0Writes int
	for i := range st.upsertedReplicas {
		if st.upsertedReplicas[i].Index == 0 {
			slot0Writes++
		}
	}
	st.mu.Unlock()
	if slot1First == nil || slot1First.Status != "crashed" {
		t.Fatalf("expected slot 1 normalized to crashed before re-placement, got %+v", slot1First)
	}
	if slot0Writes != 0 {
		t.Errorf("live sibling slot 0 was written %d times, want untouched", slot0Writes)
	}
	up, ok := upsertFor(st, 1)
	if !ok || up.Status != db.ReplicaStatusRunning || up.WorkerID != "node-b" {
		t.Errorf("slot 1 not re-placed onto healthy worker: ok=%v %+v", ok, up)
	}
	if st.appStatus["app"] != "running" {
		t.Errorf("app status = %q, want running after full heal", st.appStatus["app"])
	}
}

// Test 16: the crashed-app reviver honors the durable schedule-activation
// fence, like every other watcher path that mutates an app's runtime. While an
// activation is in flight (e.g. requeued as repairing after a process crash)
// the app's status and replica rows are the activation's to mutate: no revive,
// no sibling normalization. The fence lifting is what admits the revive.
func TestRunOnce_ReviveHonorsActivationFence(t *testing.T) {
	st := newFakeStore(
		map[string]*db.App{"app": {ID: 1, Slug: "app", Status: "crashed", Replicas: 2}},
		[]*db.Deployment{{ID: 7, Version: "v1", BundleDir: "/bundles/v1"}},
	)
	st.replicas = map[int64][]*db.Replica{
		1: {
			{AppID: 1, Index: 0, Status: "running"}, // dead runtime, activation-owned
			{AppID: 1, Index: 1, Status: db.ReplicaStatusLost, Tier: "remote", WorkerID: "node-a"},
		},
	}
	st.activationInFlight = true
	var calls []int
	w := newTestWatcher(Config{RestartMaxAttempts: 5}, &fakeManager{}, newFakeProxy(), st, healingDeploy(&calls))
	w.EnableLostReplicaHealing(func(tier string) bool { return true })

	w.runOnce()

	if st.appStatus["app"] != "crashed" {
		t.Fatalf("app status = %q, want crashed (revive fenced while activation in flight)", st.appStatus["app"])
	}
	if got := st.replicas[1][0].Status; got != db.ReplicaStatusRunning {
		t.Fatalf("sibling row status = %q, want running (activation-owned rows untouched)", got)
	}
	if len(calls) != 0 {
		t.Fatalf("expected no deploys while fenced, got %v", calls)
	}

	// The activation resolves: the next tick revives, normalizes, and heals.
	st.mu.Lock()
	st.activationInFlight = false
	st.mu.Unlock()

	w.runOnce()

	if len(calls) != 2 || calls[0] != 0 || calls[1] != 1 {
		t.Fatalf("expected both slots re-placed after fence lifted, got %v", calls)
	}
	if st.appStatus["app"] != "running" {
		t.Errorf("app status = %q, want running after heal", st.appStatus["app"])
	}
}

// Test 17: a failed sibling normalization defers the revive instead of leaving
// permanent debris. If the crashed rewrite cannot be persisted after the app is
// already revived, no later tick retries it (the reviver only selects crashed
// apps) and the dead sibling row reads healthy forever. So the rewrite happens
// first, and the app stays crashed until every rewrite lands; the write coming
// back admits the full revive.
func TestRunOnce_ReviveDeferredUntilNormalizationPersists(t *testing.T) {
	st := newFakeStore(
		map[string]*db.App{"app": {ID: 1, Slug: "app", Status: "crashed", Replicas: 2}},
		[]*db.Deployment{{ID: 7, Version: "v1", BundleDir: "/bundles/v1"}},
	)
	st.replicas = map[int64][]*db.Replica{
		1: {
			{AppID: 1, Index: 0, Status: "running"}, // dead runtime to normalize
			{AppID: 1, Index: 1, Status: db.ReplicaStatusLost, Tier: "remote", WorkerID: "node-a"},
		},
	}
	st.recordCrashErr = errors.New("database unavailable")
	var calls []int
	w := newTestWatcher(Config{RestartMaxAttempts: 5}, &fakeManager{}, newFakeProxy(), st, healingDeploy(&calls))
	w.EnableLostReplicaHealing(func(tier string) bool { return true })

	w.runOnce()

	if st.appStatus["app"] != "crashed" {
		t.Fatalf("app status = %q, want crashed (revive deferred while normalization fails)", st.appStatus["app"])
	}
	if len(calls) != 0 {
		t.Fatalf("expected no deploys while revive deferred, got %v", calls)
	}

	// The write path recovers: the next tick normalizes, revives, and heals.
	st.mu.Lock()
	st.recordCrashErr = nil
	st.mu.Unlock()

	w.runOnce()

	if len(calls) != 2 || calls[0] != 0 || calls[1] != 1 {
		t.Fatalf("expected both slots re-placed after write path recovered, got %v", calls)
	}
	up, ok := upsertFor(st, 0)
	if !ok || up.Status != db.ReplicaStatusRunning || up.WorkerID != "node-b" {
		t.Errorf("dead sibling not re-placed onto healthy worker: ok=%v %+v", ok, up)
	}
	if st.appStatus["app"] != "running" {
		t.Errorf("app status = %q, want running after heal", st.appStatus["app"])
	}
}

// Crash-loop terminalization deregisters the app's entire proxy pool, and
// RunReplica requires the pool size to be set before it can register a booted
// replica. The revive must therefore restore the pool configuration before
// the same-tick reconcile heals the lost slot - otherwise the healed replica's
// registration fails, which stops it again and re-crashes the app.
func TestRunOnce_ReviveRestoresProxyPoolBeforeHeal(t *testing.T) {
	st := newFakeStore(
		map[string]*db.App{"app": {ID: 1, Slug: "app", Status: "crashed", Replicas: 1, MaxSessionsPerReplica: 7}},
		[]*db.Deployment{{ID: 7, BundleDir: "/bundles/v1"}},
	)
	st.replicas = map[int64][]*db.Replica{
		1: {{AppID: 1, Index: 0, Status: db.ReplicaStatusLost, Tier: "remote", WorkerID: "node-a"}},
	}
	prx := newFakeProxy()
	var sizeAtDeploy []int
	w := newTestWatcher(Config{RestartMaxAttempts: 5}, &fakeManager{}, prx, st,
		func(slug, dir string, idx int) (*deploy.Result, error) {
			prx.mu.Lock()
			sizeAtDeploy = append(sizeAtDeploy, prx.poolSizes[slug])
			prx.mu.Unlock()
			return &deploy.Result{Index: idx, PID: 500, Port: 9000}, nil
		})
	w.EnableLostReplicaHealing(func(tier string) bool { return true })

	w.runOnce()

	if len(sizeAtDeploy) != 1 || sizeAtDeploy[0] != 1 {
		t.Fatalf("pool size seen by the healing deploy = %v, want [1] (set before the boot)", sizeAtDeploy)
	}
	prx.mu.Lock()
	appID, cap := prx.poolAppIDs["app"], prx.poolCaps["app"]
	prx.mu.Unlock()
	if appID != 1 {
		t.Errorf("pool app ID = %d, want 1", appID)
	}
	if cap != 7 {
		t.Errorf("pool cap = %d, want 7 (the app's max_sessions_per_replica)", cap)
	}
}

// Terminalization's mgr.Stop is best-effort: a sibling replica whose stop
// failed stays live in the process manager, keeps its running row (the
// normalization skips live runtimes), and lost its route when the pool was
// deregistered. Nothing else in a single-node deployment re-creates that
// route, so the revive must re-register every replica the manager still runs.
func TestRunOnce_ReviveReregistersLiveSiblingRoute(t *testing.T) {
	st := newFakeStore(
		map[string]*db.App{"app": {ID: 1, Slug: "app", Status: "crashed", Replicas: 2}},
		[]*db.Deployment{{ID: 7, BundleDir: "/bundles/v1"}},
	)
	st.replicas = map[int64][]*db.Replica{
		1: {
			{AppID: 1, Index: 0, Status: db.ReplicaStatusRunning, Tier: "local"},
			{AppID: 1, Index: 1, Status: db.ReplicaStatusLost, Tier: "remote", WorkerID: "node-a"},
		},
	}
	mgr := &fakeManager{entries: []*process.ProcessInfo{
		{Slug: "app", Index: 0, Status: process.StatusRunning, Port: 30123, Tier: "local", DeploymentID: 7},
		{Slug: "app", Index: 4, Status: process.StatusRunning, Port: 30999},   // out of pool
		{Slug: "other", Index: 0, Status: process.StatusRunning, Port: 31000}, // different app
	}}
	prx := newFakeProxy()
	var calls []int
	w := newTestWatcher(Config{RestartMaxAttempts: 5}, mgr, prx, st, healingDeploy(&calls))
	w.EnableLostReplicaHealing(func(tier string) bool { return true })

	w.runOnce()

	prx.mu.Lock()
	registered := append([]registeredBackend(nil), prx.registered...)
	prx.mu.Unlock()
	if len(registered) != 1 {
		t.Fatalf("registered backends = %v, want exactly the live sibling slot 0", registered)
	}
	got := registered[0]
	if got.slug != "app" || got.index != 0 {
		t.Errorf("registered %s#%d, want app#0", got.slug, got.index)
	}
	if got.target != "http://127.0.0.1:30123" {
		t.Errorf("registered target = %q, want http://127.0.0.1:30123", got.target)
	}
	if got.appID != 1 {
		t.Errorf("registered app ID = %d, want 1", got.appID)
	}
	if len(calls) != 1 || calls[0] != 1 {
		t.Fatalf("deploys = %v, want only the lost slot 1 re-placed", calls)
	}
}

// The crashed-apps listing runs before the per-app lease, so every revive gate
// must judge the row as it is under the lease, not the listing snapshot. Here
// the app was switched to per_session between the two: the stale snapshot says
// multiplex and would pass the elastic gate, booting a durable replica the
// elastic app must not have. ReviveCrashedApp's CAS guards only the status.
func TestRunOnce_ReviveChecksFreshRowNotListingSnapshot(t *testing.T) {
	st := newFakeStore(
		map[string]*db.App{"app": {ID: 1, Slug: "app", Status: "crashed", Replicas: 1, WorkerIsolation: "per_session"}},
		[]*db.Deployment{{ID: 7, BundleDir: "/bundles/v1"}},
	)
	st.listCrashedStale = []*db.App{{ID: 1, Slug: "app", Status: "crashed", Replicas: 1, WorkerIsolation: ""}}
	st.replicas = map[int64][]*db.Replica{
		1: {{AppID: 1, Index: 0, Status: db.ReplicaStatusLost, Tier: "remote", WorkerID: "node-a"}},
	}
	var calls []int
	w := newTestWatcher(Config{RestartMaxAttempts: 5}, &fakeManager{}, newFakeProxy(), st, healingDeploy(&calls))
	w.EnableLostReplicaHealing(func(tier string) bool { return true })

	w.runOnce()

	if st.appStatus["app"] != "crashed" {
		t.Errorf("app status = %q, want crashed (elastic app must not be revived from a stale row)", st.appStatus["app"])
	}
	if len(calls) != 0 {
		t.Fatalf("deploys = %v, want none for an elastic app", calls)
	}
}

// A revive is funded by one worker-death event: the heal consumes the lost
// rows, and a further revive needs another death. When the heal's failure
// cannot even be persisted (the rows stay lost) and the app re-terminalizes,
// the already-forgiven episode must not fund another revive - otherwise the
// app cycles crashed -> degraded -> crashed with a fresh budget each time,
// exactly the unbounded retry restartSlotLocked's once-per-episode forgiveness
// exists to prevent.
func TestRunOnce_ReviveFundedOncePerLossEpisode(t *testing.T) {
	st := newFakeStore(
		map[string]*db.App{"app": {ID: 1, Slug: "app", Status: "crashed", Replicas: 1}},
		[]*db.Deployment{{ID: 7, BundleDir: "/bundles/v1"}},
	)
	st.replicas = map[int64][]*db.Replica{
		1: {{AppID: 1, Index: 0, Status: db.ReplicaStatusLost, Tier: "remote", WorkerID: "node-a"}},
	}
	var calls []int
	w := newTestWatcher(Config{RestartMaxAttempts: 5}, &fakeManager{}, newFakeProxy(), st, healingDeploy(&calls))
	w.EnableLostReplicaHealing(func(tier string) bool { return true })

	k := replicaKey{"app", 0}
	w.mu.Lock()
	w.lostForgiven[k] = true // a prior revive already forgave this loss episode
	w.attempts[k] = 5        // and the budget it granted was spent again
	w.mu.Unlock()

	w.runOnce()

	if st.appStatus["app"] != "crashed" {
		t.Errorf("app status = %q, want crashed (episode already had its forgiveness)", st.appStatus["app"])
	}
	if len(calls) != 0 {
		t.Fatalf("deploys = %v, want none while the revive is unfunded", calls)
	}
	w.mu.Lock()
	spent := w.attempts[k]
	w.mu.Unlock()
	if spent != 5 {
		t.Errorf("attempts = %d, want 5 (no budget re-grant without a new loss episode)", spent)
	}

	// Positive control: a new loss episode (forgiveness cleared when the row
	// last left the lost state) funds the revive again.
	w.mu.Lock()
	delete(w.lostForgiven, k)
	w.mu.Unlock()

	w.runOnce()

	if st.appStatus["app"] == "crashed" {
		t.Error("app was not revived for a fresh loss episode")
	}
	if len(calls) != 1 || calls[0] != 0 {
		t.Fatalf("deploys = %v, want the lost slot re-placed once funded", calls)
	}
}

// A revive funded by one slot's fresh loss episode must not re-grant budget to
// a sibling slot whose episode was already forgiven: that slot's re-spent
// budget is the recorded verdict against it and survives the sibling's revive.
func TestRunOnce_RevivePreservesForgivenSlotBudget(t *testing.T) {
	st := newFakeStore(
		map[string]*db.App{"app": {ID: 1, Slug: "app", Status: "crashed", Replicas: 2}},
		[]*db.Deployment{{ID: 7, BundleDir: "/bundles/v1"}},
	)
	st.replicas = map[int64][]*db.Replica{
		1: {
			{AppID: 1, Index: 0, Status: db.ReplicaStatusLost, Tier: "remote", WorkerID: "node-a"},
			{AppID: 1, Index: 1, Status: db.ReplicaStatusLost, Tier: "remote", WorkerID: "node-a"},
		},
	}
	var calls []int
	w := newTestWatcher(Config{RestartMaxAttempts: 5}, &fakeManager{}, newFakeProxy(), st, healingDeploy(&calls))
	w.EnableLostReplicaHealing(func(tier string) bool { return true })

	forgiven := replicaKey{"app", 0}
	fresh := replicaKey{"app", 1}
	w.mu.Lock()
	w.lostForgiven[forgiven] = true
	w.attempts[forgiven] = 5
	w.attempts[fresh] = 5
	w.mu.Unlock()

	w.runOnce()

	if st.appStatus["app"] == "crashed" {
		t.Error("app was not revived despite slot 1's fresh loss episode")
	}
	if len(calls) != 1 || calls[0] != 1 {
		t.Fatalf("deploys = %v, want only slot 1 (slot 0's episode keeps its spent budget)", calls)
	}
	w.mu.Lock()
	forgivenSpent := w.attempts[forgiven]
	w.mu.Unlock()
	if forgivenSpent != 5 {
		t.Errorf("forgiven slot attempts = %d, want 5 (no re-grant from the sibling's revive)", forgivenSpent)
	}
}
