package lifecycle

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/rvben/shinyhub/internal/db"
	"github.com/rvben/shinyhub/internal/deploy"
	"github.com/rvben/shinyhub/internal/process"
	"github.com/rvben/shinyhub/internal/proxy"
)

// Config controls watchdog and hibernation behaviour. All fields have defaults
// applied in main.go from config.LifecycleConfig.
type Config struct {
	WatchInterval      time.Duration // loop tick period; default 15s
	RestartMaxAttempts int           // max consecutive crash restarts before degraded; default 5
	HibernateTimeout   time.Duration // global idle timeout; 0 = disabled globally
}

// replicaKey uniquely identifies a single replica within a slug.
type replicaKey struct {
	slug  string
	index int
}

// manager is the subset of *process.Manager used by the Watcher.
// The interface enables testing with fakes without starting real processes.
type manager interface {
	All() []*process.ProcessInfo
	Stop(slug string) error
}

// proxyBackend is the subset of *proxy.Proxy used by the Watcher.
type proxyBackend interface {
	SetOnMiss(fn func(string))
	LastSeen(slug string) time.Time
	Deregister(slug string)
	SetPoolSize(slug string, size int)
}

// appStore is the subset of *db.Store used by the Watcher.
type appStore interface {
	GetAppBySlug(slug string) (*db.App, error)
	UpdateAppStatus(p db.UpdateAppStatusParams) error
	ListDeployments(appID int64) ([]*db.Deployment, error)
	UpsertReplica(p db.UpsertReplicaParams) error
}

// Compile-time interface satisfaction checks.
var _ manager = (*process.Manager)(nil)
var _ proxyBackend = (*proxy.Proxy)(nil)
var _ appStore = (*db.Store)(nil)

// Watcher owns crash-restart and idle-hibernation policy. It runs a background
// loop that inspects process state on each tick and takes corrective action.
type Watcher struct {
	cfg    Config
	mgr    manager
	prx    proxyBackend
	store  appStore
	deploy func(slug, bundleDir string, index int) (*deploy.Result, error)

	mu        sync.Mutex
	attempts  map[replicaKey]int       // consecutive crash-restart attempts per replica
	nextRetry map[replicaKey]time.Time // earliest time to retry a crashed replica
	waking    map[string]bool          // true while a wake goroutine is in flight for slug
}

// New constructs a Watcher. deployFn encapsulates deploy.RunReplica with the
// shared Manager and Proxy so wake/restart paths can persist the resulting PID
// and port on a per-replica basis.
func New(cfg Config, mgr *process.Manager, prx *proxy.Proxy, st *db.Store,
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

// Start wires up the onMiss callback on the proxy and launches the background
// watchdog/hibernation loop. Blocks until ctx is cancelled.
func (w *Watcher) Start(ctx context.Context) {
	w.prx.SetOnMiss(w.OnMiss)
	ticker := time.NewTicker(w.cfg.WatchInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			w.runOnce()
		}
	}
}

// RunOnce exposes a single watchdog/hibernation tick for testing.
func (w *Watcher) RunOnce() { w.runOnce() }

// runOnce processes all current manager entries for one watchdog/hibernation tick.
// handleIdle is called at most once per slug — since idleness is per-app (not
// per-replica), iterating the whole pool would redundantly hibernate the same app.
func (w *Watcher) runOnce() {
	idleChecked := make(map[string]bool)
	for _, info := range w.mgr.All() {
		switch info.Status {
		case process.StatusCrashed:
			w.handleCrashed(info.Slug, info.Index)
		case process.StatusRunning:
			if !idleChecked[info.Slug] {
				idleChecked[info.Slug] = true
				w.handleIdle(info.Slug)
			}
		}
	}
}

// handleCrashed attempts to restart a crashed replica with exponential backoff.
// After RestartMaxAttempts consecutive failures the app is marked degraded only
// if every replica in the pool has also exhausted its retry budget.
func (w *Watcher) handleCrashed(slug string, index int) {
	k := replicaKey{slug, index}

	w.mu.Lock()
	if retry, ok := w.nextRetry[k]; ok && time.Now().Before(retry) {
		w.mu.Unlock()
		return // still within backoff window
	}
	// If this replica has already exhausted its budget, skip the increment
	// and re-check degraded status (handles the case where another replica
	// later exhausts its budget too).
	if w.attempts[k] > w.cfg.RestartMaxAttempts {
		w.mu.Unlock()
		w.maybeMarkDegraded(slug)
		return
	}
	w.attempts[k]++
	attempt := w.attempts[k]
	w.mu.Unlock()

	if attempt > w.cfg.RestartMaxAttempts {
		// Do not delete the attempt key here — maybeMarkDegraded reads it
		// to determine whether every replica has exhausted its budget.
		// The keys are cleaned up on a successful restart.
		w.maybeMarkDegraded(slug)
		return
	}

	app, err := w.store.GetAppBySlug(slug)
	if err != nil {
		return
	}
	deployments, err := w.store.ListDeployments(app.ID)
	if err != nil || len(deployments) == 0 {
		return
	}

	res, err := w.deploy(slug, deployments[0].BundleDir, index)
	if err != nil {
		// Schedule the next retry: 2^(attempt-1) seconds, capped at 5 minutes.
		delaySec := 1 << uint(attempt-1)
		if delaySec > 5*60 {
			delaySec = 5 * 60
		}
		w.mu.Lock()
		w.nextRetry[k] = time.Now().Add(time.Duration(delaySec) * time.Second)
		w.mu.Unlock()
		return
	}

	pid, port := res.PID, res.Port
	_ = w.store.UpsertReplica(db.UpsertReplicaParams{
		AppID:  app.ID,
		Index:  index,
		PID:    &pid,
		Port:   &port,
		Status: "running",
	})
	_ = w.store.UpdateAppStatus(db.UpdateAppStatusParams{Slug: slug, Status: "running"})

	// Successful restart — reset backoff state for this replica.
	w.mu.Lock()
	delete(w.attempts, k)
	delete(w.nextRetry, k)
	w.mu.Unlock()
}

// maybeMarkDegraded sets app status to degraded only if every replica in the
// pool has exhausted its retry budget. Running replicas keep the app green.
func (w *Watcher) maybeMarkDegraded(slug string) {
	app, err := w.store.GetAppBySlug(slug)
	if err != nil {
		return
	}
	// If any replica is still running, the app is not fully degraded.
	for _, info := range w.mgr.All() {
		if info.Slug == slug && info.Status == process.StatusRunning {
			return
		}
	}
	// No running replicas — check every index has exhausted attempts.
	w.mu.Lock()
	defer w.mu.Unlock()
	for idx := 0; idx < app.Replicas; idx++ {
		if w.attempts[replicaKey{slug, idx}] <= w.cfg.RestartMaxAttempts {
			return
		}
	}
	_ = w.store.UpdateAppStatus(db.UpdateAppStatusParams{Slug: slug, Status: "degraded"})
}

// handleIdle checks whether a running app has been idle past its configured
// timeout and hibernates it if so. It stops all replicas and zeroes replica rows.
func (w *Watcher) handleIdle(slug string) {
	app, err := w.store.GetAppBySlug(slug)
	if err != nil {
		return
	}

	var timeout time.Duration
	if app.HibernateTimeoutMinutes != nil {
		if *app.HibernateTimeoutMinutes == 0 {
			return // hibernation explicitly disabled for this app
		}
		timeout = time.Duration(*app.HibernateTimeoutMinutes) * time.Minute
	} else {
		if w.cfg.HibernateTimeout == 0 {
			return // hibernation disabled globally
		}
		timeout = w.cfg.HibernateTimeout
	}

	lastActivity := w.prx.LastSeen(slug)
	if lastActivity.IsZero() {
		lastActivity = app.UpdatedAt // freshly deployed, never served
	}
	if time.Since(lastActivity) < timeout {
		return
	}

	_ = w.mgr.Stop(slug) // stops all replicas in the pool
	w.prx.Deregister(slug)
	for i := 0; i < app.Replicas; i++ {
		_ = w.store.UpsertReplica(db.UpsertReplicaParams{AppID: app.ID, Index: i, Status: "stopped"})
	}
	_ = w.store.UpdateAppStatus(db.UpdateAppStatusParams{Slug: slug, Status: "hibernated"})
}

// OnMiss is registered with the Proxy as the onMiss callback. When a request
// arrives for a hibernated app, this wakes it by re-running the deploy for all
// replicas in parallel.
func (w *Watcher) OnMiss(slug string) {
	w.mu.Lock()
	if w.waking[slug] {
		w.mu.Unlock()
		return // already waking; concurrent requests share the same wake
	}
	w.waking[slug] = true
	w.mu.Unlock()

	go func() {
		defer func() {
			w.mu.Lock()
			delete(w.waking, slug)
			w.mu.Unlock()
		}()

		app, err := w.store.GetAppBySlug(slug)
		if err != nil || app.Status != "hibernated" {
			return
		}
		deployments, err := w.store.ListDeployments(app.ID)
		if err != nil || len(deployments) == 0 {
			return
		}

		w.prx.SetPoolSize(slug, app.Replicas)

		var wg sync.WaitGroup
		for i := 0; i < app.Replicas; i++ {
			wg.Add(1)
			go func(idx int) {
				defer wg.Done()
				res, err := w.deploy(slug, deployments[0].BundleDir, idx)
				if err != nil {
					slog.Warn("wake replica failed", "slug", slug, "idx", idx, "err", err)
					return
				}
				pid, port := res.PID, res.Port
				_ = w.store.UpsertReplica(db.UpsertReplicaParams{
					AppID:  app.ID,
					Index:  idx,
					PID:    &pid,
					Port:   &port,
					Status: "running",
				})
			}(i)
		}
		wg.Wait()
		_ = w.store.UpdateAppStatus(db.UpdateAppStatusParams{Slug: slug, Status: "running"})
	}()
}
