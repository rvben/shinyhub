package lifecycle

import (
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rvben/shinyhub/internal/config"
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

	// suspendFreed/suspendErr script Suspend; suspendCalls records invocations.
	// Default zero value (false, nil) makes Suspend report "not freed", so the
	// watcher falls back to Stop - preserving existing tests' behaviour.
	suspendFreed bool
	suspendErr   error
	suspendCalls int

	// stoppedReplicas records every StopReplica(slug, index) call (GC eviction).
	stoppedReplicas []replicaKey

	// logTail is returned verbatim by LogTail (the captured crash diagnostic).
	logTail string

	// lastExit scripts LastExit(slug,index): the most recent exit verdict per
	// replica, used to surface an OOM-kill reason.
	lastExit map[replicaKey]process.ExitVerdict
}

func (f *fakeManager) LogTail(_ string, _, _ int) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.logTail
}

func (f *fakeManager) LastExit(slug string, index int) (process.ExitVerdict, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	v, ok := f.lastExit[replicaKey{slug, index}]
	return v, ok
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

func (f *fakeManager) Suspend(_ string) (bool, error) {
	f.mu.Lock()
	f.suspendCalls++
	freed, err := f.suspendFreed, f.suspendErr
	f.mu.Unlock()
	return freed, err
}

func (f *fakeManager) StopReplica(slug string, index int) error {
	f.mu.Lock()
	f.stoppedReplicas = append(f.stoppedReplicas, replicaKey{slug, index})
	f.mu.Unlock()
	return nil
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
func (f *fakeProxy) SetPoolAppID(_ string, _ int64)                               {}
func (f *fakeProxy) SetPoolIdentityHeaders(_ string, _ bool)                      {}
func (f *fakeProxy) SetPoolMode(_ string, _ config.WorkerIsolationMode, _, _ int) {}

type fakeStore struct {
	mu                sync.Mutex
	apps              map[string]*db.App
	deployments       []*db.Deployment
	statusUpdates     []db.UpdateAppStatusParams
	appStatus         map[string]string
	upsertedReplicas  []db.UpsertReplicaParams
	replicas          map[int64][]*db.Replica
	suspendedReplicas []db.SuspendedReplica // returned by ListSuspendedReplicas
	upsertErr         error                 // when set, UpsertReplica records the call then returns this
	updateStatusErr   error                 // when set, UpdateAppStatus records the call then returns this
	reapCount         int                   // incremented by each ReapStaleReplicaSessions call

	// hibernateAppCalls tracks every HibernateApp call (slug).
	hibernateAppCalls []string

	// crashReasons records the reason from the most recent MarkAppCrashed per
	// slug; cleared by any non-crashed UpdateAppStatus.
	crashReasons map[string]string

	// auditEvents records every LogAuditEvent call so tests can assert the
	// crash audit trail.
	auditEvents []db.AuditEventParams

	// forceHibernatedList, when non-nil, is returned verbatim by
	// ListHibernatedApps regardless of per-app status (drives the
	// snapshot-vs-claim race in warm-restore tests).
	forceHibernatedList []*db.App

	// listReplicasCalls counts how many times ListReplicas has been called.
	listReplicasCalls int

	// listReconcilableAppsCalls counts how many times ListReconcilableApps has
	// been called; used to assert the watchdog tick batches this to one call
	// instead of one per reconcile phase.
	listReconcilableAppsCalls int

	// listReplicasForAppsCalls counts how many times the batch ListReplicasForApps
	// has been called; used to assert the watchdog tick uses a small, bounded
	// number of batched calls instead of one ListReplicas call per app per phase.
	listReplicasForAppsCalls int

	// fleetActive and fleetIdleSinceSec are returned by AppFleetLoad.
	// fleetActive is the sum of other-instance active counts (0 = fleet idle).
	// fleetIdleSinceSec is the seconds since the most recent fleet activity on
	// the DB clock; set to db.NoFleetActivity (math.MaxInt64) to simulate "no
	// peers". Values >= timeout.Seconds() allow hibernation; values < timeout.Seconds()
	// block it.
	fleetActive       int64
	fleetIdleSinceSec int64

	// fleetLastActivity is the Unix epoch returned by AppFleetLastActivity.
	// 0 means no fleet rows; a value > shrinkMoment.Unix() triggers expansion.
	fleetLastActivity int64
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
	delete(f.crashReasons, p.Slug) // any non-crashed transition clears the reason
	f.mu.Unlock()
	return nil
}

// MarkAppCrashed records the crash reason and sets status to "crashed", mirroring
// the real store (a "deleting" app is left untouched).
func (f *fakeStore) MarkAppCrashed(slug, reason string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.appStatus[slug] == "deleting" {
		return db.ErrNotFound
	}
	if app, ok := f.apps[slug]; ok {
		app.Status = "crashed"
	}
	if f.appStatus == nil {
		f.appStatus = make(map[string]string)
	}
	if f.crashReasons == nil {
		f.crashReasons = make(map[string]string)
	}
	f.appStatus[slug] = "crashed"
	f.crashReasons[slug] = reason
	return nil
}

func (f *fakeStore) LogAuditEvent(p db.AuditEventParams) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.auditEvents = append(f.auditEvents, p)
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
	f.listReconcilableAppsCalls++
	var out []*db.App
	for _, app := range f.apps {
		if app.Status == "running" || app.Status == "degraded" {
			out = append(out, app)
		}
	}
	return out, nil
}

// ListReplicasForApps is the batch form of ListReplicas. Unlike ListReplicas
// (which returns the live, mutation-aliased slice held by the fake), this
// returns independent copies of each replica, faithfully modeling a real SQL
// scan where every call produces fresh struct values. A caller that caches an
// early result must not observe a later UpsertReplica mutation without
// re-fetching - exactly the staleness class the real store's ListReplicasForApps
// exposes callers to.
func (f *fakeStore) ListReplicasForApps(appIDs []int64) (map[int64][]*db.Replica, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.listReplicasForAppsCalls++
	if len(appIDs) == 0 {
		return nil, nil
	}
	out := make(map[int64][]*db.Replica, len(appIDs))
	for _, id := range appIDs {
		reps := f.replicas[id]
		if len(reps) == 0 {
			continue
		}
		cp := make([]*db.Replica, len(reps))
		for i, r := range reps {
			rc := *r
			cp[i] = &rc
		}
		out[id] = cp
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
	f.listReplicasCalls++
	return f.replicas[appID], nil
}
func (f *fakeStore) ListSuspendedReplicas() ([]db.SuspendedReplica, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.suspendedReplicas, nil
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

// AppFleetLastActivity returns the pre-configured fleetLastActivity epoch.
// 0 means no fleet rows; a value > shrinkMoment.Unix() triggers expansion.
func (f *fakeStore) AppFleetLastActivity(_ int64, _ int64, _ string) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.fleetLastActivity, nil
}

// ListWarmShrunkApps returns apps in the store whose replicas include at least
// one with desired_state='warm' and whose app status is 'running' or 'degraded'.
func (f *fakeStore) ListWarmShrunkApps() ([]*db.App, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []*db.App
	for _, app := range f.apps {
		if app.Status != "running" && app.Status != "degraded" {
			continue
		}
		for _, r := range f.replicas[app.ID] {
			if r.DesiredState == db.ReplicaDesiredWarm {
				out = append(out, app)
				break
			}
		}
	}
	return out, nil
}

func (f *fakeStore) ListHibernatedApps() ([]*db.App, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	// forceHibernatedList overrides the status filter so a test can drive a
	// snapshot-vs-claim race (an app already woken off "hibernated" between the
	// list and the BeginWake CAS).
	if f.forceHibernatedList != nil {
		return f.forceHibernatedList, nil
	}
	var out []*db.App
	for slug, app := range f.apps {
		if f.appStatus[slug] == "hibernated" {
			out = append(out, app)
		}
	}
	return out, nil
}

// newTestWatcher builds a Watcher with fakes. Tests in the same package can
// call runOnce() directly without starting the background goroutine.
func newTestWatcher(cfg Config, mgr *fakeManager, prx *fakeProxy, st *fakeStore,
	deployFn func(slug, bundleDir string, index int) (*deploy.Result, error)) *Watcher {
	return &Watcher{
		cfg:           cfg,
		mgr:           mgr,
		prx:           prx,
		store:         st,
		deploy:        deployFn,
		attempts:      make(map[replicaKey]int),
		nextRetry:     make(map[replicaKey]time.Time),
		crashCount:    make(map[replicaKey]int),
		lastCrash:     make(map[replicaKey]time.Time),
		driving:       make(map[string]bool),
		expandingWarm: make(map[string]bool),
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

// TestRunOnce_BatchesReconcileQueries proves the watchdog tick issues a
// bounded, batched number of reconcile queries per tick instead of the old
// pattern: two ListReconcilableApps calls (one from reconcileReplicas, one
// from reconcileStatuses) plus one ListReplicas call per app per phase. With 3
// reconcilable apps the old pattern cost 2 ListReconcilableApps + 6 ListReplicas
// calls; the batched path costs exactly 1 ListReconcilableApps call and at most
// 2 batched ListReplicasForApps calls (one per reconcile phase), with the
// per-app ListReplicas path unused.
func TestRunOnce_BatchesReconcileQueries(t *testing.T) {
	mgr := &fakeManager{entries: []*process.ProcessInfo{
		{Slug: "a", Index: 0, Status: process.StatusRunning},
		{Slug: "b", Index: 0, Status: process.StatusRunning},
		{Slug: "c", Index: 0, Status: process.StatusRunning},
	}}
	st := newFakeStore(
		map[string]*db.App{
			"a": {ID: 1, Slug: "a", Status: "running", Replicas: 1},
			"b": {ID: 2, Slug: "b", Status: "running", Replicas: 1},
			"c": {ID: 3, Slug: "c", Status: "running", Replicas: 1},
		},
		[]*db.Deployment{{BundleDir: "/bundles/v1"}},
	)
	st.replicas = map[int64][]*db.Replica{
		1: {{AppID: 1, Index: 0, Status: db.ReplicaStatusRunning, DesiredState: "running"}},
		2: {{AppID: 2, Index: 0, Status: db.ReplicaStatusRunning, DesiredState: "running"}},
		3: {{AppID: 3, Index: 0, Status: db.ReplicaStatusRunning, DesiredState: "running"}},
	}
	w := newTestWatcher(Config{RestartMaxAttempts: 5}, mgr, newFakeProxy(), st,
		func(slug, dir string, idx int) (*deploy.Result, error) { return &deploy.Result{}, nil })

	w.runOnce()

	st.mu.Lock()
	defer st.mu.Unlock()
	if st.listReconcilableAppsCalls != 1 {
		t.Errorf("ListReconcilableApps called %d times per tick, want exactly 1 (was 2: reconcileReplicas + reconcileStatuses each called it separately)", st.listReconcilableAppsCalls)
	}
	if st.listReplicasCalls != 0 {
		t.Errorf("expected the per-app ListReplicas call to no longer be used by the reconcile path, got %d calls", st.listReplicasCalls)
	}
	if st.listReplicasForAppsCalls == 0 {
		t.Fatal("expected the batched ListReplicasForApps to be used")
	}
	if st.listReplicasForAppsCalls > 2 {
		t.Errorf("ListReplicasForApps called %d times for 3 apps in one tick, want <= 2 (batched once per reconcile phase, not once per app)", st.listReplicasForAppsCalls)
	}
}

// TestRunOnce_ReconcileStatusesSeesSameTickReplicaRestart proves that batching
// the reconcile queries does not go stale: reconcileStatuses must observe a
// replica that reconcileReplicas itself restarted earlier in the same tick, not
// a pre-restart snapshot. Only reconcileReplicas can drive replica index 1 here
// (the process manager has no entry for it at all, so the crash never routes
// through the top-loop's handleCrashed path) - this is what would catch a naive
// single up-front replica fetch shared across both reconcile phases. The fake's
// ListReplicasForApps deliberately returns independent copies (like a real SQL
// scan) rather than the live-mutating aliases ListReplicas returns, so a stale
// reuse is actually observable here rather than masked by pointer aliasing.
func TestRunOnce_ReconcileStatusesSeesSameTickReplicaRestart(t *testing.T) {
	mgr := &fakeManager{entries: []*process.ProcessInfo{
		{Slug: "myapp", Index: 0, Status: process.StatusRunning},
	}}
	st := newFakeStore(
		map[string]*db.App{"myapp": {ID: 1, Slug: "myapp", Status: "degraded", Replicas: 2}},
		[]*db.Deployment{{BundleDir: "/bundles/v1"}},
	)
	pid0, port0 := 10, 20010
	st.replicas = map[int64][]*db.Replica{
		1: {
			{AppID: 1, Index: 0, PID: &pid0, Port: &port0, Status: db.ReplicaStatusRunning, DesiredState: "running"},
			{AppID: 1, Index: 1, Status: "crashed", DesiredState: "running"}, // no mgr entry: only reconcileReplicas can restart this
		},
	}
	w := newTestWatcher(Config{RestartMaxAttempts: 5}, mgr, newFakeProxy(), st,
		func(slug, bundleDir string, idx int) (*deploy.Result, error) {
			return &deploy.Result{Index: idx, PID: 11, Port: 20011}, nil
		})

	w.runOnce()

	if got := st.appStatus["myapp"]; got != "running" {
		t.Fatalf("status = %q, want running: reconcileStatuses must observe reconcileReplicas's same-tick restart of index 1, not a stale pre-restart snapshot", got)
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
	// The single replica is down and its restart budget is spent: the app is fully
	// down with no recovery in flight, so it is "crashed" (terminal, restartable),
	// not "degraded" (partially up, still self-healing).
	if st.appStatus["app"] != "crashed" {
		t.Errorf("expected status=crashed, got %q", st.appStatus["app"])
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

// TestHibernation_StopFailureDoesNotPersistHibernatedStatus proves that when
// mgr.Stop fails during single-node hibernation, the watcher does NOT persist
// "hibernated" (app status) or "stopped" (replica rows). Asserting a terminal
// hibernated state that isn't real would strand a live manager entry and could
// later trip ErrReplicaAlreadyRunning on wake; leaving the real status
// untouched lets the next tick retry the stop.
func TestHibernation_StopFailureDoesNotPersistHibernatedStatus(t *testing.T) {
	mgr := &fakeManager{
		entries: []*process.ProcessInfo{
			{Slug: "app", Index: 0, Status: process.StatusRunning},
		},
		stopErr: fmt.Errorf("kill refused"),
	}
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
	// The replica is genuinely still running (mgr.Stop below is refused, so the
	// real OS process never dies): seed the same pre-existing "running" row a
	// live app would have, so reconcileStatuses - which runs later in the same
	// tick - reconciles against the true state rather than an empty snapshot.
	st.replicas = map[int64][]*db.Replica{
		1: {{AppID: 1, Index: 0, Status: db.ReplicaStatusRunning, DesiredState: "running"}},
	}
	w := newTestWatcher(Config{HibernateTimeout: 30 * time.Minute, RestartMaxAttempts: 5},
		mgr, prx, st, func(slug, dir string, idx int) (*deploy.Result, error) { return &deploy.Result{}, nil })

	w.runOnce()

	if len(mgr.stopped) == 0 || mgr.stopped[0] != "app" {
		t.Fatalf("expected manager.Stop('app') to have been attempted, got %v", mgr.stopped)
	}
	for _, s := range st.statusUpdates {
		if s.Status == "hibernated" {
			t.Errorf("expected no hibernated status update after a failed Stop, got %v", st.statusUpdates)
		}
	}
	if got := st.appStatus["app"]; got != "running" {
		t.Errorf("app status = %q, want running (unchanged, so the next tick retries the stop)", got)
	}
	st.mu.Lock()
	defer st.mu.Unlock()
	for _, ur := range st.upsertedReplicas {
		if ur.Status == "stopped" {
			t.Errorf("expected no replica persisted as stopped after a failed Stop, got %+v", ur)
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

func TestWatcher_AllReplicasCrashed(t *testing.T) {
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
	// Both replicas are down with their restart budgets spent: the app is fully
	// down (crashed), not partially up (degraded).
	if st.appStatus["demo"] != "crashed" {
		t.Fatalf("expected crashed after all replicas exhaust retries, got %q", st.appStatus["demo"])
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

// TestWakingReconcile_SingleNodeDrivesStuckWake asserts that a single runOnce
// tick with Clustered:false still drives apps left in the 'waking' status.
// This is the safety net for wakes interrupted by a process handoff (ZDT
// upgrade): the old process may have died mid-driveWakingApp; BeginWake's CAS
// leaves the app in 'waking'; the successor process must recover it on its
// first tick even though it is not clustered.
//
// Pre-fix this test FAILS: the waking-app reconcile block is gated
// `if w.cfg.Clustered`, so single-node skips it and the app stays stuck.
func TestWakingReconcile_SingleNodeDrivesStuckWake(t *testing.T) {
	prx := newFakeProxy()
	st := newFakeStore(
		map[string]*db.App{"stuck": {ID: 1, Slug: "stuck", Status: "waking", Replicas: 1}},
		[]*db.Deployment{{BundleDir: "/bundles/v1"}},
	)

	var deployed []string
	var mu sync.Mutex
	w := newTestWatcher(Config{
		RestartMaxAttempts: 5,
		Clustered:          false, // single-node
	}, &fakeManager{}, prx, st,
		func(slug, bundleDir string, idx int) (*deploy.Result, error) {
			mu.Lock()
			deployed = append(deployed, slug)
			mu.Unlock()
			return &deploy.Result{Index: idx, PID: 77, Port: 20077}, nil
		})
	// The watcher is the owner (isOwner == nil => always-owner).

	w.runOnce()
	waitNotWaking(t, st, "stuck")

	mu.Lock()
	gotDeployed := append([]string(nil), deployed...)
	mu.Unlock()

	if len(gotDeployed) == 0 {
		t.Fatal("single-node runOnce must drive waking apps stuck after a handoff; got 0 deploys (fix: remove Clustered gate on waking-app reconcile)")
	}
	if gotDeployed[0] != "stuck" {
		t.Errorf("expected deploy for 'stuck', got %v", gotDeployed)
	}
	st.mu.Lock()
	status := st.apps["stuck"].Status
	st.mu.Unlock()
	if status != "running" {
		t.Errorf("app status after runOnce = %q, want running", status)
	}
}

// TestWakingReconcile_ClusteredBehaviorUnchanged asserts that clustered mode
// still drives waking apps on runOnce (guards against regression).
func TestWakingReconcile_ClusteredBehaviorUnchanged(t *testing.T) {
	prx := newFakeProxy()
	st := newFakeStore(
		map[string]*db.App{"clust-stuck": {ID: 2, Slug: "clust-stuck", Status: "waking", Replicas: 1}},
		[]*db.Deployment{{BundleDir: "/bundles/v1"}},
	)

	var deployed []string
	var mu sync.Mutex
	w := newTestWatcher(Config{
		RestartMaxAttempts: 5,
		Clustered:          true, // clustered
	}, &fakeManager{}, prx, st,
		func(slug, bundleDir string, idx int) (*deploy.Result, error) {
			mu.Lock()
			deployed = append(deployed, slug)
			mu.Unlock()
			return &deploy.Result{Index: idx, PID: 78, Port: 20078}, nil
		})

	w.runOnce()
	waitNotWaking(t, st, "clust-stuck")

	mu.Lock()
	gotDeployed := append([]string(nil), deployed...)
	mu.Unlock()

	if len(gotDeployed) == 0 {
		t.Fatal("clustered runOnce must drive waking apps")
	}
	if gotDeployed[0] != "clust-stuck" {
		t.Errorf("expected deploy for 'clust-stuck', got %v", gotDeployed)
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
func (m *orderCheckingManager) Suspend(slug string) (bool, error) { return m.inner.Suspend(slug) }
func (m *orderCheckingManager) LastExit(slug string, index int) (process.ExitVerdict, bool) {
	return m.inner.LastExit(slug, index)
}
func (m *orderCheckingManager) LogTail(slug string, index, n int) string {
	return m.inner.LogTail(slug, index, n)
}
func (m *orderCheckingManager) StopReplica(slug string, index int) error {
	return m.inner.StopReplica(slug, index)
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
// The manager has 3 running replicas (> floor=2) so the floor guard passes.
func TestHandleIdle_ClusteredWarmShrinkReplacesHibernate(t *testing.T) {
	mgr := &fakeManager{entries: []*process.ProcessInfo{
		{Slug: "cwarm", Index: 0, Status: process.StatusRunning},
		{Slug: "cwarm", Index: 1, Status: process.StatusRunning},
		{Slug: "cwarm", Index: 2, Status: process.StatusRunning},
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

// --- warm-expand tests ---

// warmExpandSetup builds a watcher and store with one warm-shrunk running app.
// shrinkTime is the UpdatedAt on the warm replica row (the shrink moment).
func warmExpandSetup(t *testing.T, shrinkTime time.Time) (*fakeManager, *fakeProxy, *fakeStore, *Watcher) {
	t.Helper()
	mgr := &fakeManager{entries: []*process.ProcessInfo{
		{Slug: "warm", Index: 0, Status: process.StatusRunning},
	}}
	prx := newFakeProxy()
	st := newFakeStore(
		map[string]*db.App{"warm": {
			ID:              1,
			Slug:            "warm",
			Status:          "running",
			Replicas:        2,
			MinWarmReplicas: 1,
			UpdatedAt:       shrinkTime.Add(-10 * time.Minute),
		}},
		[]*db.Deployment{{BundleDir: "/bundles/v1"}},
	)
	// Warm replica at index 1 parked at shrinkTime.
	pid0 := 100
	port0 := 20100
	st.replicas = map[int64][]*db.Replica{
		1: {
			{AppID: 1, Index: 0, Status: "running", DesiredState: "running", PID: &pid0, Port: &port0},
			{AppID: 1, Index: 1, Status: "stopped", DesiredState: db.ReplicaDesiredWarm, UpdatedAt: shrinkTime},
		},
	}
	w := newTestWatcher(Config{HibernateTimeout: 30 * time.Minute, RestartMaxAttempts: 5},
		mgr, prx, st, func(slug, dir string, idx int) (*deploy.Result, error) { return &deploy.Result{}, nil })
	return mgr, prx, st, w
}

// TestHandleWarmExpand_ActivityAfterShrinkExpands (single-node): LastSeen after
// the shrink moment must trigger warmExpand with the correct slug.
func TestHandleWarmExpand_ActivityAfterShrinkExpands(t *testing.T) {
	shrinkTime := time.Now().Add(-5 * time.Minute)
	_, prx, _, w := warmExpandSetup(t, shrinkTime)

	// Proxy saw traffic 2 minutes after the shrink.
	prx.seen["warm"] = shrinkTime.Add(2 * time.Minute)

	var expandCalls []string
	var mu sync.Mutex
	w.SetWarmOps(
		func(slug string, floor int) (bool, error) { return false, nil },
		func(slug string) (bool, error) {
			mu.Lock()
			expandCalls = append(expandCalls, slug)
			mu.Unlock()
			return true, nil
		},
	)

	w.runOnce()

	mu.Lock()
	calls := append([]string(nil), expandCalls...)
	mu.Unlock()
	if len(calls) != 1 || calls[0] != "warm" {
		t.Errorf("expected warmExpand called once with 'warm', got %v", calls)
	}
}

// TestHandleWarmExpand_NoActivityNoExpand: LastSeen before shrink moment (or
// equal) must NOT trigger warmExpand.
func TestHandleWarmExpand_NoActivityNoExpand(t *testing.T) {
	shrinkTime := time.Now().Add(-5 * time.Minute)
	_, prx, _, w := warmExpandSetup(t, shrinkTime)

	// Proxy last saw traffic 2 minutes BEFORE the shrink.
	prx.seen["warm"] = shrinkTime.Add(-2 * time.Minute)

	var expanded bool
	w.SetWarmOps(
		func(slug string, floor int) (bool, error) { return false, nil },
		func(slug string) (bool, error) { expanded = true; return false, nil },
	)

	w.runOnce()

	if expanded {
		t.Error("warmExpand must NOT be called when LastSeen is before the shrink moment")
	}
}

// TestHandleWarmExpand_RestartZeroLastSeen: LastSeen zero value (server restart,
// no traffic yet) must NOT trigger warmExpand.
func TestHandleWarmExpand_RestartZeroLastSeen(t *testing.T) {
	shrinkTime := time.Now().Add(-5 * time.Minute)
	_, _, _, w := warmExpandSetup(t, shrinkTime)
	// prx.seen["warm"] is zero (never set after restart).

	var expanded bool
	w.SetWarmOps(
		func(slug string, floor int) (bool, error) { return false, nil },
		func(slug string) (bool, error) { expanded = true; return false, nil },
	)

	w.runOnce()

	if expanded {
		t.Error("warmExpand must NOT be called when LastSeen is zero (server restart)")
	}
}

// TestHandleWarmExpand_NilOpsSkips: when warmExpand is nil, no store calls
// should be made for warm expansion (method exits early).
func TestHandleWarmExpand_NilOpsSkips(t *testing.T) {
	shrinkTime := time.Now().Add(-5 * time.Minute)
	_, prx, st, w := warmExpandSetup(t, shrinkTime)

	// Proxy has fresh activity; would expand if ops were wired.
	prx.seen["warm"] = shrinkTime.Add(time.Minute)

	// warmExpand is nil; do not call SetWarmOps.
	w.runOnce()

	// Verify no UpsertReplica was called as part of expansion (only baseline
	// reconcile calls are expected, none for the expand path itself).
	// The key invariant: no panic and the app remains unchanged.
	st.mu.Lock()
	defer st.mu.Unlock()
	for _, ur := range st.upsertedReplicas {
		if ur.DesiredState == "running" && ur.Index == 1 {
			t.Error("unexpected replica upsert for warm-expand when warmExpand is nil")
		}
	}
}

// TestHandleWarmExpand_ClusteredFleetActivityExpands: clustered mode with fleet
// last_activity epoch after the shrink moment must trigger warmExpand.
func TestHandleWarmExpand_ClusteredFleetActivityExpands(t *testing.T) {
	shrinkTime := time.Now().Add(-5 * time.Minute)
	_, _, st, w := warmExpandSetup(t, shrinkTime)
	w.cfg.Clustered = true
	w.cfg.InstanceID = "self"

	// Fleet last_activity is 2 minutes after the shrink moment.
	st.fleetLastActivity = shrinkTime.Add(2 * time.Minute).Unix()

	var expandCalls []string
	var mu sync.Mutex
	w.SetWarmOps(
		func(slug string, floor int) (bool, error) { return false, nil },
		func(slug string) (bool, error) {
			mu.Lock()
			expandCalls = append(expandCalls, slug)
			mu.Unlock()
			return true, nil
		},
	)

	w.runOnce()

	mu.Lock()
	calls := append([]string(nil), expandCalls...)
	mu.Unlock()
	if len(calls) != 1 || calls[0] != "warm" {
		t.Errorf("clustered: expected warmExpand called once with 'warm', got %v", calls)
	}
}

// TestHandleWarmExpand_ClusteredNoFleetActivityNoExpand: clustered mode with
// fleet last_activity epoch at or before the shrink moment must NOT expand.
func TestHandleWarmExpand_ClusteredNoFleetActivityNoExpand(t *testing.T) {
	shrinkTime := time.Now().Add(-5 * time.Minute)
	_, _, st, w := warmExpandSetup(t, shrinkTime)
	w.cfg.Clustered = true
	w.cfg.InstanceID = "self"

	// Fleet last_activity was 2 minutes BEFORE the shrink moment.
	st.fleetLastActivity = shrinkTime.Add(-2 * time.Minute).Unix()

	var expanded bool
	w.SetWarmOps(
		func(slug string, floor int) (bool, error) { return false, nil },
		func(slug string) (bool, error) { expanded = true; return false, nil },
	)

	w.runOnce()

	if expanded {
		t.Error("clustered: warmExpand must NOT be called when fleet activity is before shrink moment")
	}
}

// TestHandleWarmExpand_RunOnceWiringExpands: integration through runOnce confirms
// that a warm-shrunk app with fresh proxy activity results in warmExpand being
// called (tests the wiring from runOnce into handleWarmExpand).
func TestHandleWarmExpand_RunOnceWiringExpands(t *testing.T) {
	shrinkTime := time.Now().Add(-10 * time.Minute)
	_, prx, _, w := warmExpandSetup(t, shrinkTime)

	// Traffic arrived 3 minutes after shrink.
	prx.seen["warm"] = shrinkTime.Add(3 * time.Minute)

	var expandCount int32
	w.SetWarmOps(
		func(slug string, floor int) (bool, error) { return false, nil },
		func(slug string) (bool, error) {
			atomic.AddInt32(&expandCount, 1)
			return true, nil
		},
	)

	w.runOnce()

	if n := atomic.LoadInt32(&expandCount); n != 1 {
		t.Errorf("runOnce: expected warmExpand called once, got %d", n)
	}
}

// --- WakeTrigger warm-expand path tests ---

// TestWakeTrigger_RunningAppWithWarmRows_CallsWarmExpand verifies that
// WakeTrigger on a running app that has warm-parked replicas calls warmExpand
// once instead of attempting a hibernate->waking wake flow.
func TestWakeTrigger_RunningAppWithWarmRows_CallsWarmExpand(t *testing.T) {
	prx := newFakeProxy()
	st := newFakeStore(
		map[string]*db.App{"warm": {ID: 1, Slug: "warm", Status: "running", Replicas: 2, MinWarmReplicas: 1}},
		[]*db.Deployment{{BundleDir: "/tmp/warm"}},
	)
	// One warm-parked replica.
	st.replicas = map[int64][]*db.Replica{
		1: {
			{AppID: 1, Index: 0, Status: "running", DesiredState: "running"},
			{AppID: 1, Index: 1, Status: "stopped", DesiredState: db.ReplicaDesiredWarm},
		},
	}

	var expandCalls []string
	var mu sync.Mutex
	w := newTestWatcher(Config{RestartMaxAttempts: 5}, &fakeManager{}, prx, st, nil)
	w.SetWarmOps(
		func(slug string, floor int) (bool, error) { return false, nil },
		func(slug string) (bool, error) {
			mu.Lock()
			expandCalls = append(expandCalls, slug)
			mu.Unlock()
			return true, nil
		},
	)

	w.WakeTrigger("warm")

	// WakeTrigger may call warmExpand synchronously or via a goroutine.
	// Give it a short window.
	deadline := time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(deadline) {
		mu.Lock()
		n := len(expandCalls)
		mu.Unlock()
		if n > 0 {
			break
		}
		time.Sleep(time.Millisecond)
	}

	mu.Lock()
	calls := append([]string(nil), expandCalls...)
	mu.Unlock()
	if len(calls) != 1 || calls[0] != "warm" {
		t.Errorf("expected warmExpand called once with 'warm', got %v", calls)
	}

	// The BeginWake CAS must NOT have advanced the status (app is running, not hibernated).
	st.mu.Lock()
	status := st.apps["warm"].Status
	st.mu.Unlock()
	if status != "running" {
		t.Errorf("WakeTrigger on running app must not change status; got %q", status)
	}
}

// TestWakeTrigger_RunningAppWithoutWarmRows_DoesNothing verifies that
// WakeTrigger on a running app with no warm-parked replicas is a silent no-op.
func TestWakeTrigger_RunningAppWithoutWarmRows_DoesNothing(t *testing.T) {
	prx := newFakeProxy()
	st := newFakeStore(
		map[string]*db.App{"app": {ID: 1, Slug: "app", Status: "running", Replicas: 2}},
		[]*db.Deployment{{BundleDir: "/tmp/app"}},
	)
	// Both replicas fully running (no warm rows).
	st.replicas = map[int64][]*db.Replica{
		1: {
			{AppID: 1, Index: 0, Status: "running", DesiredState: "running"},
			{AppID: 1, Index: 1, Status: "running", DesiredState: "running"},
		},
	}

	var expandCalled bool
	w := newTestWatcher(Config{RestartMaxAttempts: 5}, &fakeManager{}, prx, st, nil)
	w.SetWarmOps(
		func(slug string, floor int) (bool, error) { return false, nil },
		func(slug string) (bool, error) { expandCalled = true; return false, nil },
	)

	w.WakeTrigger("app")
	// Give any async paths time to run.
	time.Sleep(20 * time.Millisecond)

	if expandCalled {
		t.Error("warmExpand must not be called when no warm-parked replicas exist")
	}
	st.mu.Lock()
	status := st.apps["app"].Status
	st.mu.Unlock()
	if status != "running" {
		t.Errorf("WakeTrigger on fully-running app must not change status; got %q", status)
	}
}

// TestWakeTrigger_HibernatedApp_PerformsWakeFlowNotWarmExpand verifies that
// WakeTrigger on a hibernated app performs the existing hibernated->waking CAS
// and does NOT call warmExpand. This pins the pre-existing wake behavior.
func TestWakeTrigger_HibernatedApp_PerformsWakeFlowNotWarmExpand(t *testing.T) {
	prx := newFakeProxy()
	st := newFakeStore(
		map[string]*db.App{"app": {ID: 1, Slug: "app", Status: "hibernated", Replicas: 1}},
		[]*db.Deployment{{BundleDir: "/bundles/v1"}},
	)

	var expandCalled bool
	var deployCount int32
	w := newTestWatcher(Config{RestartMaxAttempts: 5}, &fakeManager{}, prx, st,
		func(slug, dir string, idx int) (*deploy.Result, error) {
			atomic.AddInt32(&deployCount, 1)
			return &deploy.Result{Index: idx, PID: 42, Port: 20042}, nil
		})
	w.SetWarmOps(
		func(slug string, floor int) (bool, error) { return false, nil },
		func(slug string) (bool, error) { expandCalled = true; return false, nil },
	)

	w.WakeTrigger("app")
	waitNotWaking(t, st, "app")

	if expandCalled {
		t.Error("warmExpand must NOT be called for a hibernated app wake — use the existing wake flow")
	}
	// BeginWake CAS must have been attempted (status transitions: hibernated->waking->running).
	st.mu.Lock()
	finalStatus := st.apps["app"].Status
	st.mu.Unlock()
	if finalStatus != "running" {
		t.Errorf("hibernated app must reach 'running' after WakeTrigger; got %q", finalStatus)
	}
	if n := atomic.LoadInt32(&deployCount); n == 0 {
		t.Error("expected at least one deploy call for hibernated app wake; got 0")
	}
}

// TestWakeTrigger_BurstOnWarmApp_OnlyOneExpand verifies that a burst of
// concurrent WakeTrigger calls on a warm-shrunk running app results in exactly
// one warmExpand call and bounded store reads: the in-flight guard must
// short-circuit all but the first trigger so N goroutines do not each issue
// GetAppBySlug+ListReplicas and each call warmExpand.
//
// Synchronisation design: the first goroutine to enter warmExpand holds the
// guard and blocks until it receives a signal that all other goroutines have
// finished their WakeTrigger calls (quickly rejected by the guard). Only then
// does warmExpand return. This guarantees the guard window covers the entire
// burst, so no late-arriving goroutine can slip through after the guard clears.
func TestWakeTrigger_BurstOnWarmApp_OnlyOneExpand(t *testing.T) {
	const burst = 20

	prx := newFakeProxy()
	st := newFakeStore(
		map[string]*db.App{"warm": {ID: 1, Slug: "warm", Status: "running", Replicas: 2, MinWarmReplicas: 1}},
		[]*db.Deployment{{BundleDir: "/tmp/warm"}},
	)
	st.replicas = map[int64][]*db.Replica{
		1: {
			{AppID: 1, Index: 0, Status: "running", DesiredState: "running"},
			{AppID: 1, Index: 1, Status: "stopped", DesiredState: db.ReplicaDesiredWarm},
		},
	}

	// othersFinished is a channel buffered for (burst-1) sends; warmExpand
	// reads (burst-1) signals from it before returning, ensuring the guard is
	// held for the full duration of all concurrent WakeTrigger calls.
	othersFinished := make(chan struct{}, burst)

	var expandCalls atomic.Int32
	w := newTestWatcher(Config{RestartMaxAttempts: 5}, &fakeManager{}, prx, st, nil)
	w.SetWarmOps(
		func(slug string, floor int) (bool, error) { return false, nil },
		func(slug string) (bool, error) {
			expandCalls.Add(1)
			// Block until all other goroutines have returned from WakeTrigger
			// (either guard-rejected or this is the only caller). This holds
			// the in-flight guard open across the full burst window.
			for i := 0; i < burst-1; i++ {
				select {
				case <-othersFinished:
				case <-time.After(5 * time.Second):
					// Unblock rather than deadlock; the assertion will catch the error.
					return false, fmt.Errorf("timed out waiting for burst completions")
				}
			}
			return true, nil
		},
	)

	// Barrier gate: hold all goroutines until all are ready, then release
	// simultaneously to maximise guard contention.
	gate := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < burst; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-gate
			w.WakeTrigger("warm")
			// Signal completion to warmExpand (the winning goroutine reads these).
			othersFinished <- struct{}{}
		}()
	}
	close(gate) // start all goroutines simultaneously
	wg.Wait()

	if n := expandCalls.Load(); n != 1 {
		t.Errorf("warmExpand called %d times; want exactly 1 (in-flight guard must debounce burst)", n)
	}

	st.mu.Lock()
	replicaReads := st.listReplicasCalls
	st.mu.Unlock()
	if replicaReads >= burst {
		t.Errorf("ListReplicas called %d times; want < %d (guard must prevent per-trigger store reads)", replicaReads, burst)
	}
}

// --- warm-shrink floor guard tests ---

// TestHandleIdle_AlreadyAtFloorSkipsWarmShrink: an app with MinWarmReplicas=1
// and exactly 1 running process in the manager must NOT trigger warmShrink on
// an idle tick — it is already at its floor.
func TestHandleIdle_AlreadyAtFloorSkipsWarmShrink(t *testing.T) {
	mgr := &fakeManager{entries: []*process.ProcessInfo{
		{Slug: "floor", Index: 0, Status: process.StatusRunning},
	}}
	prx := newFakeProxy()
	prx.seen["floor"] = time.Now().Add(-2 * time.Hour) // idle 2h > 30m timeout

	st := newFakeStore(
		map[string]*db.App{"floor": {
			ID:              1,
			Slug:            "floor",
			Status:          "running",
			Replicas:        1,
			MinWarmReplicas: 1, // floor == running count; already at floor
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
		t.Error("warmShrink must NOT be called when running count equals the floor")
	}
	// BeginHibernate must also NOT be called: the floor guard returns early
	// before reaching the full-hibernate path.
	prx.mu.Lock()
	hibernated := append([]string(nil), prx.hibernated...)
	prx.mu.Unlock()
	if len(hibernated) != 0 {
		t.Errorf("BeginHibernate must NOT be called when already at floor, got %v", hibernated)
	}
}

// TestHandleIdle_AboveFloorCallsWarmShrink: an app with MinWarmReplicas=1 and
// 2 running processes IS above its floor and must invoke warmShrink on an idle
// tick. This is the regression guard for the floor check.
func TestHandleIdle_AboveFloorCallsWarmShrink(t *testing.T) {
	mgr := &fakeManager{entries: []*process.ProcessInfo{
		{Slug: "above", Index: 0, Status: process.StatusRunning},
		{Slug: "above", Index: 1, Status: process.StatusRunning},
	}}
	prx := newFakeProxy()
	prx.seen["above"] = time.Now().Add(-2 * time.Hour)

	st := newFakeStore(
		map[string]*db.App{"above": {
			ID:              1,
			Slug:            "above",
			Status:          "running",
			Replicas:        2,
			MinWarmReplicas: 1, // floor=1, running=2: above floor, should shrink
			UpdatedAt:       time.Now().Add(-3 * time.Hour),
		}},
		nil,
	)

	var shrinkCalled bool
	w := newTestWatcher(Config{HibernateTimeout: 30 * time.Minute, RestartMaxAttempts: 5},
		mgr, prx, st, func(slug, dir string, idx int) (*deploy.Result, error) { return &deploy.Result{}, nil })
	w.SetWarmOps(
		func(slug string, floor int) (bool, error) { shrinkCalled = true; return true, nil },
		func(slug string) (bool, error) { return false, nil },
	)

	w.runOnce()

	if !shrinkCalled {
		t.Error("warmShrink must be called when running count exceeds the floor")
	}
}

// TestHandleIdle_ClusteredAlreadyAtFloorSkipsWarmShrink: same floor guard in
// clustered mode. Fleet is idle, local is idle, but running == floor => skip.
func TestHandleIdle_ClusteredAlreadyAtFloorSkipsWarmShrink(t *testing.T) {
	mgr := &fakeManager{entries: []*process.ProcessInfo{
		{Slug: "cfloor", Index: 0, Status: process.StatusRunning},
	}}
	prx := newFakeProxy()
	prx.seen["cfloor"] = time.Now().Add(-2 * time.Hour)

	st := newFakeStore(
		map[string]*db.App{"cfloor": {
			ID:              42,
			Slug:            "cfloor",
			Status:          "running",
			Replicas:        1,
			MinWarmReplicas: 1,
			UpdatedAt:       time.Now().Add(-3 * time.Hour),
		}},
		nil,
	)
	st.fleetActive = 0
	st.fleetIdleSinceSec = db.NoFleetActivity

	var shrinkCalled bool
	w := newTestWatcher(Config{
		HibernateTimeout:   30 * time.Minute,
		RestartMaxAttempts: 5,
		Clustered:          true,
		InstanceID:         "self",
	}, mgr, prx, st, func(slug, dir string, idx int) (*deploy.Result, error) { return &deploy.Result{}, nil })
	w.SetWarmOps(
		func(slug string, floor int) (bool, error) { shrinkCalled = true; return false, nil },
		func(slug string) (bool, error) { return false, nil },
	)

	w.runOnce()

	if shrinkCalled {
		t.Error("clustered: warmShrink must NOT be called when running count equals the floor")
	}
	// HibernateApp CAS must NOT fire either.
	st.mu.Lock()
	casCalls := append([]string(nil), st.hibernateAppCalls...)
	st.mu.Unlock()
	if len(casCalls) != 0 {
		t.Errorf("clustered: HibernateApp must NOT fire when already at floor, got %v", casCalls)
	}
}
