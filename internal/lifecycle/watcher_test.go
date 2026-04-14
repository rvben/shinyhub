package lifecycle

import (
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rvben/shinyhub/internal/db"
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
	onMissFn     func(string)
}

func newFakeProxy() *fakeProxy {
	return &fakeProxy{seen: make(map[string]time.Time)}
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

type fakeStore struct {
	mu            sync.Mutex
	app           *db.App
	deployments   []*db.Deployment
	statusUpdates []db.UpdateAppStatusParams
}

func (f *fakeStore) GetAppBySlug(_ string) (*db.App, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.app, nil
}
func (f *fakeStore) UpdateAppStatus(p db.UpdateAppStatusParams) error {
	f.mu.Lock()
	f.statusUpdates = append(f.statusUpdates, p)
	f.mu.Unlock()
	return nil
}
func (f *fakeStore) ListDeployments(_ int64) ([]*db.Deployment, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.deployments, nil
}

// newTestWatcher builds a Watcher with fakes. Tests in the same package can
// call runOnce() directly without starting the background goroutine.
func newTestWatcher(cfg Config, mgr *fakeManager, prx *fakeProxy, st *fakeStore,
	deployFn func(slug, bundleDir string) error) *Watcher {
	return &Watcher{
		cfg:       cfg,
		mgr:       mgr,
		prx:       prx,
		store:     st,
		deploy:    deployFn,
		attempts:  make(map[string]int),
		nextRetry: make(map[string]time.Time),
		waking:    make(map[string]bool),
	}
}

// --- watchdog tests ---

func TestWatchdog_RestartsOnCrash(t *testing.T) {
	mgr := &fakeManager{entries: []*process.ProcessInfo{
		{Slug: "myapp", Status: process.StatusCrashed},
	}}
	st := &fakeStore{
		app:         &db.App{ID: 1, Slug: "myapp", Status: "crashed"},
		deployments: []*db.Deployment{{BundleDir: "/bundles/v1"}},
	}
	var deployed []string
	var mu sync.Mutex
	w := newTestWatcher(Config{RestartMaxAttempts: 5}, mgr, newFakeProxy(), st,
		func(slug, bundleDir string) error {
			mu.Lock()
			deployed = append(deployed, slug)
			mu.Unlock()
			return nil
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
		{Slug: "app", Status: process.StatusCrashed},
	}}
	st := &fakeStore{
		app:         &db.App{ID: 1, Slug: "app", Status: "crashed"},
		deployments: []*db.Deployment{{BundleDir: "/bundles/v1"}},
	}
	var deployCount int32
	w := newTestWatcher(Config{RestartMaxAttempts: 5}, mgr, newFakeProxy(), st,
		func(slug, bundleDir string) error {
			atomic.AddInt32(&deployCount, 1)
			return fmt.Errorf("still crashed")
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
	w.nextRetry["app"] = time.Now().Add(-time.Second)
	w.mu.Unlock()

	w.runOnce()
	if got := atomic.LoadInt32(&deployCount); got != 2 {
		t.Errorf("expected 2 deploys after backoff elapsed, got %d", got)
	}
}

func TestWatchdog_GivesUpAfterMaxAttempts(t *testing.T) {
	mgr := &fakeManager{entries: []*process.ProcessInfo{
		{Slug: "app", Status: process.StatusCrashed},
	}}
	st := &fakeStore{
		app:         &db.App{ID: 1, Slug: "app", Status: "crashed"},
		deployments: []*db.Deployment{{BundleDir: "/bundles/v1"}},
	}
	w := newTestWatcher(Config{RestartMaxAttempts: 3}, mgr, newFakeProxy(), st,
		func(slug, bundleDir string) error { return fmt.Errorf("always fails") })

	// Run max attempts, advancing past the backoff each time.
	for i := 0; i < 3; i++ {
		w.mu.Lock()
		w.nextRetry["app"] = time.Now().Add(-time.Second)
		w.mu.Unlock()
		w.runOnce()
	}

	// One more tick: attempts > max → should mark degraded.
	w.mu.Lock()
	w.nextRetry["app"] = time.Now().Add(-time.Second)
	w.mu.Unlock()
	w.runOnce()

	updates := st.statusUpdates
	if len(updates) == 0 || updates[len(updates)-1].Status != "degraded" {
		t.Errorf("expected final status=degraded, got %v", updates)
	}
}

func TestWatchdog_ResetsAttemptsOnSuccess(t *testing.T) {
	mgr := &fakeManager{entries: []*process.ProcessInfo{
		{Slug: "app", Status: process.StatusCrashed},
	}}
	st := &fakeStore{
		app:         &db.App{ID: 1, Slug: "app", Status: "crashed"},
		deployments: []*db.Deployment{{BundleDir: "/bundles/v1"}},
	}
	var callCount int32
	w := newTestWatcher(Config{RestartMaxAttempts: 5}, mgr, newFakeProxy(), st,
		func(slug, bundleDir string) error {
			n := atomic.AddInt32(&callCount, 1)
			if n < 2 {
				return fmt.Errorf("fail once")
			}
			return nil
		})

	// First tick: fail → attempts=1.
	w.runOnce()
	w.mu.Lock()
	if w.attempts["app"] != 1 {
		w.mu.Unlock()
		t.Fatalf("expected attempts=1 after failure, got %d", w.attempts["app"])
	}
	w.nextRetry["app"] = time.Now().Add(-time.Second)
	w.mu.Unlock()

	// Second tick: succeed → attempts key deleted (zero value).
	w.runOnce()
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.attempts["app"] != 0 {
		t.Errorf("expected attempts reset to 0 after success, got %d", w.attempts["app"])
	}
}

// --- hibernation tests ---

func TestHibernation_StopsIdleApp(t *testing.T) {
	mgr := &fakeManager{entries: []*process.ProcessInfo{
		{Slug: "app", Status: process.StatusRunning},
	}}
	prx := newFakeProxy()
	prx.seen["app"] = time.Now().Add(-2 * time.Hour) // idle for 2h

	st := &fakeStore{
		app: &db.App{
			ID:        1,
			Slug:      "app",
			Status:    "running",
			UpdatedAt: time.Now().Add(-3 * time.Hour),
		},
	}
	w := newTestWatcher(Config{HibernateTimeout: 30 * time.Minute, RestartMaxAttempts: 5},
		mgr, prx, st, func(slug, dir string) error { return nil })

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
}

func TestHibernation_RespectsPerAppDisable(t *testing.T) {
	mgr := &fakeManager{entries: []*process.ProcessInfo{
		{Slug: "app", Status: process.StatusRunning},
	}}
	prx := newFakeProxy()
	prx.seen["app"] = time.Now().Add(-2 * time.Hour)

	zero := 0
	st := &fakeStore{
		app: &db.App{
			ID:                      1,
			Slug:                    "app",
			Status:                  "running",
			HibernateTimeoutMinutes: &zero, // 0 = disabled for this app
			UpdatedAt:               time.Now().Add(-3 * time.Hour),
		},
	}
	w := newTestWatcher(Config{HibernateTimeout: 30 * time.Minute, RestartMaxAttempts: 5},
		mgr, prx, st, func(slug, dir string) error { return nil })

	w.runOnce()

	if len(mgr.stopped) > 0 {
		t.Errorf("expected no stop (per-app disabled), got %v", mgr.stopped)
	}
}

func TestHibernation_RespectsPerAppCustomTimeout(t *testing.T) {
	mgr := &fakeManager{entries: []*process.ProcessInfo{
		{Slug: "app", Status: process.StatusRunning},
	}}
	prx := newFakeProxy()
	prx.seen["app"] = time.Now().Add(-20 * time.Minute) // idle 20m > custom 10m

	tenMin := 10
	st := &fakeStore{
		app: &db.App{
			ID:                      1,
			Slug:                    "app",
			Status:                  "running",
			HibernateTimeoutMinutes: &tenMin,
			UpdatedAt:               time.Now().Add(-30 * time.Minute),
		},
	}
	w := newTestWatcher(Config{HibernateTimeout: 30 * time.Minute, RestartMaxAttempts: 5},
		mgr, prx, st, func(slug, dir string) error { return nil })

	w.runOnce()

	if len(mgr.stopped) == 0 {
		t.Error("expected app stopped (custom 10m timeout exceeded)")
	}
}

func TestHibernation_GloballyDisabled(t *testing.T) {
	mgr := &fakeManager{entries: []*process.ProcessInfo{
		{Slug: "app", Status: process.StatusRunning},
	}}
	prx := newFakeProxy()
	prx.seen["app"] = time.Now().Add(-2 * time.Hour)

	st := &fakeStore{
		app: &db.App{ID: 1, Slug: "app", Status: "running", UpdatedAt: time.Now().Add(-3 * time.Hour)},
	}
	w := newTestWatcher(Config{HibernateTimeout: 0, RestartMaxAttempts: 5},
		mgr, prx, st, func(slug, dir string) error { return nil })

	w.runOnce()

	if len(mgr.stopped) > 0 {
		t.Errorf("expected no stop (globally disabled), got %v", mgr.stopped)
	}
}

// --- wake-on-request tests ---

func TestWake_TriggeredOnMiss(t *testing.T) {
	prx := newFakeProxy()
	st := &fakeStore{
		app:         &db.App{ID: 1, Slug: "app", Status: "hibernated"},
		deployments: []*db.Deployment{{BundleDir: "/bundles/v1"}},
	}
	var deployed []string
	var mu sync.Mutex
	w := newTestWatcher(Config{RestartMaxAttempts: 5}, &fakeManager{}, prx, st,
		func(slug, bundleDir string) error {
			mu.Lock()
			deployed = append(deployed, slug)
			mu.Unlock()
			return nil
		})

	w.OnMiss("app")
	time.Sleep(50 * time.Millisecond)

	mu.Lock()
	defer mu.Unlock()
	if len(deployed) != 1 || deployed[0] != "app" {
		t.Errorf("expected deployFn('app') called once, got %v", deployed)
	}
}

func TestWake_NoConcurrentWakes(t *testing.T) {
	prx := newFakeProxy()
	st := &fakeStore{
		app:         &db.App{ID: 1, Slug: "app", Status: "hibernated"},
		deployments: []*db.Deployment{{BundleDir: "/bundles/v1"}},
	}
	var deployCount int32
	w := newTestWatcher(Config{RestartMaxAttempts: 5}, &fakeManager{}, prx, st,
		func(slug, bundleDir string) error {
			atomic.AddInt32(&deployCount, 1)
			time.Sleep(30 * time.Millisecond) // slow to create race window
			return nil
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
		{Slug: "app", Status: process.StatusRunning},
	}}
	prx := newFakeProxy()
	prx.seen["app"] = time.Now().Add(-5 * time.Minute) // recently active, under timeout

	st := &fakeStore{
		app: &db.App{
			ID:        1,
			Slug:      "app",
			Status:    "running",
			UpdatedAt: time.Now().Add(-10 * time.Minute),
		},
	}
	w := newTestWatcher(Config{HibernateTimeout: 30 * time.Minute, RestartMaxAttempts: 5},
		mgr, prx, st, func(slug, dir string) error { return nil })

	w.runOnce()

	if len(mgr.stopped) > 0 {
		t.Errorf("expected no stop (app recently active), got %v", mgr.stopped)
	}
}

func TestWake_NonHibernatedAppNotRedeployed(t *testing.T) {
	prx := newFakeProxy()
	st := &fakeStore{
		app:         &db.App{ID: 1, Slug: "app", Status: "running"}, // not hibernated
		deployments: []*db.Deployment{{BundleDir: "/bundles/v1"}},
	}
	var deployCount int32
	w := newTestWatcher(Config{RestartMaxAttempts: 5}, &fakeManager{}, prx, st,
		func(slug, bundleDir string) error {
			atomic.AddInt32(&deployCount, 1)
			return nil
		})

	w.OnMiss("app")
	time.Sleep(50 * time.Millisecond)

	if n := atomic.LoadInt32(&deployCount); n != 0 {
		t.Errorf("expected 0 deploys for non-hibernated app, got %d", n)
	}
}
