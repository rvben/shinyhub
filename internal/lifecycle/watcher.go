package lifecycle

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/rvben/shinyhub/internal/db"
	"github.com/rvben/shinyhub/internal/deploy"
	"github.com/rvben/shinyhub/internal/process"
	"github.com/rvben/shinyhub/internal/proxy"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
)

// Config controls watchdog and hibernation behaviour. All fields have defaults
// applied in main.go from config.LifecycleConfig.
type Config struct {
	WatchInterval      time.Duration // loop tick period; default 15s
	RestartMaxAttempts int           // max consecutive crash restarts before degraded; default 5
	HibernateTimeout   time.Duration // global idle timeout; 0 = disabled globally
	// DefaultMaxSessionsPerReplica is the runtime-wide session cap fallback
	// applied on wake when an app has max_sessions_per_replica == 0.
	DefaultMaxSessionsPerReplica int
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
	BeginHibernate(slug string, since time.Time) bool
	Deregister(slug string)
	SetPoolSize(slug string, size int)
	SetPoolCap(slug string, max int)
}

// MetricsRecorder records lifecycle business metrics. A nil recorder disables
// recording; the metrics package's *Registry satisfies it. Kept as an interface
// so this package does not import Prometheus.
type MetricsRecorder interface {
	RecordStateTransition(event string)
	RecordReplicaRestart()
}

// appStore is the subset of *db.Store used by the Watcher.
type appStore interface {
	GetAppBySlug(slug string) (*db.App, error)
	UpdateAppStatus(p db.UpdateAppStatusParams) error
	ListDeployments(appID int64) ([]*db.Deployment, error)
	UpsertReplica(p db.UpsertReplicaParams) error
	ListReconcilableApps() ([]*db.App, error)
	ListReplicas(appID int64) ([]*db.Replica, error)
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

	// tierHealthy reports whether a healthy worker exists for a tier. It gates
	// re-placement of lost replicas: nil disables lost-replica healing entirely;
	// a false result keeps a no-worker replica lost at zero cost. Set once via
	// EnableLostReplicaHealing before Start, then only read.
	tierHealthy func(tier string) bool

	// metrics records lifecycle business metrics. nil disables recording. Set
	// once via SetMetrics before Start, then only read.
	metrics MetricsRecorder

	// tracer emits spans for background operations (wake/restart/hibernate).
	// nil disables tracing. Set once via SetTracer before Start, then only read.
	tracer trace.Tracer

	mu        sync.Mutex
	stopping  bool                     // set when Start's ctx is cancelled; rejects new wakes
	attempts  map[replicaKey]int       // consecutive crash-restart attempts per replica
	nextRetry map[replicaKey]time.Time // earliest time to retry a crashed replica
	waking    map[string]bool          // true while a wake goroutine is in flight for slug

	// wakeWG tracks in-flight OnMiss wake goroutines so shutdown can wait
	// for them to persist replica/PID rows before the store is closed.
	wakeWG sync.WaitGroup
}

// wakeDrainTimeout bounds how long Start waits for outstanding wake
// goroutines after its context is cancelled, so a slow app launch cannot
// hang process shutdown indefinitely.
const wakeDrainTimeout = 15 * time.Second

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
			w.drainWakes()
			return
		case <-ticker.C:
			w.runOnce()
		}
	}
}

// drainWakes stops admitting new wakes and waits (bounded) for in-flight
// ones to finish so their replica rows are persisted before shutdown
// closes the store.
func (w *Watcher) drainWakes() {
	w.mu.Lock()
	w.stopping = true
	w.mu.Unlock()

	done := make(chan struct{})
	go func() {
		w.wakeWG.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(wakeDrainTimeout):
		slog.Warn("watcher: shutdown timeout; abandoning in-flight wakes")
	}
}

// RunOnce exposes a single watchdog/hibernation tick for testing.
func (w *Watcher) RunOnce() { w.runOnce() }

// EnableLostReplicaHealing turns on re-placement of lost replicas, gating it on
// the supplied predicate: a lost replica is re-placed only when its tier has a
// healthy worker. Called once at startup before Start; leaving it unset keeps
// lost replicas untouched (crash recovery is unaffected).
func (w *Watcher) EnableLostReplicaHealing(tierHealthy func(tier string) bool) {
	w.tierHealthy = tierHealthy
}

// SetMetrics wires a recorder for lifecycle business metrics. Called once at
// startup before Start; leaving it unset disables recording. The nil-safe
// recordTransition/recordRestart helpers gate every call site.
func (w *Watcher) SetMetrics(m MetricsRecorder) {
	w.metrics = m
}

func (w *Watcher) recordTransition(event string) {
	if w.metrics != nil {
		w.metrics.RecordStateTransition(event)
	}
}

func (w *Watcher) recordRestart() {
	if w.metrics != nil {
		w.metrics.RecordReplicaRestart()
	}
}

// SetTracer wires a tracer for lifecycle background-operation spans. Called once
// at startup before Start; leaving it unset disables tracing. The nil-safe
// traceOp helper gates every call site.
func (w *Watcher) SetTracer(t trace.Tracer) {
	w.tracer = t
}

// traceOp starts an internal span named op for slug and returns a derived
// context plus an end func that records the operation's error (if any) and ends
// the span. A no-op when tracing is disabled. Background operations are
// unparented, so the returned context carries a root span.
func (w *Watcher) traceOp(ctx context.Context, op, slug string) (context.Context, func(err error)) {
	if w.tracer == nil {
		return ctx, func(error) {}
	}
	ctx, span := w.tracer.Start(ctx, op, trace.WithSpanKind(trace.SpanKindInternal))
	span.SetAttributes(attribute.String("shinyhub.app.slug", slug))
	return ctx, func(err error) {
		if err != nil {
			span.RecordError(err)
			span.SetStatus(codes.Error, err.Error())
		}
		span.End()
	}
}

// runOnce processes all current manager entries for one watchdog/hibernation tick.
// handleIdle is called at most once per slug — since idleness is per-app (not
// per-replica), iterating the whole pool would redundantly hibernate the same app.
func (w *Watcher) runOnce() {
	idleChecked := make(map[string]bool)
	handled := make(map[replicaKey]bool)
	for _, info := range w.mgr.All() {
		switch info.Status {
		case process.StatusCrashed:
			handled[replicaKey{info.Slug, info.Index}] = true
			w.handleCrashed(info.Slug, info.Index)
		case process.StatusRunning:
			if !idleChecked[info.Slug] {
				idleChecked[info.Slug] = true
				w.handleIdle(info.Slug)
			}
		}
	}
	w.reconcileReplicas(handled)
	w.reconcileStatuses()
}

// reconcileReplicas re-places replica slots the process manager is not actively
// driving: persisted-crashed slots with no live manager entry (the state left
// by a partial-success deploy) and lost slots (whose worker died). Both run over
// running+degraded apps so a degraded app can recover. handled holds the slots
// already driven this tick via the manager loop so they are not driven twice.
func (w *Watcher) reconcileReplicas(handled map[replicaKey]bool) {
	apps, err := w.store.ListReconcilableApps()
	if err != nil {
		return
	}
	for _, app := range apps {
		reps, err := w.store.ListReplicas(app.ID)
		if err != nil {
			continue
		}
		for _, r := range reps {
			if r.Index >= app.Replicas || handled[replicaKey{app.Slug, r.Index}] {
				continue
			}
			switch r.Status {
			case "crashed":
				w.restartSlot(app, r.Index)
			case db.ReplicaStatusLost:
				// Lost-only gate: a healthy worker must exist for the tier before
				// spending any effort. This is both the cheap fast-path skip and the
				// on/off switch; ErrNoLiveWorker classification in restartSlot is the
				// correctness backstop for the gate-vs-start TOCTOU.
				if w.tierHealthy != nil && w.tierHealthy(r.Tier) {
					w.restartSlot(app, r.Index)
				}
			}
		}
	}
}

// handleCrashed restarts a crashed replica reported by the process manager. It
// loads the owning app and delegates to the shared restartSlot core.
func (w *Watcher) handleCrashed(slug string, index int) {
	app, err := w.store.GetAppBySlug(slug)
	if err != nil {
		return
	}
	w.restartSlot(app, index)
}

// restartSlot is the single deploy+persist+backoff core shared by the crash path
// and lost-replica re-placement. It re-runs the current deployment for one
// replica index and persists the result. A missing worker or a lost race against
// a concurrent redeploy is classified as zero-cost (the restart budget is not
// consumed); any other deploy error consumes one attempt and schedules backoff.
// Status promotion is left to reconcileStatuses (the single status authority).
func (w *Watcher) restartSlot(app *db.App, index int) {
	k := replicaKey{app.Slug, index}

	w.mu.Lock()
	if retry, ok := w.nextRetry[k]; ok && time.Now().Before(retry) {
		w.mu.Unlock()
		return // still within backoff window
	}
	if w.attempts[k] >= w.cfg.RestartMaxAttempts {
		w.mu.Unlock()
		return // restart budget spent; stay degraded
	}
	w.mu.Unlock()

	_, endSpan := w.traceOp(context.Background(), "lifecycle.restart", app.Slug)
	var opErr error
	defer func() { endSpan(opErr) }()

	deployments, err := w.store.ListDeployments(app.ID)
	if err != nil || len(deployments) == 0 {
		opErr = err
		return
	}
	res, err := w.deploy(app.Slug, deployments[0].BundleDir, index)
	if err != nil {
		if errors.Is(err, process.ErrNoLiveWorker) || errors.Is(err, process.ErrReplicaAlreadyRunning) {
			return // not the app's fault: retry next tick at zero cost
		}
		opErr = err
		w.mu.Lock()
		w.attempts[k]++
		w.scheduleBackoffLocked(k, w.attempts[k])
		w.mu.Unlock()
		return
	}

	pid, port := res.PID, res.Port
	depID := deployments[0].ID
	if err := w.store.UpsertReplica(db.UpsertReplicaParams{
		AppID:        app.ID,
		Index:        index,
		PID:          &pid,
		Port:         &port,
		Status:       db.ReplicaStatusRunning,
		Provider:     res.Provider,
		Tier:         res.Tier,
		EndpointURL:  res.EndpointURL,
		WorkerID:     res.WorkerID,
		AppVersion:   deployments[0].Version,
		DesiredState: "running",
		DeploymentID: &depID,
	}); err != nil {
		slog.Warn("watcher: persist restarted replica failed", "slug", app.Slug, "index", index, "err", err)
	}

	// Successful restart — reset backoff state for this replica.
	w.mu.Lock()
	delete(w.attempts, k)
	delete(w.nextRetry, k)
	w.mu.Unlock()
	w.recordRestart()
}

// scheduleBackoffLocked sets the next retry time for k using exponential backoff
// (2^(attempt-1) seconds, capped at 5 minutes). The caller must hold w.mu.
func (w *Watcher) scheduleBackoffLocked(k replicaKey, attempt int) {
	delaySec := 1 << uint(attempt-1)
	if delaySec > 5*60 {
		delaySec = 5 * 60
	}
	w.nextRetry[k] = time.Now().Add(time.Duration(delaySec) * time.Second)
}

// reconcileStatuses is the sole running<->degraded authority. It runs over
// running+degraded apps and reconciles each app's status against its real
// replica health, consistent with "UpdateAppStatus is soft state — the watchdog
// reconciles".
func (w *Watcher) reconcileStatuses() {
	apps, err := w.store.ListReconcilableApps()
	if err != nil {
		return
	}
	for _, app := range apps {
		w.reconcileAppStatus(app)
	}
}

// reconcileAppStatus marks an app running iff every desired slot is running,
// else degraded. It only moves an app between running and degraded; it never
// touches hibernated/deploying/stopped apps.
func (w *Watcher) reconcileAppStatus(app *db.App) {
	if app.Status != "running" && app.Status != "degraded" {
		return
	}
	reps, err := w.store.ListReplicas(app.ID)
	if err != nil {
		return
	}
	running := 0
	for _, r := range reps {
		if r.Index < app.Replicas && r.Status == db.ReplicaStatusRunning {
			running++
		}
	}
	want := "running"
	if running < app.Replicas {
		want = "degraded"
	}
	if want != app.Status {
		if err := w.store.UpdateAppStatus(db.UpdateAppStatusParams{Slug: app.Slug, Status: want}); err != nil {
			slog.Warn("watcher: reconcile app status failed", "slug", app.Slug, "want", want, "err", err)
		}
	}
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

	// CAS-style hibernate: atomically remove the pool from routing iff no
	// activity has been recorded since the snapshot AND no request is in
	// flight. If a request slipped in between LastSeen above and here, abort
	// without stopping the manager — the next tick will reconsider.
	if !w.prx.BeginHibernate(slug, lastActivity) {
		return
	}

	_, endSpan := w.traceOp(context.Background(), "lifecycle.hibernate", slug)
	defer func() { endSpan(nil) }()

	if err := w.mgr.Stop(slug); err != nil { // stops all replicas in the pool
		slog.Warn("watcher: stop on hibernate failed", "slug", slug, "err", err)
	}
	for i := 0; i < app.Replicas; i++ {
		if err := w.store.UpsertReplica(db.UpsertReplicaParams{AppID: app.ID, Index: i, Status: "stopped", DesiredState: "stopped"}); err != nil {
			slog.Warn("watcher: persist hibernated replica failed", "slug", slug, "index", i, "err", err)
		}
	}
	if err := w.store.UpdateAppStatus(db.UpdateAppStatusParams{Slug: slug, Status: "hibernated"}); err != nil {
		slog.Warn("watcher: persist hibernated status failed", "slug", slug, "err", err)
	}
	w.recordTransition("hibernate")
}

// OnMiss is registered with the Proxy as the onMiss callback. When a request
// arrives for a hibernated app, this wakes it by re-running the deploy for all
// replicas in parallel.
func (w *Watcher) OnMiss(slug string) {
	w.mu.Lock()
	if w.stopping {
		w.mu.Unlock()
		return // shutting down; do not start new app launches
	}
	if w.waking[slug] {
		w.mu.Unlock()
		return // already waking; concurrent requests share the same wake
	}
	w.waking[slug] = true
	w.wakeWG.Add(1)
	w.mu.Unlock()

	go func() {
		defer w.wakeWG.Done()
		defer func() {
			w.mu.Lock()
			delete(w.waking, slug)
			w.mu.Unlock()
		}()

		_, endSpan := w.traceOp(context.Background(), "lifecycle.wake", slug)
		var opErr error
		defer func() { endSpan(opErr) }()

		app, err := w.store.GetAppBySlug(slug)
		if err != nil || app.Status != "hibernated" {
			opErr = err
			return
		}
		deployments, err := w.store.ListDeployments(app.ID)
		if err != nil || len(deployments) == 0 {
			opErr = err
			return
		}

		w.prx.SetPoolSize(slug, app.Replicas)
		w.prx.SetPoolCap(slug, deploy.ResolveMaxSessionsPerReplica(app.MaxSessionsPerReplica, w.cfg.DefaultMaxSessionsPerReplica))

		var wg sync.WaitGroup
		var started atomic.Int32
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
				if err := w.store.UpsertReplica(db.UpsertReplicaParams{
					AppID:        app.ID,
					Index:        idx,
					PID:          &pid,
					Port:         &port,
					Status:       "running",
					Provider:     res.Provider,
					Tier:         res.Tier,
					EndpointURL:  res.EndpointURL,
					WorkerID:     res.WorkerID,
					AppVersion:   deployments[0].Version,
					DesiredState: "running",
				}); err != nil {
					slog.Warn("watcher: persist woken replica failed", "slug", slug, "index", idx, "err", err)
				}
				started.Add(1)
			}(i)
		}
		wg.Wait()
		if started.Load() > 0 {
			if err := w.store.UpdateAppStatus(db.UpdateAppStatusParams{Slug: slug, Status: "running"}); err != nil {
				slog.Warn("watcher: persist woken status failed", "slug", slug, "err", err)
			}
			w.recordTransition("wake")
		}
	}()
}
