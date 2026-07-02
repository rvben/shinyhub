package lifecycle

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/rvben/shinyhub/internal/config"
	"github.com/rvben/shinyhub/internal/db"
	"github.com/rvben/shinyhub/internal/deploy"
)

// modeRecordingProxy implements proxyBackend and records every SetPoolMode
// call so tests can assert that the isolation mode was propagated correctly.
// It embeds all no-op stubs for the remaining proxyBackend methods.
type modeRecordingProxy struct {
	mu        sync.Mutex
	poolSizes map[string]int
	poolCaps  map[string]int
	poolModes map[string]config.WorkerIsolationMode

	seen         map[string]time.Time
	deregistered []string
}

func newModeRecordingProxy() *modeRecordingProxy {
	return &modeRecordingProxy{
		poolSizes: make(map[string]int),
		poolCaps:  make(map[string]int),
		poolModes: make(map[string]config.WorkerIsolationMode),
		seen:      make(map[string]time.Time),
	}
}

func (p *modeRecordingProxy) LastSeen(slug string) time.Time {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.seen[slug]
}
func (p *modeRecordingProxy) BeginHibernate(slug string, since time.Time) bool { return false }
func (p *modeRecordingProxy) Deregister(slug string) {
	p.mu.Lock()
	p.deregistered = append(p.deregistered, slug)
	p.mu.Unlock()
}
func (p *modeRecordingProxy) SetPoolSize(slug string, size int) {
	p.mu.Lock()
	p.poolSizes[slug] = size
	p.mu.Unlock()
}
func (p *modeRecordingProxy) SetPoolCap(slug string, max int) {
	p.mu.Lock()
	p.poolCaps[slug] = max
	p.mu.Unlock()
}
func (p *modeRecordingProxy) SetPoolAppID(_ string, _ int64)          {}
func (p *modeRecordingProxy) SetPoolIdentityHeaders(_ string, _ bool) {}
func (p *modeRecordingProxy) SetPoolMode(slug string, mode config.WorkerIsolationMode, _, _ int) {
	p.mu.Lock()
	p.poolModes[slug] = mode
	p.mu.Unlock()
}

func newPoolModeWatcher(cfg Config, prx *modeRecordingProxy, st *fakeStore,
	deployFn func(slug, bundleDir string, index int) (*deploy.Result, error)) *Watcher {
	return &Watcher{
		cfg:           cfg,
		mgr:           &fakeManager{},
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

// TestWake_ElasticPerSession_SkipsReplicaBoot verifies that wakeApp transitions
// an elastic (per_session) hibernated app to running WITHOUT calling the deploy
// function (no replica boot). The proxy pool mode is set to per_session.
func TestWake_ElasticPerSession_SkipsReplicaBoot(t *testing.T) {
	prx := newModeRecordingProxy()
	st := newFakeStore(
		map[string]*db.App{"elastic-app": {
			ID:               1,
			Slug:             "elastic-app",
			Status:           "hibernated",
			Replicas:         2,
			WorkerIsolation:  "per_session",
			WorkerMaxWorkers: 5,
		}},
		[]*db.Deployment{{BundleDir: "/bundles/v1"}},
	)

	var deployCount int
	var deployMu sync.Mutex
	w := newPoolModeWatcher(
		Config{
			RestartMaxAttempts:     5,
			DefaultWorkerIsolation: "multiplex", // fleet default; per-app overrides it
		},
		prx,
		st,
		func(slug, bundleDir string, idx int) (*deploy.Result, error) {
			deployMu.Lock()
			deployCount++
			deployMu.Unlock()
			return &deploy.Result{Index: idx, PID: 99, Port: 20099}, nil
		},
	)

	w.WakeTrigger("elastic-app")
	waitNotWaking(t, st, "elastic-app")

	// The deploy function must NOT have been called: elastic apps have no fixed replicas.
	deployMu.Lock()
	got := deployCount
	deployMu.Unlock()
	if got != 0 {
		t.Errorf("deployFn called %d time(s); elastic wake must skip replica boot", got)
	}

	// The app should now be running (FinishWake committed the CAS).
	st.mu.Lock()
	status := st.apps["elastic-app"].Status
	st.mu.Unlock()
	if status != "running" {
		t.Errorf("app status after elastic wake = %q, want running", status)
	}

	// SetPoolMode must have been called with per_session.
	prx.mu.Lock()
	mode := prx.poolModes["elastic-app"]
	prx.mu.Unlock()
	if mode != config.IsolationPerSession {
		t.Errorf("pool mode after elastic wake = %q, want %q", mode, config.IsolationPerSession)
	}
}

// TestWake_Multiplex_StillBootsReplicas is a regression test verifying that
// the elastic skip does NOT affect multiplex apps: they still call the deployFn
// and persist replica data.
func TestWake_Multiplex_StillBootsReplicas(t *testing.T) {
	prx := newModeRecordingProxy()
	st := newFakeStore(
		map[string]*db.App{"mp-app": {
			ID:       1,
			Slug:     "mp-app",
			Status:   "hibernated",
			Replicas: 1,
			// WorkerIsolation empty = multiplex
		}},
		[]*db.Deployment{{BundleDir: "/bundles/v1"}},
	)

	var deployCount int32
	w := newPoolModeWatcher(
		Config{RestartMaxAttempts: 5},
		prx,
		st,
		func(slug, bundleDir string, idx int) (*deploy.Result, error) {
			deployCount++
			return &deploy.Result{Index: idx, PID: 55, Port: 20055}, nil
		},
	)

	w.WakeTrigger("mp-app")
	waitNotWaking(t, st, "mp-app")

	if deployCount == 0 {
		t.Error("multiplex wake must call deployFn to boot replica")
	}

	st.mu.Lock()
	status := st.apps["mp-app"].Status
	st.mu.Unlock()
	if status != "running" {
		t.Errorf("multiplex app status after wake = %q, want running", status)
	}
}

// TestWake_FleetDefaultElastic_SkipsReplicaBoot verifies that an app whose
// per-app WorkerIsolation field is empty inherits the fleet default. When the
// fleet default is "per_session", driveWakingApp must treat it as elastic:
// no replica is booted, the app transitions to running, and the proxy pool
// mode is set to per_session.
func TestWake_FleetDefaultElastic_SkipsReplicaBoot(t *testing.T) {
	prx := newModeRecordingProxy()
	st := newFakeStore(
		map[string]*db.App{"inherit-app": {
			ID:               10,
			Slug:             "inherit-app",
			Status:           "hibernated",
			Replicas:         2,
			WorkerIsolation:  "", // empty: inherits fleet default
			WorkerMaxWorkers: 5,
		}},
		[]*db.Deployment{{BundleDir: "/bundles/v1"}},
	)

	var deployCount int
	var deployMu sync.Mutex
	w := newPoolModeWatcher(
		Config{
			RestartMaxAttempts:     5,
			DefaultWorkerIsolation: "per_session", // fleet default makes app elastic
		},
		prx,
		st,
		func(slug, bundleDir string, idx int) (*deploy.Result, error) {
			deployMu.Lock()
			deployCount++
			deployMu.Unlock()
			return &deploy.Result{Index: idx, PID: 99, Port: 20099}, nil
		},
	)

	w.WakeTrigger("inherit-app")
	waitNotWaking(t, st, "inherit-app")

	// The deploy function must NOT have been called: resolved isolation is elastic.
	deployMu.Lock()
	got := deployCount
	deployMu.Unlock()
	if got != 0 {
		t.Errorf("deployFn called %d time(s); fleet-default-elastic wake must skip replica boot", got)
	}

	// App must be running: FinishWake committed the CAS.
	st.mu.Lock()
	status := st.apps["inherit-app"].Status
	st.mu.Unlock()
	if status != "running" {
		t.Errorf("app status after fleet-default-elastic wake = %q, want running", status)
	}

	// Pool mode must be per_session (resolved from fleet default).
	prx.mu.Lock()
	mode := prx.poolModes["inherit-app"]
	prx.mu.Unlock()
	if mode != config.IsolationPerSession {
		t.Errorf("pool mode = %q, want %q", mode, config.IsolationPerSession)
	}
}

// TestRestoreWarm_FleetDefaultElastic_Skipped verifies that RestoreWarm skips
// an app whose per-app WorkerIsolation is empty but whose resolved mode (via
// fleet default) is elastic. No claim is taken, no deploy function is called,
// and the app remains hibernated.
func TestRestoreWarm_FleetDefaultElastic_Skipped(t *testing.T) {
	prx := newModeRecordingProxy()
	st := newFakeStore(
		map[string]*db.App{"inherit-hiber": {
			ID:              20,
			Slug:            "inherit-hiber",
			Status:          "hibernated",
			Replicas:        1,
			WorkerIsolation: "", // empty: inherits fleet default
		}},
		[]*db.Deployment{{BundleDir: "/bundles/v1"}},
	)
	st.mu.Lock()
	st.forceHibernatedList = []*db.App{st.apps["inherit-hiber"]}
	st.mu.Unlock()

	var deployCount int
	w := newPoolModeWatcher(
		Config{
			RestartMaxAttempts:     5,
			DefaultWorkerIsolation: "per_session", // fleet default makes app elastic
		},
		prx,
		st,
		func(slug, bundleDir string, idx int) (*deploy.Result, error) {
			deployCount++
			return &deploy.Result{Index: idx, PID: 77, Port: 20077}, nil
		},
	)

	w.RestoreWarm(context.Background())

	if deployCount != 0 {
		t.Errorf("RestoreWarm called deployFn %d time(s) for fleet-default-elastic app; want 0", deployCount)
	}
	// App stays hibernated: the elastic skip fires before any claim is taken.
	st.mu.Lock()
	status := st.apps["inherit-hiber"].Status
	st.mu.Unlock()
	if status != "hibernated" {
		t.Errorf("app status after RestoreWarm = %q, want hibernated", status)
	}
}

// TestRestoreWarm_ElasticSkipped verifies that RestoreWarm skips elastic apps
// entirely (no claim taken, no deployFn called) so they remain hibernated.
func TestRestoreWarm_ElasticSkipped(t *testing.T) {
	prx := newModeRecordingProxy()
	prx.LastSeen("elastic-hiber") // warm the seen map key (no-op, just ensure no panic)
	st := newFakeStore(
		map[string]*db.App{"elastic-hiber": {
			ID:                2,
			Slug:              "elastic-hiber",
			Status:            "hibernated",
			Replicas:          1,
			WorkerIsolation:   "grouped",
			WorkerGroupedSize: 3,
		}},
		[]*db.Deployment{{BundleDir: "/bundles/v1"}},
	)
	// Force ListHibernatedApps to return this elastic app.
	st.mu.Lock()
	st.forceHibernatedList = []*db.App{st.apps["elastic-hiber"]}
	st.mu.Unlock()

	var deployCount int
	w := newPoolModeWatcher(
		Config{RestartMaxAttempts: 5},
		prx,
		st,
		func(slug, bundleDir string, idx int) (*deploy.Result, error) {
			deployCount++
			return &deploy.Result{Index: idx, PID: 77, Port: 20077}, nil
		},
	)

	w.RestoreWarm(context.Background())

	if deployCount != 0 {
		t.Errorf("RestoreWarm called deployFn %d time(s) for elastic app; want 0", deployCount)
	}
	// App stays hibernated (no claim was taken).
	st.mu.Lock()
	status := st.apps["elastic-hiber"].Status
	st.mu.Unlock()
	if status != "hibernated" {
		t.Errorf("elastic app status after RestoreWarm = %q, want hibernated", status)
	}
}
