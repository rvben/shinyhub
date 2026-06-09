package lifecycle

import (
	"sync"
	"sync/atomic"
	"testing"

	"github.com/rvben/shinyhub/internal/db"
	"github.com/rvben/shinyhub/internal/deploy"
)

// newTestWatcherWithOwner builds a Watcher with a configurable isOwner predicate.
func newTestWatcherWithOwner(cfg Config, mgr *fakeManager, prx *fakeProxy, st *fakeStore,
	isOwner func() bool,
	deployFn func(slug, bundleDir string, index int) (*deploy.Result, error)) *Watcher {
	w := newTestWatcher(cfg, mgr, prx, st, deployFn)
	w.isOwner = isOwner
	return w
}

// TestWakeTrigger_StandbyBeginsWakeButDoesNotDrive verifies that when isOwner()
// returns false, WakeTrigger issues the BeginWake CAS (hibernated->waking) but
// does NOT spawn a goroutine to deploy replicas.
func TestWakeTrigger_StandbyBeginsWakeButDoesNotDrive(t *testing.T) {
	st := newFakeStore(
		map[string]*db.App{"app": {ID: 1, Slug: "app", Status: "hibernated", Replicas: 1}},
		[]*db.Deployment{{ID: 10, BundleDir: "/bundles/v1"}},
	)
	var deployCount int32
	w := newTestWatcherWithOwner(Config{RestartMaxAttempts: 5}, &fakeManager{}, newFakeProxy(), st,
		func() bool { return false }, // standby
		func(slug, bundleDir string, idx int) (*deploy.Result, error) {
			atomic.AddInt32(&deployCount, 1)
			return &deploy.Result{Index: idx, PID: 99, Port: 20099}, nil
		})

	w.WakeTrigger("app")

	// DB state must be "waking": BeginWake was issued.
	st.mu.Lock()
	status := st.apps["app"].Status
	st.mu.Unlock()
	if status != "waking" {
		t.Errorf("standby WakeTrigger: expected status=waking, got %q", status)
	}

	// No deploy must have been attempted.
	if n := atomic.LoadInt32(&deployCount); n != 0 {
		t.Errorf("standby WakeTrigger: expected 0 deploy calls, got %d", n)
	}
}

// TestWakeTrigger_ActiveDrivesInline verifies that when isOwner() returns true,
// WakeTrigger issues BeginWake AND drives the app to running (single-node path).
func TestWakeTrigger_ActiveDrivesInline(t *testing.T) {
	st := newFakeStore(
		map[string]*db.App{"app": {ID: 1, Slug: "app", Status: "hibernated", Replicas: 1}},
		[]*db.Deployment{{ID: 10, BundleDir: "/bundles/v1"}},
	)
	var deployCount int32
	w := newTestWatcherWithOwner(Config{RestartMaxAttempts: 5}, &fakeManager{}, newFakeProxy(), st,
		func() bool { return true }, // active owner
		func(slug, bundleDir string, idx int) (*deploy.Result, error) {
			atomic.AddInt32(&deployCount, 1)
			return &deploy.Result{Index: idx, PID: 33, Port: 20033}, nil
		})

	w.WakeTrigger("app")
	waitNotWaking(t, st, "app")

	// Replica deployed exactly once.
	if n := atomic.LoadInt32(&deployCount); n != 1 {
		t.Errorf("active WakeTrigger: expected 1 deploy call, got %d", n)
	}

	// App reaches running.
	st.mu.Lock()
	status := st.apps["app"].Status
	st.mu.Unlock()
	if status != "running" {
		t.Errorf("active WakeTrigger: expected status=running, got %q", status)
	}
}

// TestReconciler_DrivesWakingAppsInClusteredMode verifies that when Clustered=true
// and an app is in 'waking' status (CAS'd by a standby), a runOnce tick drives
// it to running via driveWakingApp.
func TestReconciler_DrivesWakingAppsInClusteredMode(t *testing.T) {
	st := newFakeStore(
		// App is already in 'waking' state (as if a standby triggered BeginWake).
		map[string]*db.App{"app": {ID: 1, Slug: "app", Status: "waking", Replicas: 1}},
		[]*db.Deployment{{ID: 10, BundleDir: "/bundles/v1"}},
	)
	var deployCount int32
	w := newTestWatcher(Config{Clustered: true, RestartMaxAttempts: 5}, &fakeManager{}, newFakeProxy(), st,
		func(slug, bundleDir string, idx int) (*deploy.Result, error) {
			atomic.AddInt32(&deployCount, 1)
			return &deploy.Result{Index: idx, PID: 77, Port: 20077}, nil
		})

	w.runOnce()
	waitNotWaking(t, st, "app")

	if n := atomic.LoadInt32(&deployCount); n != 1 {
		t.Errorf("clustered reconciler: expected 1 deploy call, got %d", n)
	}

	st.mu.Lock()
	status := st.apps["app"].Status
	st.mu.Unlock()
	if status != "running" {
		t.Errorf("clustered reconciler: expected status=running after drive, got %q", status)
	}
}

// TestReconciler_SkipsWakingAppsInSingleNodeMode verifies that when Clustered=false
// (single-node), runOnce does NOT run the waking-apps reconciler. The inline
// trigger drive handles single-node wakes; an orphaned 'waking' row (which
// cannot occur in normal single-node operation) would be left alone.
func TestReconciler_SkipsWakingAppsInSingleNodeMode(t *testing.T) {
	st := newFakeStore(
		// Simulate a hypothetical 'waking' app on a single-node instance.
		map[string]*db.App{"app": {ID: 1, Slug: "app", Status: "waking", Replicas: 1}},
		[]*db.Deployment{{ID: 10, BundleDir: "/bundles/v1"}},
	)
	var deployCount int32
	w := newTestWatcher(Config{Clustered: false, RestartMaxAttempts: 5}, &fakeManager{}, newFakeProxy(), st,
		func(slug, bundleDir string, idx int) (*deploy.Result, error) {
			atomic.AddInt32(&deployCount, 1)
			return &deploy.Result{Index: idx, PID: 11, Port: 20011}, nil
		})

	w.runOnce()

	// No deploy must fire from the reconciler on single-node.
	if n := atomic.LoadInt32(&deployCount); n != 0 {
		t.Errorf("single-node: reconciler must not drive waking apps, got %d deploy calls", n)
	}
}

// TestDriveWakingApp_DeploymentIDSet verifies that a woken replica's UpsertReplica
// call carries the deployment ID from the latest deployment row.
func TestDriveWakingApp_DeploymentIDSet(t *testing.T) {
	deploymentID := int64(42)
	st := newFakeStore(
		map[string]*db.App{"app": {ID: 1, Slug: "app", Status: "hibernated", Replicas: 1}},
		[]*db.Deployment{{ID: deploymentID, BundleDir: "/bundles/v1", Version: "v1"}},
	)
	w := newTestWatcher(Config{RestartMaxAttempts: 5}, &fakeManager{}, newFakeProxy(), st,
		func(slug, bundleDir string, idx int) (*deploy.Result, error) {
			return &deploy.Result{Index: idx, PID: 55, Port: 20055}, nil
		})

	w.WakeTrigger("app")
	waitNotWaking(t, st, "app")

	st.mu.Lock()
	defer st.mu.Unlock()
	if len(st.upsertedReplicas) == 0 {
		t.Fatal("expected UpsertReplica call after wake")
	}
	ur := st.upsertedReplicas[len(st.upsertedReplicas)-1]
	if ur.DeploymentID == nil {
		t.Fatal("DeploymentID must be set on wake UpsertReplica, got nil")
	}
	if *ur.DeploymentID != deploymentID {
		t.Errorf("DeploymentID = %d, want %d", *ur.DeploymentID, deploymentID)
	}
}

// TestDriveWakingApp_DoubleDriveDedup verifies that two concurrent driveWakingApp
// calls for the same slug deploy the app's replicas only once. The in-memory
// driving guard prevents the second concurrent call from spawning a duplicate
// goroutine.
func TestDriveWakingApp_DoubleDriveDedup(t *testing.T) {
	st := &fakeStore{
		apps:      map[string]*db.App{"app": {ID: 1, Slug: "app", Status: "waking", Replicas: 1}},
		appStatus: map[string]string{"app": "waking"},
	}
	st.deployments = []*db.Deployment{{ID: 10, BundleDir: "/bundles/v1"}}

	var deployCount int32
	// deployFn blocks briefly so the second concurrent call arrives while the
	// first is still running, exercising the dedup guard.
	var once sync.Once
	canProceed := make(chan struct{})
	w := newTestWatcher(Config{RestartMaxAttempts: 5}, &fakeManager{}, newFakeProxy(), st,
		func(slug, bundleDir string, idx int) (*deploy.Result, error) {
			once.Do(func() { close(canProceed) })
			atomic.AddInt32(&deployCount, 1)
			return &deploy.Result{Index: idx, PID: 66, Port: 20066}, nil
		})

	// First call: wins the driving guard.
	w.driveWakingApp("app")
	// Wait until the first call's goroutine has started deploying.
	<-canProceed
	// Second call: should be deduplicated by the in-memory guard.
	w.driveWakingApp("app")

	waitNotWaking(t, st, "app")

	if n := atomic.LoadInt32(&deployCount); n != 1 {
		t.Errorf("double-drive: expected exactly 1 deploy call (dedup guard), got %d", n)
	}
}

// TestWakeTrigger_SingleNodeIsOwnerByDefault verifies that when isOwner is nil
// (not wired, as in single-node setups), WakeTrigger treats the instance as
// always-owner and drives inline, keeping single-node behaviour byte-for-byte
// unchanged.
func TestWakeTrigger_SingleNodeIsOwnerByDefault(t *testing.T) {
	st := newFakeStore(
		map[string]*db.App{"app": {ID: 1, Slug: "app", Status: "hibernated", Replicas: 1}},
		[]*db.Deployment{{ID: 10, BundleDir: "/bundles/v1"}},
	)
	var deployCount int32
	// isOwner left nil - the default for single-node (not calling SetIsOwner).
	w := newTestWatcher(Config{RestartMaxAttempts: 5}, &fakeManager{}, newFakeProxy(), st,
		func(slug, bundleDir string, idx int) (*deploy.Result, error) {
			atomic.AddInt32(&deployCount, 1)
			return &deploy.Result{Index: idx, PID: 11, Port: 20011}, nil
		})

	w.WakeTrigger("app")
	waitNotWaking(t, st, "app")

	if n := atomic.LoadInt32(&deployCount); n != 1 {
		t.Errorf("single-node (nil isOwner): expected 1 deploy call, got %d", n)
	}
	st.mu.Lock()
	status := st.apps["app"].Status
	st.mu.Unlock()
	if status != "running" {
		t.Errorf("single-node (nil isOwner): expected status=running, got %q", status)
	}
}
