package lifecycle

import (
	"fmt"
	"sync/atomic"
	"testing"

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

	w.restartSlot(st.apps["app"], 0)

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

	w.reconcileAppStatus(st.apps["app"])

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

	w.reconcileAppStatus(st.apps["app"])

	if st.appStatus["app"] != "running" {
		t.Errorf("status = %q, want running", st.appStatus["app"])
	}
}

func TestReconcileAppStatus_IgnoresHibernatedAndDeploying(t *testing.T) {
	for _, status := range []string{"hibernated", "deploying", "stopped"} {
		st := newFakeStore(map[string]*db.App{"app": {ID: 1, Slug: "app", Status: status, Replicas: 2}}, nil)
		st.replicas = map[int64][]*db.Replica{1: {{AppID: 1, Index: 0, Status: "running"}}}
		w := newTestWatcher(Config{}, &fakeManager{}, newFakeProxy(), st, nil)

		w.reconcileAppStatus(st.apps["app"])

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

	w.restartSlot(st.apps["app"], 0)

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
	w.reconcileAppStatus(st.apps["app"])
	if st.appStatus["app"] != "running" {
		t.Errorf("tick 1: status = %q, want running (warm victims are healthy stopped capacity)", st.appStatus["app"])
	}
	w.reconcileAppStatus(st.apps["app"])
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

	w.reconcileAppStatus(st.apps["app"])

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

	w.reconcileReplicas(map[replicaKey]bool{})

	if n := atomic.LoadInt32(&deployCount); n != 0 {
		t.Errorf("expected no restart for warm rows, got %d deploy calls", n)
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
