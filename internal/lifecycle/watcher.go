package lifecycle

import (
	"context"
	"sync"
	"time"

	"github.com/rvben/shinyhub/internal/db"
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
}

// appStore is the subset of *db.Store used by the Watcher.
type appStore interface {
	GetAppBySlug(slug string) (*db.App, error)
	UpdateAppStatus(p db.UpdateAppStatusParams) error
	ListDeployments(appID int64) ([]*db.Deployment, error)
}

// Watcher owns crash-restart and idle-hibernation policy. It runs a background
// loop that inspects process state on each tick and takes corrective action.
type Watcher struct {
	cfg    Config
	mgr    manager
	prx    proxyBackend
	store  appStore
	deploy func(slug, bundleDir string) error

	mu        sync.Mutex
	attempts  map[string]int       // consecutive crash-restart attempts per slug
	nextRetry map[string]time.Time // earliest time to retry a crashed app
	waking    map[string]bool      // true while a wake goroutine is in flight for slug
}

// New constructs a Watcher. deployFn encapsulates deploy.Run with the shared
// Manager and Proxy so the Watcher stays free of deploy-package details.
func New(cfg Config, mgr *process.Manager, prx *proxy.Proxy, st *db.Store,
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

// runOnce processes all current manager entries for one watchdog/hibernation tick.
func (w *Watcher) runOnce() {
	for _, info := range w.mgr.All() {
		switch info.Status {
		case process.StatusCrashed:
			w.handleCrashed(info.Slug)
		case process.StatusRunning:
			w.handleIdle(info.Slug)
		}
	}
}

// handleCrashed attempts to restart a crashed app with exponential backoff.
// After RestartMaxAttempts consecutive failures the app is marked degraded.
func (w *Watcher) handleCrashed(slug string) {
	w.mu.Lock()
	if retry, ok := w.nextRetry[slug]; ok && time.Now().Before(retry) {
		w.mu.Unlock()
		return // still within backoff window
	}
	w.attempts[slug]++
	attempt := w.attempts[slug]
	w.mu.Unlock()

	if attempt > w.cfg.RestartMaxAttempts {
		_ = w.store.UpdateAppStatus(db.UpdateAppStatusParams{Slug: slug, Status: "degraded"})
		w.mu.Lock()
		delete(w.attempts, slug)
		delete(w.nextRetry, slug)
		w.mu.Unlock()
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

	if err := w.deploy(slug, deployments[0].BundleDir); err != nil {
		// Schedule the next retry: 2^(attempt-1) seconds, capped at 5 minutes.
		delaySec := 1 << uint(attempt-1)
		if delaySec > 5*60 {
			delaySec = 5 * 60
		}
		w.mu.Lock()
		w.nextRetry[slug] = time.Now().Add(time.Duration(delaySec) * time.Second)
		w.mu.Unlock()
		return
	}

	// Successful restart — reset backoff state.
	w.mu.Lock()
	w.attempts[slug] = 0
	delete(w.nextRetry, slug)
	w.mu.Unlock()
}

// handleIdle checks whether a running app has been idle past its configured
// timeout and hibernates it if so.
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

	_ = w.mgr.Stop(slug)
	w.prx.Deregister(slug)
	_ = w.store.UpdateAppStatus(db.UpdateAppStatusParams{Slug: slug, Status: "hibernated"})
}

// OnMiss is registered with the Proxy as the onMiss callback. When a request
// arrives for a hibernated app, this wakes it by re-running the deploy.
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
		_ = w.deploy(slug, deployments[0].BundleDir)
	}()
}
