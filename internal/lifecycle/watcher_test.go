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
	poolSizes       map[string]int
	poolCaps        map[string]int
	onMissFn        func(string)
}

func newFakeProxy() *fakeProxy {
	return &fakeProxy{
		seen:      make(map[string]time.Time),
		poolSizes: make(map[string]int),
		poolCaps:  make(map[string]int),
	}
}

func (f *fakeProxy) SetOnMiss(fn func(string)) { f.mu.Lock(); f.onMissFn = fn; f.mu.Unlock() }
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
		return nil, fmt.Errorf("fakeStore: no app for slug %q", slug)
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
func (f *fakeStore) ListReplicas(appID int64) ([]*db.Replica, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.replicas[appID], nil
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

func TestWake_TriggeredOnMiss(t *testing.T) {
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

	w.OnMiss("app")
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

	// Two concurrent OnMiss calls should result in exactly one deploy.
	w.OnMiss("app")
	w.OnMiss("app")
	waitNotWaking(t, st, "app")

	if n := atomic.LoadInt32(&deployCount); n != 1 {
		t.Errorf("expected exactly 1 deploy for concurrent OnMiss, got %d", n)
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

	w.OnMiss("app")
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

func TestWatcher_OnMissWakesAllReplicas(t *testing.T) {
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
	w.OnMiss("demo")
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

	w.OnMiss("demo")
	waitNotWaking(t, st, "demo")

	st.mu.Lock()
	defer st.mu.Unlock()
	for _, upd := range st.statusUpdates {
		if upd.Status == "running" {
			t.Fatal("app marked running despite all replicas failing")
		}
	}
}
