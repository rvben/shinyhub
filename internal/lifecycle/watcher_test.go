package lifecycle

import (
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rvben/shinyhub/internal/db"
	"github.com/rvben/shinyhub/internal/deploy"
	"github.com/rvben/shinyhub/internal/process"
)

// --- test fakes ---

type fakeManager struct {
	mu      sync.Mutex
	entries []*process.ProcessInfo
	stopped []string
	stopErr error // when set, Stop records the slug then returns this error
}

func (f *fakeManager) All() []*process.ProcessInfo {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]*process.ProcessInfo, len(f.entries))
	copy(out, f.entries)
	return out
}

func (f *fakeManager) Stop(slug string) error {
	f.mu.Lock()
	f.stopped = append(f.stopped, slug)
	err := f.stopErr
	f.mu.Unlock()
	return err
}

type fakeProxy struct {
	mu              sync.Mutex
	seen            map[string]time.Time
	deregistered    []string
	hibernated      []string
	hibernateAlways bool // if true, BeginHibernate ignores `since` and always returns true
	hibernateNever  bool // if true, BeginHibernate always returns false (simulates activeConns>0)
	poolSizes       map[string]int
	poolCaps        map[string]int
}

func newFakeProxy() *fakeProxy {
	return &fakeProxy{
		seen:      make(map[string]time.Time),
		poolSizes: make(map[string]int),
		poolCaps:  make(map[string]int),
	}
}

func (f *fakeProxy) LastSeen(slug string) time.Time {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.seen[slug]
}
func (f *fakeProxy) Deregister(slug string) {
	f.mu.Lock()
	f.deregistered = append(f.deregistered, slug)
	f.mu.Unlock()
}
func (f *fakeProxy) BeginHibernate(slug string, since time.Time) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.hibernateNever {
		return false // simulates activeConns>0 or a raced-in request
	}
	if !f.hibernateAlways {
		if last := f.seen[slug]; last.After(since) {
			return false
		}
	}
	f.hibernated = append(f.hibernated, slug)
	delete(f.seen, slug)
	return true
}
func (f *fakeProxy) SetPoolSize(slug string, size int) {
	f.mu.Lock()
	f.poolSizes[slug] = size
	f.mu.Unlock()
}
func (f *fakeProxy) SetPoolCap(slug string, max int) {
	f.mu.Lock()
	f.poolCaps[slug] = max
	f.mu.Unlock()
}
func (f *fakeProxy) SetPoolAppID(_ string, _ int64)          {}
func (f *fakeProxy) SetPoolIdentityHeaders(_ string, _ bool) {}

type fakeStore struct {
	mu               sync.Mutex
	apps             map[string]*db.App
	deployments      []*db.Deployment
	statusUpdates    []db.UpdateAppStatusParams
	appStatus        map[string]string
	upsertedReplicas []db.UpsertReplicaParams
	replicas         map[int64][]*db.Replica
	upsertErr        error // when set, UpsertReplica records the call then returns this
	updateStatusErr  error // when set, UpdateAppStatus records the call then returns this
	reapCount        int   // incremented by each ReapStaleReplicaSessions call

	// hibernateAppCalls tracks every HibernateApp call (slug).
	hibernateAppCalls []string

	// fleetActive and fleetIdleSinceSec are returned by AppFleetLoad.
	// fleetActive is the sum of other-instance active counts (0 = fleet idle).
	// fleetIdleSinceSec is the seconds since the most recent fleet activity on
	// the DB clock; set to db.NoFleetActivity (math.MaxInt64) to simulate "no
	// peers". Values >= timeout.Seconds() allow hibernation; values < timeout.Seconds()
	// block it.
	fleetActive       int64
	fleetIdleSinceSec int64
}

func newFakeStore(apps map[string]*db.App, deployments []*db.Deployment) *fakeStore {
	statuses := make(map[string]string, len(apps))
	for slug, app := range apps {
		statuses[slug] = app.Status
	}
	return &fakeStore{
		apps:        apps,
		deployments: deployments,
		appStatus:   statuses,
	}
}

func (f *fakeStore) GetAppBySlug(slug string) (*db.App, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	app, ok := f.apps[slug]
	if !ok {
		// Mirror the real store so callers can errors.Is(err, db.ErrNotFound).
		return nil, db.ErrNotFound
	}
	return app, nil
}
func (f *fakeStore) UpdateAppStatus(p db.UpdateAppStatusParams) error {
	f.mu.Lock()
	f.statusUpdates = append(f.statusUpdates, p)
	if f.updateStatusErr != nil {
		err := f.updateStatusErr
		f.mu.Unlock()
		return err
	}
	if app, ok := f.apps[p.Slug]; ok {
		app.Status = p.Status
	}
	if f.appStatus == nil {
		f.appStatus = make(map[string]string)
	}
	f.appStatus[p.Slug] = p.Status
	f.mu.Unlock()
	return nil
}

// BeginWake mirrors the real store's CAS: hibernated -> waking, winner only.
func (f *fakeStore) BeginWake(slug string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	app, ok := f.apps[slug]
	if !ok || app.Status != "hibernated" {
		return false, nil
	}
	app.Status = "waking"
	if f.appStatus != nil {
		f.appStatus[slug] = "waking"
	}
	return true, nil
}

// AbortWake mirrors the real store's reverse CAS: waking -> hibernated (no-op
// otherwise).
func (f *fakeStore) AbortWake(slug string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if app, ok := f.apps[slug]; ok && app.Status == "waking" {
		app.Status = "hibernated"
		if f.appStatus != nil {
			f.appStatus[slug] = "hibernated"
		}
	}
	return nil
}

// FinishWake mirrors the real store's CAS: waking -> running, winner only (no-op
// + false if a concurrent change moved the app off "waking").
func (f *fakeStore) FinishWake(slug string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	app, ok := f.apps[slug]
	if !ok || app.Status != "waking" {
		return false, nil
	}
	app.Status = "running"
	if f.appStatus != nil {
		f.appStatus[slug] = "running"
	}
	return true, nil
}

func (f *fakeStore) ListDeployments(_ int64) ([]*db.Deployment, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.deployments, nil
}
func (f *fakeStore) UpsertReplica(p db.UpsertReplicaParams) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.upsertedReplicas = append(f.upsertedReplicas, p)
	if f.upsertErr != nil {
		return f.upsertErr
	}
	// Write through to the replica table so the status authority observes the
	// new state, matching the real store where UpsertReplica is durable.
	if f.replicas == nil {
		f.replicas = make(map[int64][]*db.Replica)
	}
	for _, r := range f.replicas[p.AppID] {
		if r.Index == p.Index {
			r.Status, r.PID, r.Port = p.Status, p.PID, p.Port
			r.Provider, r.Tier = p.Provider, p.Tier
			r.EndpointURL, r.WorkerID = p.EndpointURL, p.WorkerID
			r.AppVersion, r.DesiredState, r.DeploymentID = p.AppVersion, p.DesiredState, p.DeploymentID
			return nil
		}
	}
	f.replicas[p.AppID] = append(f.replicas[p.AppID], &db.Replica{
		AppID: p.AppID, Index: p.Index, PID: p.PID, Port: p.Port, Status: p.Status,
		Provider: p.Provider, Tier: p.Tier, EndpointURL: p.EndpointURL, WorkerID: p.WorkerID,
		AppVersion: p.AppVersion, DesiredState: p.DesiredState, DeploymentID: p.DeploymentID,
	})
	return nil
}
func (f *fakeStore) ListReconcilableApps() ([]*db.App, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []*db.App
	for _, app := range f.apps {
		if app.Status == "running" || app.Status == "degraded" {
			out = append(out, app)
		}
	}
	return out, nil
}

func (f *fakeStore) ListWakingApps() ([]*db.App, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []*db.App
	for _, app := range f.apps {
		if app.Status == "waking" {
			out = append(out, app)
		}
	}
	return out, nil
}
func (f *fakeStore) ListReplicas(appID int64) ([]*db.Replica, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.replicas[appID], nil
}

func (f *fakeStore) ReapStaleReplicaSessions(_ int64) error {
	f.mu.Lock()
	f.reapCount++
	f.mu.Unlock()
	return nil
}

// HibernateApp mirrors the real store's CAS: running -> hibernated, winner only.
func (f *fakeStore) HibernateApp(slug string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.hibernateAppCalls = append(f.hibernateAppCalls, slug)
	app, ok := f.apps[slug]
	if !ok || app.Status != "running" {
		return false, nil
	}
	app.Status = "hibernated"
	if f.appStatus != nil {
		f.appStatus[slug] = "hibernated"
	}
	return true, nil
}

// AppFleetLoad returns the configured fake fleet load values. The real signature
// includes a staleWindowSec and excludeInstanceID but the fake ignores them and
// returns the pre-configured values so tests can control fleet-idle outcomes.
// fleetIdleSinceSec represents seconds since the most recent fleet activity on
// the DB clock; use db.NoFleetActivity to simulate "no live peer data".
func (f *fakeStore) AppFleetLoad(_ int64, _ int64, _ string) ([]int64, int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.fleetActive == 0 {
		return []int64{}, f.fleetIdleSinceSec, nil
	}
	return []int64{f.fleetActive}, f.fleetIdleSinceSec, nil
}

// newTestWatcher builds a Watcher with fakes. Tests in the same package can
// call runOnce() directly without starting the background goroutine.
func newTestWatcher(cfg Config, mgr *fakeManager, prx *fakeProxy, st *fakeStore,
	deployFn func(slug, bundleDir string, index int) (*deploy.Result, error)) *Watcher {
	return &Watcher{
		cfg:       cfg,
		mgr:       mgr,
		prx:       prx,
		store:     st,
		deploy:    deployFn,
		attempts:  make(map[replicaKey]int),
		nextRetry: make(map[replicaKey]time.Time),
		driving:   make(map[string]bool),
	}
}

// --- watchdog tests ---

func TestWatchdog_RestartsOnCrash(t *testing.T) {
	mgr := &fakeManager{entries: []*process.ProcessInfo{
		{Slug: "myapp", Index: 0, Status: process.StatusCrashed},
	}}
	st := newFakeStore(
		map[string]*db.App{"myapp": {ID: 1, Slug: "myapp", Status: "running", Replicas: 1}},
		[]*db.Deployment{{BundleDir: "/bundles/v1"}},
	)
	var deployed []string
	var mu sync.Mutex
	w := newTestWatcher(Config{RestartMaxAttempts: 5}, mgr, newFakeProxy(), st,
		func(slug, bundleDir string, idx int) (*deploy.Result, error) {
			mu.Lock()
			deployed = append(deployed, slug)
			mu.Unlock()
			return &deploy.Result{Index: idx, PID: 11, Port: 20011}, nil
		})

	w.runOnce()

	mu.Lock()
	defer mu.Unlock()
	if len(deployed) != 1 || deployed[0] != "myapp" {
		t.Errorf("expected deployFn called once for myapp, got %v", deployed)
	}
}

// TestWatchdog_ReconcilesCrashedReplicaSlot covers the partial-deploy gap: an
// index that never booted is persisted as "crashed" but the process manager
// has no entry for it (StopReplica was called on boot failure). The watchdog
// must still drive it back up from the persisted row, not leave the app
// permanently under-replicated.
func TestWatchdog_ReconcilesCrashedReplicaSlot(t *testing.T) {
	// Manager only knows about the healthy replica 0.
	mgr := &fakeManager{entries: []*process.ProcessInfo{
		{Slug: "myapp", Index: 0, Status: process.StatusRunning},
	}}
	st := newFakeStore(
		map[string]*db.App{"myapp": {ID: 1, Slug: "myapp", Status: "running", Replicas: 2}},
		[]*db.Deployment{{BundleDir: "/bundles/v1"}},
	)
	pid0, port0 := 10, 20010
	st.replicas = map[int64][]*db.Replica{
		1: {
			{AppID: 1, Index: 0, PID: &pid0, Port: &port0, Status: "running"},
			{AppID: 1, Index: 1, Status: "crashed"},
		},
	}
	var deployedIdx []int
	var mu sync.Mutex
	w := newTestWatcher(Config{RestartMaxAttempts: 5}, mgr, newFakeProxy(), st,
		func(slug, bundleDir string, idx int) (*deploy.Result, error) {
			mu.Lock()
			deployedIdx = append(deployedIdx, idx)
			mu.Unlock()
			return &deploy.Result{Index: idx, PID: 11, Port: 20011}, nil
		})

	w.runOnce()

	mu.Lock()
	defer mu.Unlock()
	if len(deployedIdx) != 1 || deployedIdx[0] != 1 {
		t.Fatalf("expected deployFn called once for crashed replica index 1, got %v", deployedIdx)
	}
	st.mu.Lock()
	defer st.mu.Unlock()
	var sawRunning bool
	for _, up := range st.upsertedReplicas {
		if up.Index == 1 && up.Status == "running" {
			sawRunning = true
		}
	}
	if !sawRunning {
		t.Errorf("expected replica index 1 upserted as running, got %+v", st.upsertedReplicas)
	}
}

// TestWatchdog_IgnoresCrashedSlotAboveReplicaCount ensures a stale crashed row
// left by a replica shrink (index >= desired Replicas) is not resurrected.
func TestWatchdog_IgnoresCrashedSlotAboveReplicaCount(t *testing.T) {
	mgr := &fakeManager{entries: []*process.ProcessInfo{
		{Slug: "myapp", Index: 0, Status: process.StatusRunning},
	}}
	st := newFakeStore(
		map[string]*db.App{"myapp": {ID: 1, Slug: "myapp", Status: "running", Replicas: 1}},
		[]*db.Deployment{{BundleDir: "/bundles/v1"}},
	)
	st.replicas = map[int64][]*db.Replica{
		1: {{AppID: 1, Index: 1, Status: "crashed"}}, // idx 1 >= Replicas 1
	}
	var calls int32
	w := newTestWatcher(Config{RestartMaxAttempts: 5}, mgr, newFakeProxy(), st,
		func(slug, bundleDir string, idx int) (*deploy.Result, error) {
			atomic.AddInt32(&calls, 1)
			return &deploy.Result{Index: idx, PID: 1, Port: 1}, nil
		})

	w.runOnce()

	if n := atomic.LoadInt32(&calls); n != 0 {
		t.Errorf("expected no restart for stale crashed slot above replica count, got %d calls", n)
	}
}

func TestWatchdog_ExponentialBackoff(t *testing.T) {
	mgr := &fakeManager{entries: []*process.ProcessInfo{
		{Slug: "app", Index: 0, Status: process.StatusCrashed},
	}}
	st := newFakeStore(
		map[string]*db.App{"app": {ID: 1, Slug: "app", Status: "running", Replicas: 1}},
		[]*db.Deployment{{BundleDir: "/bundles/v1"}},
	)
	var deployCount int32
	w := newTestWatcher(Config{RestartMaxAttempts: 5}, mgr, newFakeProxy(), st,
		func(slug, bundleDir string, idx int) (*deploy.Result, error) {
			atomic.AddInt32(&deployCount, 1)
			return nil, fmt.Errorf("still crashed")
		})

	// First tick: attempt 1, deploys immediately (no nextRetry set yet).
	w.runOnce()
	if got := atomic.LoadInt32(&deployCount); got != 1 {
		t.Fatalf("expected 1 deploy after first tick, got %d", got)
	}

	// Second tick immediately: within backoff window, no deploy.
	w.runOnce()
	if got := atomic.LoadInt32(&deployCount); got != 1 {
		t.Errorf("expected still 1 deploy (in backoff), got %d", got)
	}

	// Advance nextRetry into the past so the next tick deploys.
	w.mu.Lock()
	w.nextRetry[replicaKey{"app", 0}] = time.Now().Add(-time.Second)
	w.mu.Unlock()

	w.runOnce()
	if got := atomic.LoadInt32(&deployCount); got != 2 {
		t.Errorf("expected 2 deploys after backoff elapsed, got %d", got)
	}
}

// TestWatchdog_GivesUpAfterMaxAttempts proves the broken-bundle path is bounded
// and non-zero-cost: each tick that clears the backoff window spends one attempt
// until RestartMaxAttempts is reached, after which no further deploy is entered,
// and the app reflects degraded throughout (any down slot => degraded).
func TestWatchdog_GivesUpAfterMaxAttempts(t *testing.T) {
	mgr := &fakeManager{entries: []*process.ProcessInfo{
		{Slug: "app", Index: 0, Status: process.StatusCrashed},
	}}
	st := newFakeStore(
		map[string]*db.App{"app": {ID: 1, Slug: "app", Status: "running", Replicas: 1}},
		[]*db.Deployment{{BundleDir: "/bundles/v1"}},
	)
	st.replicas = map[int64][]*db.Replica{1: {{AppID: 1, Index: 0, Status: "crashed"}}}
	var deployCount int32
	w := newTestWatcher(Config{RestartMaxAttempts: 3}, mgr, newFakeProxy(), st,
		func(slug, bundleDir string, idx int) (*deploy.Result, error) {
			atomic.AddInt32(&deployCount, 1)
			return nil, fmt.Errorf("always fails")
		})

	// Clear the backoff window each round so the next tick is allowed to attempt
	// a deploy; run well past the budget to prove deploys stop climbing.
	for i := 0; i < w.cfg.RestartMaxAttempts+3; i++ {
		w.mu.Lock()
		w.nextRetry[replicaKey{"app", 0}] = time.Now().Add(-time.Second)
		w.mu.Unlock()
		w.runOnce()
	}

	if got := atomic.LoadInt32(&deployCount); got != int32(w.cfg.RestartMaxAttempts) {
		t.Errorf("expected deploy attempts capped at %d, got %d", w.cfg.RestartMaxAttempts, got)
	}
	if st.appStatus["app"] != "degraded" {
		t.Errorf("expected status=degraded, got %q", st.appStatus["app"])
	}
}

func TestWatchdog_ResetsAttemptsOnSuccess(t *testing.T) {
	mgr := &fakeManager{entries: []*process.ProcessInfo{
		{Slug: "app", Index: 0, Status: process.StatusCrashed},
	}}
	st := newFakeStore(
		map[string]*db.App{"app": {ID: 1, Slug: "app", Status: "running", Replicas: 1}},
		[]*db.Deployment{{BundleDir: "/bundles/v1"}},
	)
	st.replicas = map[int64][]*db.Replica{1: {{AppID: 1, Index: 0, Status: "crashed"}}}
	var callCount int32
	w := newTestWatcher(Config{RestartMaxAttempts: 5}, mgr, newFakeProxy(), st,
		func(slug, bundleDir string, idx int) (*deploy.Result, error) {
			n := atomic.AddInt32(&callCount, 1)
			if n < 2 {
				return nil, fmt.Errorf("fail once")
			}
			return &deploy.Result{Index: idx, PID: 22, Port: 20022}, nil
		})

	// First tick: fail → attempts=1.
	w.runOnce()
	w.mu.Lock()
	if w.attempts[replicaKey{"app", 0}] != 1 {
		w.mu.Unlock()
		t.Fatalf("expected attempts=1 after failure, got %d", w.attempts[replicaKey{"app", 0}])
	}
	w.nextRetry[replicaKey{"app", 0}] = time.Now().Add(-time.Second)
	w.mu.Unlock()

	// Second tick: succeed → attempts key deleted (zero value).
	w.runOnce()
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.attempts[replicaKey{"app", 0}] != 0 {
		t.Errorf("expected attempts reset to 0 after success, got %d", w.attempts[replicaKey{"app", 0}])
	}
	if len(st.statusUpdates) == 0 {
		t.Fatal("expected running status update after successful restart")
	}
	last := st.statusUpdates[len(st.statusUpdates)-1]
	if last.Status != "running" {
		t.Fatalf("unexpected running update: %+v", last)
	}
	// Verify UpsertReplica was called with running status and correct pid/port.
	st.mu.Lock()
	defer st.mu.Unlock()
	if len(st.upsertedReplicas) == 0 {
		t.Fatal("expected UpsertReplica call after successful restart")
	}
	ur := st.upsertedReplicas[len(st.upsertedReplicas)-1]
	if ur.Status != "running" || ur.PID == nil || *ur.PID != 22 || ur.Port == nil || *ur.Port != 20022 {
		t.Fatalf("unexpected UpsertReplica params: %+v", ur)
	}
}

// --- hibernation tests ---

func TestHibernation_StopsIdleApp(t *testing.T) {
	mgr := &fakeManager{entries: []*process.ProcessInfo{
		{Slug: "app", Index: 0, Status: process.StatusRunning},
	}}
	prx := newFakeProxy()
	prx.seen["app"] = time.Now().Add(-2 * time.Hour) // idle for 2h

	st := newFakeStore(
		map[string]*db.App{"app": {
			ID:        1,
			Slug:      "app",
			Status:    "running",
			Replicas:  1,
			UpdatedAt: time.Now().Add(-3 * time.Hour),
		}},
		nil,
	)
	w := newTestWatcher(Config{HibernateTimeout: 30 * time.Minute, RestartMaxAttempts: 5},
		mgr, prx, st, func(slug, dir string, idx int) (*deploy.Result, error) { return &deploy.Result{}, nil })

	w.runOnce()

	if len(mgr.stopped) == 0 || mgr.stopped[0] != "app" {
		t.Errorf("expected manager.Stop('app'), got %v", mgr.stopped)
	}
	if len(prx.hibernated) == 0 || prx.hibernated[0] != "app" {
		t.Errorf("expected proxy.BeginHibernate('app'), got %v", prx.hibernated)
	}
	if len(st.statusUpdates) == 0 || st.statusUpdates[len(st.statusUpdates)-1].Status != "hibernated" {
		t.Errorf("expected status=hibernated, got %v", st.statusUpdates)
	}
	// Verify UpsertReplica called with stopped status for each replica.
	st.mu.Lock()
	defer st.mu.Unlock()
	if len(st.upsertedReplicas) == 0 {
		t.Fatal("expected UpsertReplica call with stopped status on hibernation")
	}
	for _, ur := range st.upsertedReplicas {
		if ur.Status != "stopped" {
			t.Errorf("expected UpsertReplica status=stopped, got %q", ur.Status)
		}
	}
}

// TestHibernation_AbortsWhenActivityRacesIn covers the read-then-stop race
// where a request lands between LastSeen() and the hibernate action. The
// proxy's CAS-style BeginHibernate must reject the hibernate, leaving the
// app running and avoiding a torn-down replica that's actively serving.
func TestHibernation_AbortsWhenActivityRacesIn(t *testing.T) {
	mgr := &fakeManager{entries: []*process.ProcessInfo{
		{Slug: "app", Index: 0, Status: process.StatusRunning},
	}}
	prx := newFakeProxy()
	// Snapshot returned by LastSeen says "idle for 2h", so handleIdle proceeds
	// to BeginHibernate. Between the two calls we simulate a request landing:
	// the proxy bumps lastSeen to "now", and BeginHibernate must return false.
	snapshot := time.Now().Add(-2 * time.Hour)
	prx.seen["app"] = snapshot
	st := newFakeStore(
		map[string]*db.App{"app": {
			ID:        1,
			Slug:      "app",
			Status:    "running",
			Replicas:  1,
			UpdatedAt: time.Now().Add(-3 * time.Hour),
		}},
		nil,
	)
	w := newTestWatcher(Config{HibernateTimeout: 30 * time.Minute, RestartMaxAttempts: 5},
		mgr, prx, st, func(slug, dir string, idx int) (*deploy.Result, error) { return &deploy.Result{}, nil })

	// Race the activity in: bump lastSeen so BeginHibernate's CAS check fails.
	// (The fake's BeginHibernate compares against its `seen` map.)
	prx.mu.Lock()
	prx.seen["app"] = time.Now()
	prx.mu.Unlock()

	w.runOnce()

	if len(mgr.stopped) > 0 {
		t.Errorf("expected no manager.Stop after race-in activity, got %v", mgr.stopped)
	}
	if len(prx.hibernated) > 0 {
		t.Errorf("expected BeginHibernate to abort, got %v", prx.hibernated)
	}
	for _, s := range st.statusUpdates {
		if s.Status == "hibernated" {
			t.Errorf("expected no hibernated status update, got %v", st.statusUpdates)
		}
	}
}

func TestHibernation_RespectsPerAppDisable(t *testing.T) {
	mgr := &fakeManager{entries: []*process.ProcessInfo{
		{Slug: "app", Index: 0, Status: process.StatusRunning},
	}}
	prx := newFakeProxy()
	prx.seen["app"] = time.Now().Add(-2 * time.Hour)

	zero := 0
	st := newFakeStore(
		map[string]*db.App{"app": {
			ID:                      1,
			Slug:                    "app",
			Status:                  "running",
			Replicas:                1,
			HibernateTimeoutMinutes: &zero, // 0 = disabled for this app
			UpdatedAt:               time.Now().Add(-3 * time.Hour),
		}},
		nil,
	)
	w := newTestWatcher(Config{HibernateTimeout: 30 * time.Minute, RestartMaxAttempts: 5},
		mgr, prx, st, func(slug, dir string, idx int) (*deploy.Result, error) { return &deploy.Result{}, nil })

	w.runOnce()

	if len(mgr.stopped) > 0 {
		t.Errorf("expected no stop (per-app disabled), got %v", mgr.stopped)
	}
}

func TestHibernation_RespectsPerAppCustomTimeout(t *testing.T) {
	mgr := &fakeManager{entries: []*process.ProcessInfo{
		{Slug: "app", Index: 0, Status: process.StatusRunning},
	}}
	prx := newFakeProxy()
	prx.seen["app"] = time.Now().Add(-20 * time.Minute) // idle 20m > custom 10m

	tenMin := 10
	st := newFakeStore(
		map[string]*db.App{"app": {
			ID:                      1,
			Slug:                    "app",
			Status:                  "running",
			Replicas:                1,
			HibernateTimeoutMinutes: &tenMin,
			UpdatedAt:               time.Now().Add(-30 * time.Minute),
		}},
		nil,
	)
	w := newTestWatcher(Config{HibernateTimeout: 30 * time.Minute, RestartMaxAttempts: 5},
		mgr, prx, st, func(slug, dir string, idx int) (*deploy.Result, error) { return &deploy.Result{}, nil })

	w.runOnce()

	if len(mgr.stopped) == 0 {
		t.Error("expected app stopped (custom 10m timeout exceeded)")
	}
}

func TestHibernation_GloballyDisabled(t *testing.T) {
	mgr := &fakeManager{entries: []*process.ProcessInfo{
		{Slug: "app", Index: 0, Status: process.StatusRunning},
	}}
	prx := newFakeProxy()
	prx.seen["app"] = time.Now().Add(-2 * time.Hour)

	st := newFakeStore(
		map[string]*db.App{"app": {ID: 1, Slug: "app", Status: "running", Replicas: 1, UpdatedAt: time.Now().Add(-3 * time.Hour)}},
		nil,
	)
	w := newTestWatcher(Config{HibernateTimeout: 0, RestartMaxAttempts: 5},
		mgr, prx, st, func(slug, dir string, idx int) (*deploy.Result, error) { return &deploy.Result{}, nil })

	w.runOnce()

	if len(mgr.stopped) > 0 {
		t.Errorf("expected no stop (globally disabled), got %v", mgr.stopped)
	}
}

// waitNotWaking blocks until the wake for slug has finished, indicated by the
// app leaving the transient "waking" status (set by BeginWake) for "running"
// (success) or back to "hibernated" (reverted failure). Fails after 2s.
func waitNotWaking(t *testing.T, st *fakeStore, slug string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		st.mu.Lock()
		s := ""
		if app, ok := st.apps[slug]; ok {
			s = app.Status
		}
		st.mu.Unlock()
		if s != "waking" {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("timed out waiting for wake to finish (slug=%q)", slug)
}

// --- wake-on-request tests ---

func TestWake_TriggeredOnWakeTrigger(t *testing.T) {
	prx := newFakeProxy()
	st := newFakeStore(
		map[string]*db.App{"app": {ID: 1, Slug: "app", Status: "hibernated", Replicas: 1}},
		[]*db.Deployment{{BundleDir: "/bundles/v1"}},
	)
	var deployed []string
	var mu sync.Mutex
	w := newTestWatcher(Config{RestartMaxAttempts: 5}, &fakeManager{}, prx, st,
		func(slug, bundleDir string, idx int) (*deploy.Result, error) {
			mu.Lock()
			deployed = append(deployed, slug)
			mu.Unlock()
			return &deploy.Result{Index: idx, PID: 33, Port: 20033}, nil
		})

	w.WakeTrigger("app")
	waitNotWaking(t, st, "app")

	mu.Lock()
	gotDeployed := make([]string, len(deployed))
	copy(gotDeployed, deployed)
	mu.Unlock()

	if len(gotDeployed) != 1 || gotDeployed[0] != "app" {
		t.Errorf("expected deployFn('app') called once, got %v", gotDeployed)
	}

	st.mu.Lock()
	defer st.mu.Unlock()
	// Wake finalizes via the FinishWake CAS (waking -> running), so assert the
	// app's resulting status rather than the UpdateAppStatus call log.
	if got := st.apps["app"].Status; got != "running" {
		t.Fatalf("app status after wake = %q, want running", got)
	}
	// Verify UpsertReplica was called with running status.
	if len(st.upsertedReplicas) == 0 {
		t.Fatal("expected UpsertReplica call after wake")
	}
	ur := st.upsertedReplicas[len(st.upsertedReplicas)-1]
	if ur.Status != "running" || ur.PID == nil || *ur.PID != 33 || ur.Port == nil || *ur.Port != 20033 {
		t.Fatalf("unexpected UpsertReplica params after wake: %+v", ur)
	}
}

func TestWake_NoConcurrentWakes(t *testing.T) {
	prx := newFakeProxy()
	st := newFakeStore(
		map[string]*db.App{"app": {ID: 1, Slug: "app", Status: "hibernated", Replicas: 1}},
		[]*db.Deployment{{BundleDir: "/bundles/v1"}},
	)
	var deployCount int32
	w := newTestWatcher(Config{RestartMaxAttempts: 5}, &fakeManager{}, prx, st,
		func(slug, bundleDir string, idx int) (*deploy.Result, error) {
			atomic.AddInt32(&deployCount, 1)
			time.Sleep(30 * time.Millisecond) // slow to create race window
			return &deploy.Result{Index: idx, PID: 44, Port: 20044}, nil
		})

	// Two concurrent WakeTrigger calls should result in exactly one deploy.
	w.WakeTrigger("app")
	w.WakeTrigger("app")
	waitNotWaking(t, st, "app")

	if n := atomic.LoadInt32(&deployCount); n != 1 {
		t.Errorf("expected exactly 1 deploy for concurrent WakeTrigger, got %d", n)
	}
}

// TestWake_SupersededByStopTearsDownReplicas proves that when a concurrent stop
// moves the app off "waking" while the wake is deploying, the wake leaves the
// stopped status intact (FinishWake loses the CAS) AND tears down the replicas
// it started, so no live processes are orphaned for a stopped app.
func TestWake_SupersededByStopTearsDownReplicas(t *testing.T) {
	prx := newFakeProxy()
	st := newFakeStore(
		map[string]*db.App{"app": {ID: 1, Slug: "app", Status: "hibernated", Replicas: 1}},
		[]*db.Deployment{{BundleDir: "/bundles/v1"}},
	)
	mgr := &fakeManager{}
	w := newTestWatcher(Config{RestartMaxAttempts: 5}, mgr, prx, st,
		func(slug, bundleDir string, idx int) (*deploy.Result, error) {
			// Simulate a concurrent stop landing mid-deploy: move off "waking".
			_ = st.UpdateAppStatus(db.UpdateAppStatusParams{Slug: "app", Status: "stopped"})
			return &deploy.Result{Index: idx, PID: 33, Port: 20033}, nil
		})

	w.WakeTrigger("app")
	w.wakeWG.Wait() // deterministic: blocks until the wake goroutine fully exits

	if got := st.apps["app"].Status; got != "stopped" {
		t.Fatalf("app status = %q, want stopped (wake must not clobber a concurrent stop)", got)
	}
	mgr.mu.Lock()
	stopped := append([]string(nil), mgr.stopped...)
	mgr.mu.Unlock()
	found := false
	for _, s := range stopped {
		if s == "app" {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected mgr.Stop(app) to tear down superseded-wake replicas, got %v", stopped)
	}
}

// TestWake_SupersededByDeleteTearsDownReplicas proves that when a concurrent
// delete removes the app row while the wake is deploying, the wake (whose final
// GetAppBySlug then returns ErrNotFound) still tears down the replicas it
// started, so a deleted app leaves no orphaned processes.
func TestWake_SupersededByDeleteTearsDownReplicas(t *testing.T) {
	prx := newFakeProxy()
	st := newFakeStore(
		map[string]*db.App{"app": {ID: 1, Slug: "app", Status: "hibernated", Replicas: 1}},
		[]*db.Deployment{{BundleDir: "/bundles/v1"}},
	)
	mgr := &fakeManager{}
	w := newTestWatcher(Config{RestartMaxAttempts: 5}, mgr, prx, st,
		func(slug, bundleDir string, idx int) (*deploy.Result, error) {
			// Simulate a concurrent delete removing the row mid-deploy.
			st.mu.Lock()
			delete(st.apps, "app")
			st.mu.Unlock()
			return &deploy.Result{Index: idx, PID: 33, Port: 20033}, nil
		})

	w.WakeTrigger("app")
	w.wakeWG.Wait()

	mgr.mu.Lock()
	defer mgr.mu.Unlock()
	found := false
	for _, s := range mgr.stopped {
		if s == "app" {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected mgr.Stop(app) after a delete superseded the wake, got %v", mgr.stopped)
	}
}

func TestHibernation_ActiveAppNotStopped(t *testing.T) {
	mgr := &fakeManager{entries: []*process.ProcessInfo{
		{Slug: "app", Index: 0, Status: process.StatusRunning},
	}}
	prx := newFakeProxy()
	prx.seen["app"] = time.Now().Add(-5 * time.Minute) // recently active, under timeout

	st := newFakeStore(
		map[string]*db.App{"app": {
			ID:        1,
			Slug:      "app",
			Status:    "running",
			Replicas:  1,
			UpdatedAt: time.Now().Add(-10 * time.Minute),
		}},
		nil,
	)
	w := newTestWatcher(Config{HibernateTimeout: 30 * time.Minute, RestartMaxAttempts: 5},
		mgr, prx, st, func(slug, dir string, idx int) (*deploy.Result, error) { return &deploy.Result{}, nil })

	w.runOnce()

	if len(mgr.stopped) > 0 {
		t.Errorf("expected no stop (app recently active), got %v", mgr.stopped)
	}
}

func TestWake_NonHibernatedAppNotRedeployed(t *testing.T) {
	prx := newFakeProxy()
	st := newFakeStore(
		map[string]*db.App{"app": {ID: 1, Slug: "app", Status: "running", Replicas: 1}}, // not hibernated
		[]*db.Deployment{{BundleDir: "/bundles/v1"}},
	)
	var deployCount int32
	w := newTestWatcher(Config{RestartMaxAttempts: 5}, &fakeManager{}, prx, st,
		func(slug, bundleDir string, idx int) (*deploy.Result, error) {
			atomic.AddInt32(&deployCount, 1)
			return &deploy.Result{Index: idx, PID: 55, Port: 20055}, nil
		})

	w.WakeTrigger("app")
	waitNotWaking(t, st, "app")

	if n := atomic.LoadInt32(&deployCount); n != 0 {
		t.Errorf("expected 0 deploys for non-hibernated app, got %d", n)
	}
}

// --- per-replica crash tracking tests ---

func TestWatcher_OneReplicaCrashesOtherStays(t *testing.T) {
	mgr := &fakeManager{
		entries: []*process.ProcessInfo{
			{Slug: "demo", Index: 0, Status: process.StatusCrashed},
			{Slug: "demo", Index: 1, Status: process.StatusRunning},
		},
	}
	st := newFakeStore(
		map[string]*db.App{"demo": {ID: 1, Slug: "demo", Status: "running", Replicas: 2}},
		[]*db.Deployment{{BundleDir: "/tmp/demo"}},
	)
	st.replicas = map[int64][]*db.Replica{
		1: {
			{AppID: 1, Index: 0, Status: "crashed"},
			{AppID: 1, Index: 1, Status: "running"},
		},
	}
	var restartedIndex int = -1
	w := newTestWatcher(Config{WatchInterval: time.Millisecond, RestartMaxAttempts: 3},
		mgr, newFakeProxy(), st,
		func(slug, dir string, idx int) (*deploy.Result, error) {
			restartedIndex = idx
			return &deploy.Result{Index: idx, PID: 42, Port: 20002}, nil
		})
	w.runOnce()

	if restartedIndex != 0 {
		t.Fatalf("expected replica 0 restart, got %d", restartedIndex)
	}
	if got := st.appStatus["demo"]; got == "degraded" {
		t.Fatalf("app should not be degraded while replica 1 runs; got %q", got)
	}
}

func TestWatcher_AllReplicasDegraded(t *testing.T) {
	mgr := &fakeManager{
		entries: []*process.ProcessInfo{
			{Slug: "demo", Index: 0, Status: process.StatusCrashed},
			{Slug: "demo", Index: 1, Status: process.StatusCrashed},
		},
	}
	st := newFakeStore(
		map[string]*db.App{"demo": {ID: 1, Slug: "demo", Status: "running", Replicas: 2}},
		[]*db.Deployment{{BundleDir: "/tmp/demo"}},
	)
	st.replicas = map[int64][]*db.Replica{
		1: {
			{AppID: 1, Index: 0, Status: "crashed"},
			{AppID: 1, Index: 1, Status: "crashed"},
		},
	}
	w := newTestWatcher(Config{WatchInterval: time.Millisecond, RestartMaxAttempts: 1},
		mgr, newFakeProxy(), st,
		func(slug, dir string, idx int) (*deploy.Result, error) { return nil, fmt.Errorf("boom") })
	// exhaust attempts for both replicas
	w.runOnce()
	// advance nextRetry for both replicas so the second round fires
	w.mu.Lock()
	w.nextRetry[replicaKey{"demo", 0}] = time.Now().Add(-time.Second)
	w.nextRetry[replicaKey{"demo", 1}] = time.Now().Add(-time.Second)
	w.mu.Unlock()
	w.runOnce()
	if st.appStatus["demo"] != "degraded" {
		t.Fatalf("expected degraded after all replicas exhaust retries, got %q", st.appStatus["demo"])
	}
}

// --- pool-aware hibernation and wake tests ---

func TestHibernation_DrainsPool(t *testing.T) {
	mgr := &fakeManager{entries: []*process.ProcessInfo{
		{Slug: "app", Index: 0, Status: process.StatusRunning},
		{Slug: "app", Index: 1, Status: process.StatusRunning},
	}}
	prx := newFakeProxy()
	prx.seen["app"] = time.Now().Add(-2 * time.Hour)

	st := newFakeStore(
		map[string]*db.App{"app": {
			ID:        1,
			Slug:      "app",
			Status:    "running",
			Replicas:  2,
			UpdatedAt: time.Now().Add(-3 * time.Hour),
		}},
		nil,
	)
	w := newTestWatcher(Config{HibernateTimeout: 30 * time.Minute, RestartMaxAttempts: 5},
		mgr, prx, st, func(slug, dir string, idx int) (*deploy.Result, error) { return &deploy.Result{}, nil })

	w.runOnce()

	if len(mgr.stopped) == 0 || mgr.stopped[0] != "app" {
		t.Errorf("expected manager.Stop('app'), got %v", mgr.stopped)
	}
	// Verify UpsertReplica called with stopped status for each replica (0 and 1).
	st.mu.Lock()
	defer st.mu.Unlock()
	if len(st.upsertedReplicas) != 2 {
		t.Fatalf("expected 2 UpsertReplica calls (one per replica), got %d", len(st.upsertedReplicas))
	}
	seen := map[int]bool{}
	for _, ur := range st.upsertedReplicas {
		if ur.Status != "stopped" {
			t.Errorf("expected UpsertReplica status=stopped, got %q", ur.Status)
		}
		seen[ur.Index] = true
	}
	if !seen[0] || !seen[1] {
		t.Errorf("expected UpsertReplica for indices 0 and 1, got %v", seen)
	}
}

func TestWatcher_WakeTriggerWakesAllReplicas(t *testing.T) {
	prx := newFakeProxy()
	st := newFakeStore(
		map[string]*db.App{"demo": {ID: 1, Slug: "demo", Status: "hibernated", Replicas: 3}},
		[]*db.Deployment{{BundleDir: "/tmp/demo"}},
	)
	var mu sync.Mutex
	started := map[int]bool{}
	w := newTestWatcher(Config{RestartMaxAttempts: 5}, &fakeManager{}, prx, st,
		func(slug, dir string, idx int) (*deploy.Result, error) {
			mu.Lock()
			started[idx] = true
			mu.Unlock()
			return &deploy.Result{Index: idx, PID: 100 + idx, Port: 20000 + idx}, nil
		})
	w.WakeTrigger("demo")
	waitNotWaking(t, st, "demo")

	mu.Lock()
	defer mu.Unlock()
	if !(started[0] && started[1] && started[2]) {
		t.Fatalf("expected all 3 replicas waked; got %v", started)
	}
	// Verify SetPoolSize was called.
	prx.mu.Lock()
	defer prx.mu.Unlock()
	if prx.poolSizes["demo"] != 3 {
		t.Errorf("expected SetPoolSize('demo', 3), got %v", prx.poolSizes)
	}
}

func TestWake_AllReplicasFailKeepsHibernated(t *testing.T) {
	prx := newFakeProxy()
	st := &fakeStore{
		apps:        map[string]*db.App{"demo": {ID: 1, Slug: "demo", Status: "hibernated", Replicas: 2}},
		deployments: []*db.Deployment{{BundleDir: "/tmp/demo"}},
	}
	w := newTestWatcher(Config{RestartMaxAttempts: 5}, &fakeManager{}, prx, st,
		func(slug, dir string, idx int) (*deploy.Result, error) { return nil, fmt.Errorf("boom") })

	w.WakeTrigger("demo")
	waitNotWaking(t, st, "demo")

	st.mu.Lock()
	defer st.mu.Unlock()
	for _, upd := range st.statusUpdates {
		if upd.Status == "running" {
			t.Fatal("app marked running despite all replicas failing")
		}
	}
}

// TestReaperGate_ClusteredCallsReap asserts that a single runOnce tick with
// Clustered:true calls ReapStaleReplicaSessions exactly once. This pins the
// owner-gated cleanup path so a regression removing the if-clustered guard
// fails loudly.
func TestReaperGate_ClusteredCallsReap(t *testing.T) {
	prx := newFakeProxy()
	st := newFakeStore(map[string]*db.App{}, nil)
	w := newTestWatcher(Config{Clustered: true}, &fakeManager{}, prx, st, nil)

	w.RunOnce()

	st.mu.Lock()
	n := st.reapCount
	st.mu.Unlock()
	if n != 1 {
		t.Errorf("clustered runOnce: expected ReapStaleReplicaSessions called 1 time, got %d", n)
	}
}

// TestReaperGate_SingleNodeSkipsReap asserts that a single runOnce tick with
// Clustered:false (the single-node default) never calls ReapStaleReplicaSessions.
// This is the invariant that keeps single-node behaviour byte-for-byte unchanged:
// no DELETE FROM replica_sessions is issued on SQLite deployments.
func TestReaperGate_SingleNodeSkipsReap(t *testing.T) {
	prx := newFakeProxy()
	st := newFakeStore(map[string]*db.App{}, nil)
	w := newTestWatcher(Config{Clustered: false}, &fakeManager{}, prx, st, nil)

	w.RunOnce()

	st.mu.Lock()
	n := st.reapCount
	st.mu.Unlock()
	if n != 0 {
		t.Errorf("single-node runOnce: expected ReapStaleReplicaSessions NOT called, got %d call(s)", n)
	}
}

// --- clustered hibernation tests ---

// idleApp builds a running app in the store that has been idle for 2 hours, with
// a proxy that has a stale lastSeen so BeginHibernate returns true.
func idleClusteredSetup(t *testing.T) (*fakeManager, *fakeProxy, *fakeStore) {
	t.Helper()
	mgr := &fakeManager{entries: []*process.ProcessInfo{
		{Slug: "app", Index: 0, Status: process.StatusRunning},
	}}
	prx := newFakeProxy()
	prx.seen["app"] = time.Now().Add(-2 * time.Hour)
	st := newFakeStore(
		map[string]*db.App{"app": {
			ID:        42,
			Slug:      "app",
			Status:    "running",
			Replicas:  1,
			UpdatedAt: time.Now().Add(-3 * time.Hour),
		}},
		nil,
	)
	return mgr, prx, st
}

// TestClusteredHibernation_OtherInstanceActiveBlocksHibernation asserts that the
// clustered path does NOT hibernate when another instance reports active>0, even
// though the local predicate (lastSeen old, activeConns 0) would pass.
func TestClusteredHibernation_OtherInstanceActiveBlocksHibernation(t *testing.T) {
	mgr, prx, st := idleClusteredSetup(t)
	// Simulate another instance with active sessions; idleSinceSec=0 means
	// activity just happened (blocks hibernation as a belt-and-suspenders check,
	// but the active>0 guard fires first).
	st.fleetActive = 3
	st.fleetIdleSinceSec = 0

	w := newTestWatcher(Config{
		HibernateTimeout:   30 * time.Minute,
		RestartMaxAttempts: 5,
		Clustered:          true,
		InstanceID:         "self",
	}, mgr, prx, st, func(slug, dir string, idx int) (*deploy.Result, error) { return &deploy.Result{}, nil })

	w.runOnce()

	st.mu.Lock()
	calls := append([]string(nil), st.hibernateAppCalls...)
	st.mu.Unlock()
	if len(calls) != 0 {
		t.Errorf("HibernateApp must NOT fire when another instance has active sessions, got %v", calls)
	}
	if len(mgr.stopped) != 0 {
		t.Errorf("mgr.Stop must NOT be called when fleet has active sessions, got %v", mgr.stopped)
	}
}

// TestClusteredHibernation_LocalRaceBlocksDBCAS asserts that when the time-idle
// check (A) passes and the fleet is idle (B), but BeginHibernate (C) returns
// false - simulating a local in-flight request or activeConns>0 - the DB CAS
// (HibernateApp) is never issued and mgr.Stop is never called.
//
// lastSeen is set to 2h ago so the time-idle check passes. hibernateNever forces
// BeginHibernate to return false regardless of the seen timestamp, isolating the
// (C) guard from the (A) time check. This is the only test that exercises the
// path where handleIdleClustered reaches BeginHibernate and is blocked there.
func TestClusteredHibernation_LocalRaceBlocksDBCAS(t *testing.T) {
	mgr := &fakeManager{entries: []*process.ProcessInfo{
		{Slug: "app", Index: 0, Status: process.StatusRunning},
	}}
	prx := newFakeProxy()
	// lastSeen old enough to pass the time-idle check (A).
	prx.seen["app"] = time.Now().Add(-2 * time.Hour)
	// hibernateNever makes BeginHibernate always return false, simulating a local
	// in-flight request (activeConns>0) that races in after the time check.
	prx.hibernateNever = true

	st := newFakeStore(
		map[string]*db.App{"app": {
			ID:        42,
			Slug:      "app",
			Status:    "running",
			Replicas:  1,
			UpdatedAt: time.Now().Add(-3 * time.Hour),
		}},
		nil,
	)
	// Fleet is idle so check (B) passes - the only thing blocking hibernation is (C).
	st.fleetActive = 0
	st.fleetIdleSinceSec = db.NoFleetActivity

	w := newTestWatcher(Config{
		HibernateTimeout:   30 * time.Minute,
		RestartMaxAttempts: 5,
		Clustered:          true,
		InstanceID:         "self",
	}, mgr, prx, st, func(slug, dir string, idx int) (*deploy.Result, error) { return &deploy.Result{}, nil })

	w.runOnce()

	// BeginHibernate returned false: the DB CAS must not fire.
	st.mu.Lock()
	calls := append([]string(nil), st.hibernateAppCalls...)
	st.mu.Unlock()
	if len(calls) != 0 {
		t.Errorf("HibernateApp DB CAS must NOT fire when BeginHibernate returns false, got %v", calls)
	}
	if len(mgr.stopped) != 0 {
		t.Errorf("mgr.Stop must NOT be called when BeginHibernate returns false, got %v", mgr.stopped)
	}
}

// TestClusteredHibernation_FleetIdleButLocalRecentlyActivePreventsHibernation
// asserts that when time.Since(lastActivity) < timeout the app is NOT hibernated,
// even if the fleet is idle. This proves the local time-idle predicate (A) is
// retained and not dropped in the clustered path.
func TestClusteredHibernation_FleetIdleButLocalRecentlyActivePreventsHibernation(t *testing.T) {
	mgr := &fakeManager{entries: []*process.ProcessInfo{
		{Slug: "app", Index: 0, Status: process.StatusRunning},
	}}
	prx := newFakeProxy()
	// lastSeen only 5 minutes ago, well under the 30-minute timeout.
	prx.seen["app"] = time.Now().Add(-5 * time.Minute)
	st := newFakeStore(
		map[string]*db.App{"app": {
			ID:        42,
			Slug:      "app",
			Status:    "running",
			Replicas:  1,
			UpdatedAt: time.Now().Add(-10 * time.Minute),
		}},
		nil,
	)
	st.fleetActive = 0
	st.fleetIdleSinceSec = db.NoFleetActivity

	w := newTestWatcher(Config{
		HibernateTimeout:   30 * time.Minute,
		RestartMaxAttempts: 5,
		Clustered:          true,
		InstanceID:         "self",
	}, mgr, prx, st, func(slug, dir string, idx int) (*deploy.Result, error) { return &deploy.Result{}, nil })

	w.runOnce()

	st.mu.Lock()
	calls := append([]string(nil), st.hibernateAppCalls...)
	st.mu.Unlock()
	if len(calls) != 0 {
		t.Errorf("HibernateApp must NOT fire when local lastActivity < timeout, got %v", calls)
	}
	if len(mgr.stopped) != 0 {
		t.Errorf("mgr.Stop must NOT be called when local app is recently active, got %v", mgr.stopped)
	}
}

// TestClusteredHibernation_FleetIdleAndLocalIdleHibernates asserts that when
// both the fleet and the local predicates pass, the CAS fires and replicas are
// stopped in the correct order (CAS first, then Stop).
func TestClusteredHibernation_FleetIdleAndLocalIdleHibernates(t *testing.T) {
	mgr, prx, st := idleClusteredSetup(t)
	// Fleet is idle: no other instances, no recent activity.
	st.fleetActive = 0
	st.fleetIdleSinceSec = db.NoFleetActivity

	var stopOrder []string
	var mu sync.Mutex

	w := newTestWatcher(Config{
		HibernateTimeout:   30 * time.Minute,
		RestartMaxAttempts: 5,
		Clustered:          true,
		InstanceID:         "self",
	}, mgr, prx, st, func(slug, dir string, idx int) (*deploy.Result, error) { return &deploy.Result{}, nil })

	// Patch mgr.Stop to record that HibernateApp was already called before Stop.
	// We verify order by inspecting hibernateAppCalls at Stop time.
	origMgr := w.mgr
	w.mgr = &orderCheckingManager{
		inner: origMgr,
		onStop: func(slug string) {
			st.mu.Lock()
			hasCalls := len(st.hibernateAppCalls)
			st.mu.Unlock()
			mu.Lock()
			if hasCalls > 0 {
				stopOrder = append(stopOrder, "stop-after-cas")
			} else {
				stopOrder = append(stopOrder, "stop-before-cas")
			}
			mu.Unlock()
		},
	}

	w.runOnce()

	st.mu.Lock()
	hibernateCalls := append([]string(nil), st.hibernateAppCalls...)
	st.mu.Unlock()

	if len(hibernateCalls) != 1 || hibernateCalls[0] != "app" {
		t.Errorf("HibernateApp must fire once for 'app', got %v", hibernateCalls)
	}
	if len(mgr.stopped) == 0 || mgr.stopped[0] != "app" {
		t.Errorf("mgr.Stop('app') must be called after CAS, got %v", mgr.stopped)
	}
	mu.Lock()
	ord := append([]string(nil), stopOrder...)
	mu.Unlock()
	if len(ord) == 0 || ord[0] != "stop-after-cas" {
		t.Errorf("CAS must commit before Stop: got order %v", ord)
	}

	// Verify replica rows updated.
	st.mu.Lock()
	defer st.mu.Unlock()
	if len(st.upsertedReplicas) == 0 {
		t.Fatal("expected UpsertReplica called after CAS hibernation")
	}
	for _, ur := range st.upsertedReplicas {
		if ur.Status != "stopped" {
			t.Errorf("expected UpsertReplica status=stopped, got %q", ur.Status)
		}
	}
	// In clustered mode, UpdateAppStatus(hibernated) must NOT be called as a
	// separate step because the CAS already set the status.
	for _, upd := range st.statusUpdates {
		if upd.Slug == "app" && upd.Status == "hibernated" {
			t.Errorf("UpdateAppStatus(hibernated) must NOT be called in clustered mode (CAS did it), got %+v", upd)
		}
	}
}

// TestClusteredHibernation_SingleNodeUnchanged asserts that with Clustered:false
// the original single-node path is taken: AppFleetLoad is never called,
// HibernateApp CAS is never called, and the unconditional UpdateAppStatus is used.
func TestClusteredHibernation_SingleNodeUnchanged(t *testing.T) {
	mgr := &fakeManager{entries: []*process.ProcessInfo{
		{Slug: "app", Index: 0, Status: process.StatusRunning},
	}}
	prx := newFakeProxy()
	prx.seen["app"] = time.Now().Add(-2 * time.Hour)
	st := newFakeStore(
		map[string]*db.App{"app": {
			ID:        42,
			Slug:      "app",
			Status:    "running",
			Replicas:  1,
			UpdatedAt: time.Now().Add(-3 * time.Hour),
		}},
		nil,
	)

	w := newTestWatcher(Config{
		HibernateTimeout:   30 * time.Minute,
		RestartMaxAttempts: 5,
		Clustered:          false,
	}, mgr, prx, st, func(slug, dir string, idx int) (*deploy.Result, error) { return &deploy.Result{}, nil })

	w.runOnce()

	// Single-node path: Stop must fire.
	if len(mgr.stopped) == 0 || mgr.stopped[0] != "app" {
		t.Errorf("single-node: expected mgr.Stop('app'), got %v", mgr.stopped)
	}
	// Single-node path: BeginHibernate must fire (local CAS).
	if len(prx.hibernated) == 0 || prx.hibernated[0] != "app" {
		t.Errorf("single-node: expected proxy.BeginHibernate('app'), got %v", prx.hibernated)
	}
	// Single-node path: unconditional UpdateAppStatus(hibernated) must fire.
	st.mu.Lock()
	defer st.mu.Unlock()
	found := false
	for _, upd := range st.statusUpdates {
		if upd.Slug == "app" && upd.Status == "hibernated" {
			found = true
		}
	}
	if !found {
		t.Errorf("single-node: expected UpdateAppStatus(hibernated), got %v", st.statusUpdates)
	}
	// Single-node path: HibernateApp CAS must NOT be called.
	if len(st.hibernateAppCalls) != 0 {
		t.Errorf("single-node: HibernateApp must NOT be called, got %v", st.hibernateAppCalls)
	}
}

// TestClusteredHibernation_OtherInstanceRecentActivityBlocksHibernation asserts
// that even when another instance has active=0 but a recent last_activity epoch
// (within the timeout window), hibernation is blocked. This covers the case where
// a peer finished serving all requests moments ago.
func TestClusteredHibernation_OtherInstanceRecentActivityBlocksHibernation(t *testing.T) {
	mgr, prx, st := idleClusteredSetup(t)
	// Other instance: no active sessions, but activity was only 5 minutes ago
	// (well within the 30-minute timeout). idleSinceSec = 5*60 = 300 seconds,
	// which is less than timeout (1800 s), so hibernation must be blocked.
	st.fleetActive = 0
	st.fleetIdleSinceSec = 5 * 60

	w := newTestWatcher(Config{
		HibernateTimeout:   30 * time.Minute,
		RestartMaxAttempts: 5,
		Clustered:          true,
		InstanceID:         "self",
	}, mgr, prx, st, func(slug, dir string, idx int) (*deploy.Result, error) { return &deploy.Result{}, nil })

	w.runOnce()

	st.mu.Lock()
	calls := append([]string(nil), st.hibernateAppCalls...)
	st.mu.Unlock()
	if len(calls) != 0 {
		t.Errorf("HibernateApp must NOT fire when other instance has recent last_activity, got %v", calls)
	}
}

// orderCheckingManager wraps a manager and fires onStop before delegating.
type orderCheckingManager struct {
	inner  manager
	onStop func(slug string)
}

func (m *orderCheckingManager) All() []*process.ProcessInfo { return m.inner.All() }
func (m *orderCheckingManager) Stop(slug string) error {
	if m.onStop != nil {
		m.onStop(slug)
	}
	return m.inner.Stop(slug)
}

// --- warm-shrink replaces hibernation tests ---

// TestHandleIdle_WarmShrinkReplacesHibernate: an app with MinWarmReplicas=2
// and Replicas=3 that is idle past the timeout must have warmShrink called
// with (slug, 2) and must NOT have BeginHibernate called on the proxy.
func TestHandleIdle_WarmShrinkReplacesHibernate(t *testing.T) {
	mgr := &fakeManager{entries: []*process.ProcessInfo{
		{Slug: "warm", Index: 0, Status: process.StatusRunning},
		{Slug: "warm", Index: 1, Status: process.StatusRunning},
		{Slug: "warm", Index: 2, Status: process.StatusRunning},
	}}
	prx := newFakeProxy()
	prx.seen["warm"] = time.Now().Add(-2 * time.Hour) // idle 2h > 30m timeout

	st := newFakeStore(
		map[string]*db.App{"warm": {
			ID:              1,
			Slug:            "warm",
			Status:          "running",
			Replicas:        3,
			MinWarmReplicas: 2,
			UpdatedAt:       time.Now().Add(-3 * time.Hour),
		}},
		nil,
	)

	var shrinkCalls []struct {
		slug  string
		floor int
	}
	var mu sync.Mutex
	w := newTestWatcher(Config{HibernateTimeout: 30 * time.Minute, RestartMaxAttempts: 5},
		mgr, prx, st, func(slug, dir string, idx int) (*deploy.Result, error) { return &deploy.Result{}, nil })
	w.SetWarmOps(
		func(slug string, floor int) (bool, error) {
			mu.Lock()
			shrinkCalls = append(shrinkCalls, struct {
				slug  string
				floor int
			}{slug, floor})
			mu.Unlock()
			return true, nil
		},
		func(slug string) (bool, error) { return false, nil },
	)

	w.runOnce()

	mu.Lock()
	calls := append([]struct {
		slug  string
		floor int
	}(nil), shrinkCalls...)
	mu.Unlock()

	if len(calls) != 1 {
		t.Fatalf("expected warmShrink called once, got %d calls", len(calls))
	}
	if calls[0].slug != "warm" || calls[0].floor != 2 {
		t.Errorf("warmShrink called with (%q, %d), want (\"warm\", 2)", calls[0].slug, calls[0].floor)
	}
	// BeginHibernate must NOT be called.
	prx.mu.Lock()
	hibernated := append([]string(nil), prx.hibernated...)
	prx.mu.Unlock()
	if len(hibernated) != 0 {
		t.Errorf("BeginHibernate must NOT be called when warmShrink is wired, got %v", hibernated)
	}
	// mgr.Stop must NOT be called.
	mgr.mu.Lock()
	stopped := append([]string(nil), mgr.stopped...)
	mgr.mu.Unlock()
	if len(stopped) != 0 {
		t.Errorf("mgr.Stop must NOT be called when warmShrink is wired, got %v", stopped)
	}
	// App status must not be set to hibernated.
	st.mu.Lock()
	defer st.mu.Unlock()
	for _, upd := range st.statusUpdates {
		if upd.Slug == "warm" && upd.Status == "hibernated" {
			t.Errorf("app status must not be set to hibernated when warm shrinking, got %+v", upd)
		}
	}
}

// TestHandleIdle_ZeroFloorHibernatesExactlyAsToday: an app with
// MinWarmReplicas=0 idle past the timeout must follow the original full
// hibernate path (BeginHibernate + mgr.Stop + status=hibernated).
// This pins against the existing TestHibernation_StopsIdleApp behaviour.
func TestHandleIdle_ZeroFloorHibernatesExactlyAsToday(t *testing.T) {
	mgr := &fakeManager{entries: []*process.ProcessInfo{
		{Slug: "app", Index: 0, Status: process.StatusRunning},
	}}
	prx := newFakeProxy()
	prx.seen["app"] = time.Now().Add(-2 * time.Hour)

	st := newFakeStore(
		map[string]*db.App{"app": {
			ID:              1,
			Slug:            "app",
			Status:          "running",
			Replicas:        1,
			MinWarmReplicas: 0, // zero floor = full hibernate
			UpdatedAt:       time.Now().Add(-3 * time.Hour),
		}},
		nil,
	)

	var shrinkCalled bool
	w := newTestWatcher(Config{HibernateTimeout: 30 * time.Minute, RestartMaxAttempts: 5},
		mgr, prx, st, func(slug, dir string, idx int) (*deploy.Result, error) { return &deploy.Result{}, nil })
	w.SetWarmOps(
		func(slug string, floor int) (bool, error) { shrinkCalled = true; return false, nil },
		func(slug string) (bool, error) { return false, nil },
	)

	w.runOnce()

	if shrinkCalled {
		t.Error("warmShrink must not be called when MinWarmReplicas=0")
	}
	if len(mgr.stopped) == 0 || mgr.stopped[0] != "app" {
		t.Errorf("expected manager.Stop('app') for W=0, got %v", mgr.stopped)
	}
	if len(prx.hibernated) == 0 || prx.hibernated[0] != "app" {
		t.Errorf("expected proxy.BeginHibernate('app') for W=0, got %v", prx.hibernated)
	}
	st.mu.Lock()
	defer st.mu.Unlock()
	found := false
	for _, upd := range st.statusUpdates {
		if upd.Slug == "app" && upd.Status == "hibernated" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected UpdateAppStatus(hibernated) for W=0, got %v", st.statusUpdates)
	}
}

// TestHandleIdle_NotIdleNoShrink: an app with MinWarmReplicas=2 but recent
// activity must trigger neither warmShrink nor BeginHibernate.
func TestHandleIdle_NotIdleNoShrink(t *testing.T) {
	mgr := &fakeManager{entries: []*process.ProcessInfo{
		{Slug: "active", Index: 0, Status: process.StatusRunning},
	}}
	prx := newFakeProxy()
	prx.seen["active"] = time.Now().Add(-5 * time.Minute) // recent activity

	st := newFakeStore(
		map[string]*db.App{"active": {
			ID:              1,
			Slug:            "active",
			Status:          "running",
			Replicas:        3,
			MinWarmReplicas: 2,
			UpdatedAt:       time.Now().Add(-10 * time.Minute),
		}},
		nil,
	)

	var shrinkCalled, hibernateCalled bool
	w := newTestWatcher(Config{HibernateTimeout: 30 * time.Minute, RestartMaxAttempts: 5},
		mgr, prx, st, func(slug, dir string, idx int) (*deploy.Result, error) { return &deploy.Result{}, nil })
	w.SetWarmOps(
		func(slug string, floor int) (bool, error) { shrinkCalled = true; return false, nil },
		func(slug string) (bool, error) { return false, nil },
	)

	w.runOnce()

	if shrinkCalled {
		t.Error("warmShrink must not be called when app is not idle")
	}
	prx.mu.Lock()
	hibernateCalled = len(prx.hibernated) > 0
	prx.mu.Unlock()
	if hibernateCalled {
		t.Error("BeginHibernate must not be called when app is not idle")
	}
}

// TestHandleIdle_NilWarmOpsFallsBackToHibernate: an app with MinWarmReplicas=2
// idle past the timeout, but SetWarmOps was never called (warmShrink==nil).
// The app must fully hibernate as if MinWarmReplicas were 0.
func TestHandleIdle_NilWarmOpsFallsBackToHibernate(t *testing.T) {
	mgr := &fakeManager{entries: []*process.ProcessInfo{
		{Slug: "fallback", Index: 0, Status: process.StatusRunning},
	}}
	prx := newFakeProxy()
	prx.seen["fallback"] = time.Now().Add(-2 * time.Hour)

	st := newFakeStore(
		map[string]*db.App{"fallback": {
			ID:              1,
			Slug:            "fallback",
			Status:          "running",
			Replicas:        3,
			MinWarmReplicas: 2, // floor set, but warmShrink is nil
			UpdatedAt:       time.Now().Add(-3 * time.Hour),
		}},
		nil,
	)

	// No SetWarmOps call: warmShrink remains nil.
	w := newTestWatcher(Config{HibernateTimeout: 30 * time.Minute, RestartMaxAttempts: 5},
		mgr, prx, st, func(slug, dir string, idx int) (*deploy.Result, error) { return &deploy.Result{}, nil })

	w.runOnce()

	if len(mgr.stopped) == 0 || mgr.stopped[0] != "fallback" {
		t.Errorf("nil warmShrink: expected manager.Stop('fallback'), got %v", mgr.stopped)
	}
	if len(prx.hibernated) == 0 || prx.hibernated[0] != "fallback" {
		t.Errorf("nil warmShrink: expected proxy.BeginHibernate('fallback'), got %v", prx.hibernated)
	}
	st.mu.Lock()
	defer st.mu.Unlock()
	found := false
	for _, upd := range st.statusUpdates {
		if upd.Slug == "fallback" && upd.Status == "hibernated" {
			found = true
		}
	}
	if !found {
		t.Errorf("nil warmShrink: expected UpdateAppStatus(hibernated), got %v", st.statusUpdates)
	}
}

// TestHandleIdle_ClusteredWarmShrinkReplacesHibernate: in clustered mode, an
// app with MinWarmReplicas=2 idle past the timeout (fleet-idle predicate
// satisfied) must call warmShrink and must NOT call HibernateApp CAS.
func TestHandleIdle_ClusteredWarmShrinkReplacesHibernate(t *testing.T) {
	mgr := &fakeManager{entries: []*process.ProcessInfo{
		{Slug: "cwarm", Index: 0, Status: process.StatusRunning},
	}}
	prx := newFakeProxy()
	prx.seen["cwarm"] = time.Now().Add(-2 * time.Hour) // idle 2h > 30m

	st := newFakeStore(
		map[string]*db.App{"cwarm": {
			ID:              42,
			Slug:            "cwarm",
			Status:          "running",
			Replicas:        3,
			MinWarmReplicas: 2,
			UpdatedAt:       time.Now().Add(-3 * time.Hour),
		}},
		nil,
	)
	// Fleet idle: no other instances, no recent activity.
	st.fleetActive = 0
	st.fleetIdleSinceSec = db.NoFleetActivity

	var shrinkCalls []struct {
		slug  string
		floor int
	}
	var mu sync.Mutex
	w := newTestWatcher(Config{
		HibernateTimeout:   30 * time.Minute,
		RestartMaxAttempts: 5,
		Clustered:          true,
		InstanceID:         "self",
	}, mgr, prx, st, func(slug, dir string, idx int) (*deploy.Result, error) { return &deploy.Result{}, nil })
	w.SetWarmOps(
		func(slug string, floor int) (bool, error) {
			mu.Lock()
			shrinkCalls = append(shrinkCalls, struct {
				slug  string
				floor int
			}{slug, floor})
			mu.Unlock()
			return true, nil
		},
		func(slug string) (bool, error) { return false, nil },
	)

	w.runOnce()

	mu.Lock()
	calls := append([]struct {
		slug  string
		floor int
	}(nil), shrinkCalls...)
	mu.Unlock()

	if len(calls) != 1 {
		t.Fatalf("clustered: expected warmShrink called once, got %d calls", len(calls))
	}
	if calls[0].slug != "cwarm" || calls[0].floor != 2 {
		t.Errorf("clustered: warmShrink called with (%q, %d), want (\"cwarm\", 2)", calls[0].slug, calls[0].floor)
	}
	// HibernateApp CAS must NOT be called.
	st.mu.Lock()
	casCalls := append([]string(nil), st.hibernateAppCalls...)
	st.mu.Unlock()
	if len(casCalls) != 0 {
		t.Errorf("clustered: HibernateApp CAS must NOT fire when warmShrink is wired, got %v", casCalls)
	}
	// mgr.Stop must NOT be called.
	mgr.mu.Lock()
	stopped := append([]string(nil), mgr.stopped...)
	mgr.mu.Unlock()
	if len(stopped) != 0 {
		t.Errorf("clustered: mgr.Stop must NOT be called when warmShrink is wired, got %v", stopped)
	}
}
