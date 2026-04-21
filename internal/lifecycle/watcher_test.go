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
	f.mu.Unlock()
	return nil
}

type fakeProxy struct {
	mu           sync.Mutex
	seen         map[string]time.Time
	deregistered []string
	poolSizes    map[string]int
	onMissFn     func(string)
}

func newFakeProxy() *fakeProxy {
	return &fakeProxy{
		seen:      make(map[string]time.Time),
		poolSizes: make(map[string]int),
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
func (f *fakeProxy) SetPoolSize(slug string, size int) {
	f.mu.Lock()
	f.poolSizes[slug] = size
	f.mu.Unlock()
}

type fakeStore struct {
	mu              sync.Mutex
	apps            map[string]*db.App
	deployments     []*db.Deployment
	statusUpdates   []db.UpdateAppStatusParams
	appStatus       map[string]string
	upsertedReplicas []db.UpsertReplicaParams
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
func (f *fakeStore) ListDeployments(_ int64) ([]*db.Deployment, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.deployments, nil
}
func (f *fakeStore) UpsertReplica(p db.UpsertReplicaParams) error {
	f.mu.Lock()
	f.upsertedReplicas = append(f.upsertedReplicas, p)
	f.mu.Unlock()
	return nil
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
		waking:    make(map[string]bool),
	}
}

// --- watchdog tests ---

func TestWatchdog_RestartsOnCrash(t *testing.T) {
	mgr := &fakeManager{entries: []*process.ProcessInfo{
		{Slug: "myapp", Index: 0, Status: process.StatusCrashed},
	}}
	st := newFakeStore(
		map[string]*db.App{"myapp": {ID: 1, Slug: "myapp", Status: "crashed", Replicas: 1}},
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

func TestWatchdog_ExponentialBackoff(t *testing.T) {
	mgr := &fakeManager{entries: []*process.ProcessInfo{
		{Slug: "app", Index: 0, Status: process.StatusCrashed},
	}}
	st := newFakeStore(
		map[string]*db.App{"app": {ID: 1, Slug: "app", Status: "crashed", Replicas: 1}},
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

func TestWatchdog_GivesUpAfterMaxAttempts(t *testing.T) {
	mgr := &fakeManager{entries: []*process.ProcessInfo{
		{Slug: "app", Index: 0, Status: process.StatusCrashed},
	}}
	st := newFakeStore(
		map[string]*db.App{"app": {ID: 1, Slug: "app", Status: "crashed", Replicas: 1}},
		[]*db.Deployment{{BundleDir: "/bundles/v1"}},
	)
	w := newTestWatcher(Config{RestartMaxAttempts: 3}, mgr, newFakeProxy(), st,
		func(slug, bundleDir string, idx int) (*deploy.Result, error) { return nil, fmt.Errorf("always fails") })

	// Exhaust all allowed attempts.
	for i := 0; i < w.cfg.RestartMaxAttempts; i++ {
		w.mu.Lock()
		w.nextRetry[replicaKey{"app", 0}] = time.Now().Add(-time.Second)
		w.mu.Unlock()
		w.runOnce()
	}

	// This tick pushes attempts over the limit → must mark degraded.
	w.mu.Lock()
	w.nextRetry[replicaKey{"app", 0}] = time.Now().Add(-time.Second)
	w.mu.Unlock()
	w.runOnce()

	updates := st.statusUpdates
	if len(updates) == 0 || updates[len(updates)-1].Status != "degraded" {
		t.Errorf("expected final status=degraded, got %v", updates)
	}
}

func TestWatchdog_ResetsAttemptsOnSuccess(t *testing.T) {
	mgr := &fakeManager{entries: []*process.ProcessInfo{
		{Slug: "app", Index: 0, Status: process.StatusCrashed},
	}}
	st := newFakeStore(
		map[string]*db.App{"app": {ID: 1, Slug: "app", Status: "crashed", Replicas: 1}},
		[]*db.Deployment{{BundleDir: "/bundles/v1"}},
	)
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
	if len(prx.deregistered) == 0 || prx.deregistered[0] != "app" {
		t.Errorf("expected proxy.Deregister('app'), got %v", prx.deregistered)
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
	time.Sleep(50 * time.Millisecond)

	mu.Lock()
	gotDeployed := make([]string, len(deployed))
	copy(gotDeployed, deployed)
	mu.Unlock()

	if len(gotDeployed) != 1 || gotDeployed[0] != "app" {
		t.Errorf("expected deployFn('app') called once, got %v", gotDeployed)
	}

	st.mu.Lock()
	defer st.mu.Unlock()
	if len(st.statusUpdates) == 0 {
		t.Fatal("expected running status update after wake")
	}
	last := st.statusUpdates[len(st.statusUpdates)-1]
	if last.Status != "running" {
		t.Fatalf("unexpected wake update: %+v", last)
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
	time.Sleep(150 * time.Millisecond)

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
	time.Sleep(50 * time.Millisecond)

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
		map[string]*db.App{"demo": {ID: 1, Slug: "demo", Replicas: 2}},
		[]*db.Deployment{{BundleDir: "/tmp/demo"}},
	)
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
		map[string]*db.App{"demo": {ID: 1, Slug: "demo", Replicas: 2}},
		nil,
	)
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
	// Wait for the wake goroutine to finish.
	time.Sleep(100 * time.Millisecond)

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
