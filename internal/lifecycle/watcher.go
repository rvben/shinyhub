package lifecycle

import (
	"context"
	"errors"
	"fmt"
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
	// IdentityHeadersGlobal is the global auth.identity_headers enabled flag.
	// Used to resolve each app's effective identity-forwarding setting on wake.
	IdentityHeadersGlobal bool
	// Clustered must be true when the deployment uses a shared Postgres
	// database (isClustered). When true the watcher reaps stale
	// replica_sessions rows on every tick and uses the conservative fleet-idle
	// CAS for hibernation. When false (single-node SQLite) the reaper is
	// skipped entirely and single-node behaviour is byte-for-byte unchanged.
	Clustered bool
	// InstanceID is the per-instance identifier used to exclude this
	// instance's own replica_sessions rows from the fleet-idle check, so the
	// active's local idle predicate (BeginHibernate) and the fleet predicate
	// (AppFleetLoad) each cover a disjoint set of sessions.
	InstanceID string
	// MaxSuspended caps how many replicas may stay suspended (warm-wake) at once.
	// When the count exceeds it, the watcher evicts the oldest-suspended replicas
	// (stop them; they cold-boot on next wake). 0 disables the cap.
	MaxSuspended int
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
	// StopReplica stops a single replica (unfreezing it first if suspended). Used
	// by the warm-wake GC to evict the oldest suspended replicas.
	StopReplica(slug string, index int) error
	// Suspend freezes a slug's replicas, freeing host RAM, when the runtime
	// supports it. freed=true means the warmed memory was released and the
	// replicas are resumable; freed=false means the caller must Stop instead.
	Suspend(slug string) (bool, error)
}

// proxyBackend is the subset of *proxy.Proxy used by the Watcher.
type proxyBackend interface {
	LastSeen(slug string) time.Time
	BeginHibernate(slug string, since time.Time) bool
	Deregister(slug string)
	SetPoolSize(slug string, size int)
	SetPoolCap(slug string, max int)
	// SetPoolAppID associates the numeric DB app ID with slug's pool so the
	// session reporter can key replica_sessions rows without a DB lookup.
	// A zero appID is ignored. Called alongside SetPoolSize wherever the app
	// object is available.
	SetPoolAppID(slug string, appID int64)
	// SetPoolIdentityHeaders sets the per-pool identity-forwarding flag.
	// Called alongside SetPoolSize and SetPoolCap wherever the app object is
	// available.
	SetPoolIdentityHeaders(slug string, enabled bool)
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
	BeginWake(slug string) (bool, error)
	AbortWake(slug string) error
	FinishWake(slug string) (bool, error)
	ListDeployments(appID int64) ([]*db.Deployment, error)
	UpsertReplica(p db.UpsertReplicaParams) error
	ListReconcilableApps() ([]*db.App, error)
	ListWakingApps() ([]*db.App, error)
	ListReplicas(appID int64) ([]*db.Replica, error)
	// ReapStaleReplicaSessions removes replica_sessions rows whose updated_at
	// is older than staleWindowSec seconds ago on the database clock. Called on
	// every watcher tick (owner-gated) so rows from crashed or restarted
	// instances are pruned promptly.
	ReapStaleReplicaSessions(staleWindowSec int64) error
	// HibernateApp atomically transitions a running app to "hibernated". Used
	// by the clustered hibernation path: the CAS is issued before stopping
	// replicas so any concurrent wake hits the hibernated->waking transition
	// rather than finding the app still "running" but pool-less.
	HibernateApp(slug string) (bool, error)
	// AppFleetLoad returns the per-replica active session counts and the
	// number of seconds since the most recent fleet activity (idleSinceSec),
	// both measured on the database clock. Used by the clustered hibernation
	// path to confirm that no other instance is currently serving traffic.
	AppFleetLoad(appID int64, staleWindowSec int64, excludeInstanceID string) (active []int64, idleSinceSec int64, err error)
	// AppFleetLastActivity returns the MAX(last_activity) Unix epoch across
	// non-stale, non-excluded replica_sessions rows. Returns 0 when no fresh
	// rows exist. Used by the clustered warm-expand path to compare fleet
	// activity against the shrink moment entirely on the database clock.
	AppFleetLastActivity(appID int64, staleWindowSec int64, excludeInstanceID string) (int64, error)
	// ListWarmShrunkApps returns running/degraded apps that have at least one
	// replica parked with desired_state='warm'. The watcher expand check
	// iterates this set each tick.
	ListWarmShrunkApps() ([]*db.App, error)
	// ListSuspendedReplicas returns all suspended replicas oldest-first, joined to
	// the app slug. The warm-wake GC evicts the oldest over the configured cap.
	ListSuspendedReplicas() ([]db.SuspendedReplica, error)
	// ListHibernatedApps returns apps whose status is 'hibernated'. The startup
	// warm-restore pass re-boots and re-freezes exactly this set.
	ListHibernatedApps() ([]*db.App, error)
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

	// isOwner reports whether this instance currently holds the control-plane
	// lease. Set once via SetIsOwner before Start; read on every wake trigger.
	// When nil (tests or unconfigured setups) the instance is treated as owner
	// so single-node behaviour is byte-for-byte unchanged.
	isOwner func() bool

	mu        sync.Mutex
	stopping  bool                     // set when Start's ctx is cancelled; rejects new wakes
	attempts  map[replicaKey]int       // consecutive crash-restart attempts per replica
	nextRetry map[replicaKey]time.Time // earliest time to retry a crashed replica
	// driving tracks slugs currently being driven by driveWakingApp so a
	// concurrent trigger (e.g. inline from the miss path and from the reconciler)
	// does not spawn two parallel deploys for the same wake. The guard is
	// in-memory only; it prevents the double-drive within one process while
	// BeginWake's DB CAS prevents it across processes.
	driving map[string]bool

	// expandingWarm tracks slugs for which a burst-triggered warm expansion is
	// already in flight. When a pool-degraded reject fires WakeTrigger concurrently
	// from many goroutines, only the first wins the guard; the rest return
	// immediately without issuing store reads or calling warmExpand again.
	expandingWarm map[string]bool

	// wakeWG tracks in-flight driveWakingApp goroutines so shutdown can wait
	// for them to persist replica/PID rows before the store is closed.
	wakeWG sync.WaitGroup

	// warmShrink and warmExpand are the pre-warming executors injected via
	// SetWarmOps. When warmShrink is non-nil and an app has MinWarmReplicas > 0,
	// the idle path calls warmShrink instead of fully hibernating the app.
	// Both run under the per-slug deploy lock inside the api.Server methods.
	// nil leaves pre-warming disabled: apps hibernate fully regardless of their
	// configured floor (safe degradation for unconfigured setups).
	warmShrink func(slug string, floor int) (bool, error)
	warmExpand func(slug string) (bool, error)

	// resume restores a suspended replica via deploy.ResumeReplica. nil disables
	// the warm-wake path: every wake cold-boots (safe default for unconfigured
	// setups). Set once at startup via SetResume before Start.
	resume func(slug, bundleDir string, index int) (*deploy.Result, error)
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
		cfg:           cfg,
		mgr:           mgr,
		prx:           prx,
		store:         st,
		deploy:        deployFn,
		attempts:      make(map[replicaKey]int),
		nextRetry:     make(map[replicaKey]time.Time),
		driving:       make(map[string]bool),
		expandingWarm: make(map[string]bool),
	}
}

// SetIsOwner wires the predicate that reports whether this instance currently
// holds the control-plane lease. Call once at startup before any traffic
// arrives. When unset (nil), the instance is treated as always-owner so
// single-node wake behaviour is byte-for-byte unchanged.
func (w *Watcher) SetIsOwner(fn func() bool) {
	w.isOwner = fn
}

// Start launches the background watchdog/hibernation loop. Blocks until ctx is
// cancelled. Safe to call multiple times across ownership spans: resets the
// stopping flag so wakes are admitted again after a previous span drained.
// The wake trigger is wired in main.go at startup on every instance (not inside
// Start) so a standby instance can issue the BeginWake CAS on a miss even when
// it is not the active owner.
func (w *Watcher) Start(ctx context.Context) {
	w.mu.Lock()
	w.stopping = false
	w.mu.Unlock()
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

// SetWarmOps wires the warm shrink/expand executors (api.Server.WarmShrink
// and WarmExpand). Both run under the per-slug deploy lock. nil leaves
// pre-warming disabled (apps hibernate fully regardless of their floor).
// Call once at startup before Start.
func (w *Watcher) SetWarmOps(shrink func(slug string, floor int) (bool, error), expand func(slug string) (bool, error)) {
	w.warmShrink = shrink
	w.warmExpand = expand
}

// SetResume wires the warm-wake executor (deploy.ResumeReplica). nil leaves the
// warm-wake path disabled: every wake cold-boots. Call once at startup before Start.
func (w *Watcher) SetResume(fn func(slug, bundleDir string, index int) (*deploy.Result, error)) {
	w.resume = fn
}

// hibernatePool removes a slug's replicas from host resources on hibernate,
// preferring a memory-preserving Suspend when the runtime supports it and the
// warmed RAM was actually freed. On freed=true the replica rows are marked
// suspended (resumable on wake). Otherwise it falls back to Stop + stopped,
// which always frees RAM (the never-worse-than-cold-stop invariant).
func (w *Watcher) hibernatePool(app *db.App) {
	freed, err := w.mgr.Suspend(app.Slug)
	if freed && err == nil {
		for i := 0; i < app.Replicas; i++ {
			if uerr := w.store.UpsertReplica(db.UpsertReplicaParams{
				AppID: app.ID, Index: i, Status: db.ReplicaStatusSuspended, DesiredState: "stopped",
			}); uerr != nil {
				slog.Warn("watcher: persist suspended replica failed", "slug", app.Slug, "index", i, "err", uerr)
			}
		}
		return
	}
	if err != nil && !errors.Is(err, process.ErrRuntimeNotSnapshotter) {
		slog.Warn("watcher: suspend failed; falling back to stop", "slug", app.Slug, "err", err)
	}
	if serr := w.mgr.Stop(app.Slug); serr != nil {
		slog.Warn("watcher: stop on hibernate failed", "slug", app.Slug, "err", serr)
	}
	for i := 0; i < app.Replicas; i++ {
		if uerr := w.store.UpsertReplica(db.UpsertReplicaParams{
			AppID: app.ID, Index: i, Status: "stopped", DesiredState: "stopped",
		}); uerr != nil {
			slog.Warn("watcher: persist hibernated replica failed", "slug", app.Slug, "index", i, "err", uerr)
		}
	}
}

// RestoreWarm re-boots and re-freezes apps that were hibernated before a server
// restart, so their next access is a warm resume instead of a cold boot. A
// frozen process does not survive a restart, so this re-creates the warm state
// from scratch and lands each app in the exact state an idle hibernation would:
// processes frozen + RAM reclaimed, replica rows suspended, and - crucially - no
// proxy pool, so the next request triggers a normal wake (warm resume) rather
// than routing to a frozen process.
//
// Per app: claim it (BeginWake CAS, hibernated->waking) so a concurrent user
// wake cannot double-boot it; size the pool and cold-boot each replica into a
// fresh per-app cgroup; then remove the pool from routing (gated on no in-flight
// request) and suspend. It runs in the background after recovery, one app at a
// time (so the boots do not all spike the host at once), and is owner-gated and
// ctx-cancellable. Best-effort: a failed boot leaves that app cold - woken on
// first access, exactly as before.
func (w *Watcher) RestoreWarm(ctx context.Context) {
	if w.deploy == nil {
		return
	}
	apps, err := w.store.ListHibernatedApps()
	if err != nil {
		slog.Warn("warm restore: list hibernated apps failed", "err", err)
		return
	}
	restored := 0
	for _, app := range apps {
		if ctx.Err() != nil {
			return
		}
		if w.isOwner != nil && !w.isOwner() {
			return // a standby never drives app processes
		}
		if app.Replicas < 1 {
			continue
		}

		// Claim the app so a concurrent user wake (WakeTrigger -> BeginWake) cannot
		// double-boot it. CAS hibernated->waking; if it fails another path already
		// owns the wake (or the app is no longer hibernated) - leave it to them.
		won, werr := w.store.BeginWake(app.Slug)
		if werr != nil {
			slog.Warn("warm restore: begin wake failed", "slug", app.Slug, "err", werr)
			continue
		}
		if !won {
			continue
		}

		deployments, derr := w.store.ListDeployments(app.ID)
		if derr != nil || len(deployments) == 0 {
			if derr != nil {
				slog.Warn("warm restore: list deployments failed", "slug", app.Slug, "err", derr)
			}
			w.abortWarmClaim(app.Slug) // never deployed; release claim, leave cold
			continue
		}
		bundleDir := deployments[0].BundleDir

		// Snapshot the activity mark BEFORE the pool exists so the freeze below can
		// detect any real request that lands on a booted replica during restore.
		sinceMark := w.prx.LastSeen(app.Slug)

		// Size the proxy pool before booting, exactly as the wake/recovery paths
		// do: after a restart the in-memory pool is empty for a hibernated app
		// (recovery only sizes running apps), so RegisterReplica would otherwise
		// fail with "pool size not set".
		w.prx.SetPoolSize(app.Slug, app.Replicas)
		w.prx.SetPoolCap(app.Slug, deploy.ResolveMaxSessionsPerReplica(app.MaxSessionsPerReplica, w.cfg.DefaultMaxSessionsPerReplica))
		w.prx.SetPoolAppID(app.Slug, app.ID)
		w.prx.SetPoolIdentityHeaders(app.Slug, deploy.ResolveIdentityHeaders(app.IdentityHeaders, w.cfg.IdentityHeadersGlobal))

		booted := true
		for i := 0; i < app.Replicas; i++ {
			if ctx.Err() != nil {
				booted = false
				break
			}
			if _, derr := w.deploy(app.Slug, bundleDir, i); derr != nil {
				slog.Warn("warm restore: boot failed; app left cold", "slug", app.Slug, "idx", i, "err", derr)
				booted = false
				break
			}
		}
		if !booted {
			// A replica failed mid-boot (or shutdown raced in). Take down any that
			// came up so the app is genuinely cold (no orphan replica running under
			// a 'hibernated' app, which the idle watcher would never reap), and
			// release the claim. Mirrors the watcher's Deregister+Stop teardown.
			w.prx.Deregister(app.Slug)
			if serr := w.mgr.Stop(app.Slug); serr != nil {
				slog.Warn("warm restore: cleanup after partial boot failed", "slug", app.Slug, "err", serr)
			}
			w.abortWarmClaim(app.Slug)
			if ctx.Err() != nil {
				return
			}
			continue
		}

		// Park the booted replicas into the frozen warm state, exactly as an idle
		// hibernation does: atomically remove the pool from routing (BeginHibernate
		// aborts if a request raced in during boot), then suspend. Removing the
		// pool is what makes the next access trigger a wake -> warm resume instead
		// of routing to a frozen process.
		if !w.prx.BeginHibernate(app.Slug, sinceMark) {
			// A real request landed on the app during restore; it is serving now.
			// Promote it to running and leave it up - the idle watchdog hibernates
			// it normally later.
			if _, ferr := w.store.FinishWake(app.Slug); ferr != nil {
				slog.Warn("warm restore: finish wake failed", "slug", app.Slug, "err", ferr)
			}
			slog.Info("warm restore: app served a request during restore; left running", "slug", app.Slug)
			continue
		}
		w.hibernatePool(app)
		// Restore the hibernated status: AbortWake is the waking->hibernated CAS.
		w.abortWarmClaim(app.Slug)
		restored++
		slog.Info("warm restore: re-froze hibernated app", "slug", app.Slug, "replicas", app.Replicas)
	}
	if restored > 0 {
		slog.Info("warm restore complete", "apps", restored)
	}
}

// abortWarmClaim returns a warm-restore claim from "waking" to "hibernated". It
// is the waking->hibernated CAS (AbortWake), used both to release a claim we
// could not fulfil and to park a successfully re-frozen app back to hibernated.
func (w *Watcher) abortWarmClaim(slug string) {
	if aerr := w.store.AbortWake(slug); aerr != nil {
		slog.Warn("warm restore: release claim failed", "slug", slug, "err", aerr)
	}
}

// wakeReplica brings one replica back up. For a replica persisted as suspended it
// tries the warm Resume path first; on any resume error (or when resume is
// unconfigured / the replica was not suspended) it falls back to the existing
// cold RunReplica boot. The fallback guarantees wake is never worse than today.
func (w *Watcher) wakeReplica(slug, bundleDir string, index int, suspended bool) (*deploy.Result, error) {
	if suspended && w.resume != nil {
		res, err := w.resume(slug, bundleDir, index)
		if err == nil {
			return res, nil
		}
		slog.Warn("watcher: resume failed; cold-booting", "slug", slug, "idx", index, "err", err)
	}
	return w.deploy(slug, bundleDir, index)
}

// enforceSuspendedCap evicts the oldest suspended replicas when their count
// exceeds Config.MaxSuspended, bounding the swap/disk footprint of warm-wake.
// Evicted replicas are stopped (StopReplica unfreezes and removes them) and
// marked stopped, so they cold-boot on their next wake. 0 disables the cap.
func (w *Watcher) enforceSuspendedCap() {
	if w.cfg.MaxSuspended <= 0 {
		return
	}
	susp, err := w.store.ListSuspendedReplicas()
	if err != nil {
		slog.Warn("watcher: list suspended replicas failed", "err", err)
		return
	}
	excess := len(susp) - w.cfg.MaxSuspended
	if excess <= 0 {
		return
	}
	for i := 0; i < excess; i++ {
		r := susp[i] // oldest-first
		if err := w.mgr.StopReplica(r.Slug, r.Index); err != nil {
			slog.Warn("watcher: evict suspended replica failed", "slug", r.Slug, "index", r.Index, "err", err)
			continue
		}
		if err := w.store.UpsertReplica(db.UpsertReplicaParams{
			AppID: r.AppID, Index: r.Index, Status: "stopped", DesiredState: "stopped",
		}); err != nil {
			slog.Warn("watcher: persist evicted replica failed", "slug", r.Slug, "index", r.Index, "err", err)
		}
	}
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
// As the active (owner) instance it also reaps stale replica_sessions rows so
// counts from crashed or restarted peers do not linger in the fleet view.
func (w *Watcher) runOnce() {
	// Snapshot the manager once and derive both the crash set and the per-slug
	// running count in a single pass. runningCounts feeds the warm-shrink floor
	// guard in handleIdle: when runningCount <= app.MinWarmReplicas the app is
	// already at its floor and the warmShrink call is skipped at zero DB cost.
	all := w.mgr.All()
	runningCounts := make(map[string]int, len(all))
	for _, info := range all {
		if info.Status == process.StatusRunning {
			runningCounts[info.Slug]++
		}
	}

	idleChecked := make(map[string]bool)
	handled := make(map[replicaKey]bool)
	for _, info := range all {
		switch info.Status {
		case process.StatusCrashed:
			handled[replicaKey{info.Slug, info.Index}] = true
			w.handleCrashed(info.Slug, info.Index)
		case process.StatusRunning:
			if !idleChecked[info.Slug] {
				idleChecked[info.Slug] = true
				w.handleIdle(info.Slug, runningCounts[info.Slug])
			}
		}
	}
	w.reconcileReplicas(handled)
	w.reconcileStatuses()
	w.handleWarmExpand()
	w.enforceSuspendedCap()
	// Reap stale replica_sessions rows only in clustered mode. Single-node
	// deployments never write replica_sessions rows, so this DELETE is both
	// unnecessary and a behavioral change we must avoid.
	if w.cfg.Clustered {
		staleWindowSec := int64(proxy.ReplicaSessionStaleCutoff.Seconds())
		if err := w.store.ReapStaleReplicaSessions(staleWindowSec); err != nil {
			slog.Warn("watcher: reap stale replica sessions failed", "err", err)
		}
	}
	// Drive any apps left in 'waking'. In clustered mode a standby issues
	// BeginWake (hibernated->waking) on a miss but cannot deploy; the active's
	// reconciler picks them up here. In single-node mode this reconcile is the
	// safety net for wakes interrupted by a process handoff (ZDT upgrade): the
	// old process may have died mid-driveWakingApp; BeginWake's CAS prevents
	// re-triggering (status != hibernated); and the inline trigger drive in
	// WakeTrigger does not run across a process boundary. The watcher already
	// runs inside ownerWork, so this block is owner-gated on both paths.
	wakingApps, err := w.store.ListWakingApps()
	if err != nil {
		slog.Warn("watcher: list waking apps failed", "err", err)
	} else {
		for _, app := range wakingApps {
			w.driveWakingApp(app.Slug)
		}
	}
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
	running, warm := 0, 0
	for _, r := range reps {
		if r.Index >= app.Replicas {
			continue
		}
		switch {
		case r.Status == db.ReplicaStatusRunning:
			running++
		case r.DesiredState == db.ReplicaDesiredWarm:
			// Warm victims are deliberately stopped capacity, not failures.
			warm++
		}
	}
	want := "running"
	if running < app.Replicas-warm {
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
//
// runningCount is the number of replicas the manager currently reports as running
// for this slug (derived from the same mgr.All() snapshot as the caller's loop).
// It is used by the warm-shrink branch to skip the warmShrink call when the app
// is already at or below its floor, avoiding a lock acquisition and DB read for
// a no-op.
//
// Single-node (!w.cfg.Clustered): the original path is taken byte-for-byte.
// Clustered (w.cfg.Clustered): a conservative fleet-idle CAS is used. See
// handleIdleClustered for the exact predicate and ordering.
func (w *Watcher) handleIdle(slug string, runningCount int) {
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

	if w.cfg.Clustered {
		w.handleIdleClustered(app, timeout, runningCount)
		return
	}

	// Single-node path: byte-for-byte original behaviour.
	lastActivity := w.prx.LastSeen(slug)
	if lastActivity.IsZero() {
		lastActivity = app.UpdatedAt // freshly deployed, never served
	}
	if time.Since(lastActivity) < timeout {
		return
	}

	// Warm-shrink branch: when an app has a pre-warming floor and the shrink
	// executor is wired, shrink to the floor instead of fully hibernating.
	// Skip when already at or below the floor: the manager's in-memory running
	// count is the cheapest signal available and avoids a deploy-lock acquisition
	// and DB reads for a no-op shrink.
	if app.MinWarmReplicas > 0 && w.warmShrink != nil {
		if runningCount <= app.MinWarmReplicas {
			return
		}
		if _, err := w.warmShrink(slug, app.MinWarmReplicas); err != nil {
			slog.Warn("watcher: warm shrink failed", "slug", slug, "err", err)
		}
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

	w.hibernatePool(app)
	if err := w.store.UpdateAppStatus(db.UpdateAppStatusParams{Slug: slug, Status: "hibernated"}); err != nil {
		slog.Warn("watcher: persist hibernated status failed", "slug", slug, "err", err)
	}
	w.recordTransition("hibernate")
}

// handleIdleClustered is the clustered hibernation path. It uses a conservative
// two-part idle predicate before issuing a DB CAS (running -> hibernated).
//
// Predicate order (all must pass):
//  1. (A) Time-idle: time.Since(lastActivity) >= timeout.
//  2. (B) Fleet-idle: AppFleetLoad(excludeSelf) reports no other instance has
//     active sessions AND no other instance has recent last_activity within the
//     timeout window.
//  3. (C) Local CAS: BeginHibernate(lastActivity) atomically removes the local
//     pool and returns true iff lastSeen has not advanced AND activeConns==0.
//  4. (D) DB CAS: HibernateApp(slug) = UPDATE ... WHERE status='running'.
//     One winner; the loser no-ops (idempotent).
//
// Only after the DB CAS wins: Stop replicas + UpsertReplica(stopped).
//
// Ordering rationale: the DB CAS is issued BEFORE mgr.Stop so that after commit
// any request arriving on any instance triggers BeginWake (hibernated->waking)
// rather than finding the app "running" with a removed pool. The brief sub-second
// window between CAS-commit and replica-stop is acceptable for an idle app: a
// stray request either hits a still-alive replica or triggers a harmless wake.
//
// Leadership-transfer safety: a stale old-active and a new active could both
// evaluate this during handover, but correctness holds by construction:
//   - The running->hibernated DB CAS is idempotent: exactly one caller wins.
//   - mgr.Stop is idempotent: the loser's call is a no-op.
//   - The fleet-idle check (B) uses AppFleetLoad(excludeSelf): each active's own
//     sessions appear in the OTHER instance's fleet view, so neither hibernates
//     an app that the other is actively serving.
//
// No owner-epoch fence is added to the CAS; it would be dead complexity.
func (w *Watcher) handleIdleClustered(app *db.App, timeout time.Duration, runningCount int) {
	slug := app.Slug

	// (A) Time-idle check: compare local last-seen against the local clock.
	// This is the only place the local wall clock appears, and it governs only
	// the local idle predicate - never the cross-instance stale/idle decision.
	lastActivity := w.prx.LastSeen(slug)
	if lastActivity.IsZero() {
		lastActivity = app.UpdatedAt
	}
	if time.Since(lastActivity) < timeout {
		return
	}

	// (B) Fleet-idle check: consult other instances via replica_sessions rows.
	// All freshness and idle comparisons are on the database clock (staleWindowSec
	// and idleSinceSec), so the result is not affected by control-plane clock skew.
	staleWindowSec := int64(proxy.ReplicaSessionStaleCutoff.Seconds())
	otherActive, otherIdleSinceSec, err := w.store.AppFleetLoad(app.ID, staleWindowSec, w.cfg.InstanceID)
	if err != nil {
		slog.Warn("watcher: fleet load for hibernation check failed", "slug", slug, "err", err)
		return
	}
	// Sum other-instance active counts.
	var totalOtherActive int64
	for _, a := range otherActive {
		totalOtherActive += a
	}
	if totalOtherActive > 0 {
		return // another instance is actively serving; do not hibernate
	}
	// Check other instances' last_activity age: if any peer had recent activity
	// within the timeout window, delay hibernation to let the peer's own idle
	// check fire on its next tick without the active count double-counting.
	// otherIdleSinceSec is a pure duration (db_now - MAX(last_activity)) so this
	// comparison involves no local wall clock.
	if otherIdleSinceSec < int64(timeout.Seconds()) {
		return
	}

	// Warm-shrink branch: when an app has a pre-warming floor and the shrink
	// executor is wired, shrink to the floor instead of fully hibernating.
	// Skip when already at or below the floor: the manager's in-memory running
	// count avoids a deploy-lock acquisition and DB reads for a no-op shrink.
	// Owner-only execution is already guaranteed by the watcher's ownership
	// gate at the call site.
	if app.MinWarmReplicas > 0 && w.warmShrink != nil {
		if runningCount <= app.MinWarmReplicas {
			return
		}
		if _, err := w.warmShrink(slug, app.MinWarmReplicas); err != nil {
			slog.Warn("watcher: warm shrink failed", "slug", slug, "err", err)
		}
		return
	}

	// (C) Local CAS: atomically remove the local pool from routing iff no
	// activity has been recorded since the snapshot AND no local request is
	// in flight. If a request slipped in between (A) and here, abort.
	if !w.prx.BeginHibernate(slug, lastActivity) {
		return
	}

	// (D) DB CAS: running -> hibernated. Must happen BEFORE mgr.Stop so the
	// wake path (BeginWake: hibernated->waking) is armed immediately after commit.
	won, err := w.store.HibernateApp(slug)
	if err != nil {
		slog.Warn("watcher: hibernate CAS failed", "slug", slug, "err", err)
		return
	}
	if !won {
		// Another active (e.g. during a concurrent handoff) already won the CAS
		// or moved the app to a different state. The local pool was already
		// removed by BeginHibernate. Until the DB-driven pool syncer is wired,
		// this is a transient gap: the replicas keep running but are unreachable
		// via this instance's proxy until the pool is restored on a later tick
		// or wake. This is an idempotent no-op: no stop.
		return
	}

	_, endSpan := w.traceOp(context.Background(), "lifecycle.hibernate", slug)
	defer func() { endSpan(nil) }()

	w.hibernatePool(app)
	// Do NOT call UpdateAppStatus(hibernated) here: the DB CAS (D) already set
	// the status. Calling it again would be redundant and would unconditionally
	// overwrite any concurrent status change (e.g. an immediate wake).
	w.recordTransition("hibernate")
}

// handleWarmExpand checks every warm-shrunk app each tick and re-expands any
// whose traffic has resumed since the shrink. It is the expand counterpart of
// the warm-shrink path in handleIdle/handleIdleClustered.
//
// Skipped entirely when warmExpand is nil (pre-warming not configured).
//
// Activity predicate:
//   - Single-node: compares the proxy's wall-clock LastSeen against the
//     shrink moment (the newest updated_at among the app's 'warm' replica rows).
//     Both values are on the same host clock, so the comparison is sound. After
//     a server restart LastSeen is zero, which is before any real shrink moment,
//     so no spurious expansion occurs until real traffic resumes.
//   - Clustered: compares the fleet's MAX(last_activity) epoch (from
//     AppFleetLastActivity, on the database clock) against the shrink moment's
//     Unix epoch. Either the owner's own local LastSeen (also on the local wall
//     clock - the owner proxies traffic too) OR the fleet predicate suffice: the
//     OR covers both paths.
func (w *Watcher) handleWarmExpand() {
	if w.warmExpand == nil {
		return
	}

	apps, err := w.store.ListWarmShrunkApps()
	if err != nil {
		slog.Warn("watcher: list warm-shrunk apps failed", "err", err)
		return
	}

	for _, app := range apps {
		reps, err := w.store.ListReplicas(app.ID)
		if err != nil {
			slog.Warn("watcher: list replicas for warm-expand check failed", "slug", app.Slug, "err", err)
			continue
		}

		// Compute the shrink moment as the newest updated_at among warm-parked rows.
		var shrinkMoment time.Time
		for _, r := range reps {
			if r.DesiredState == db.ReplicaDesiredWarm && r.UpdatedAt.After(shrinkMoment) {
				shrinkMoment = r.UpdatedAt
			}
		}
		if shrinkMoment.IsZero() {
			// No warm rows found (race with another tick that expanded); skip.
			continue
		}

		// Determine whether traffic has resumed since the shrink.
		shouldExpand := false
		if !w.cfg.Clustered {
			// Single-node: compare proxy LastSeen (wall clock) against shrink moment.
			// LastSeen is zero after a restart => no expansion until real traffic,
			// which is the desired safe default.
			lastSeen := w.prx.LastSeen(app.Slug)
			shouldExpand = lastSeen.After(shrinkMoment)
		} else {
			// Clustered: use DB-clock fleet activity OR local LastSeen.
			// The owner proxies traffic as well, so its own LastSeen counts as
			// activity. The fleet predicate covers other instances.
			lastSeen := w.prx.LastSeen(app.Slug)
			if lastSeen.After(shrinkMoment) {
				shouldExpand = true
			} else {
				staleWindowSec := int64(proxy.ReplicaSessionStaleCutoff.Seconds())
				fleetLastActivity, err := w.store.AppFleetLastActivity(app.ID, staleWindowSec, w.cfg.InstanceID)
				if err != nil {
					slog.Warn("watcher: fleet last activity check failed", "slug", app.Slug, "err", err)
					continue
				}
				// fleetLastActivity is a Unix epoch on the DB clock; shrinkMoment is
				// also derived from the DB clock (written by UpsertReplica's nowEpoch).
				shouldExpand = fleetLastActivity > shrinkMoment.Unix()
			}
		}

		if !shouldExpand {
			continue
		}

		if _, err := w.warmExpand(app.Slug); err != nil {
			slog.Warn("watcher: warm expand failed", "slug", app.Slug, "err", err)
		}
	}
}

// WakeTrigger is the callback wired on the proxy as the wake trigger. It runs
// on EVERY instance (active and standby alike). It issues the BeginWake CAS
// (hibernated->waking) so the DB state reflects the wake intent regardless of
// which instance holds the lease. If this instance is the active owner, it also
// drives the wake inline via driveWakingApp; a standby leaves the app in the
// 'waking' state for the active's runOnce reconciler to pick up.
//
// When the BeginWake CAS fails because the app is already running (not
// hibernated), the trigger checks whether any warm-parked replicas exist and
// calls warmExpand immediately. This drives the burst-expand path: a request
// shed with ReasonPoolDegraded fires this trigger so the warm-shrunk pool is
// restored without waiting for the next tick. Duplicate triggers are safe
// because warmExpand is idempotent and deploy-lock-guarded.
func (w *Watcher) WakeTrigger(slug string) {
	won, err := w.store.BeginWake(slug)
	if err != nil {
		slog.Warn("watcher: begin wake failed", "slug", slug, "err", err)
		return
	}
	if !won {
		// The app is not hibernated (running, degraded, or another caller
		// already won the CAS). Check for warm-parked replicas and expand
		// immediately if any exist.
		if w.warmExpand == nil {
			return
		}
		// In-flight guard: a pool-degraded 503 burst fires this trigger from
		// many goroutines simultaneously. Only the first winner proceeds to
		// the store reads and warmExpand call; the rest return immediately so
		// the burst does not generate N×(GetAppBySlug+ListReplicas+warmExpand).
		w.mu.Lock()
		if w.expandingWarm[slug] {
			w.mu.Unlock()
			return
		}
		w.expandingWarm[slug] = true
		w.mu.Unlock()

		defer func() {
			w.mu.Lock()
			delete(w.expandingWarm, slug)
			w.mu.Unlock()
		}()

		app, err := w.store.GetAppBySlug(slug)
		if err != nil {
			return // app not found or DB error; safe to skip
		}
		reps, err := w.store.ListReplicas(app.ID)
		if err != nil {
			slog.Warn("watcher: wake trigger: list replicas failed", "slug", slug, "err", err)
			return
		}
		for _, r := range reps {
			if r.DesiredState == db.ReplicaDesiredWarm {
				if _, err := w.warmExpand(slug); err != nil {
					slog.Warn("watcher: wake trigger: warm expand failed", "slug", slug, "err", err)
				}
				return
			}
		}
		return
	}
	// Only the active owner drives the wake inline. A standby leaves the
	// app in 'waking'; the active's runOnce reconciler drives it on the next
	// tick (clustered-gated).
	owner := w.isOwner == nil || w.isOwner()
	if !owner {
		return
	}
	w.driveWakingApp(slug)
}

// driveWakingApp deploys all replicas for a slug that is already in the
// 'waking' DB state (the BeginWake CAS was already won by the caller or a peer).
// It MUST NOT call BeginWake itself. An in-memory guard prevents two concurrent
// calls from spawning duplicate deploys for the same slug within this process;
// the DB BeginWake CAS guards across processes.
func (w *Watcher) driveWakingApp(slug string) {
	w.mu.Lock()
	if w.stopping {
		w.mu.Unlock()
		// We are shutting down; revert so a successor wakes it.
		if aerr := w.store.AbortWake(slug); aerr != nil {
			slog.Warn("watcher: abort wake on shutdown failed", "slug", slug, "err", aerr)
		}
		return
	}
	if w.driving[slug] {
		// Another goroutine within this process is already driving this wake.
		// The DB CAS prevents a second process from racing us, so this is safe
		// to skip entirely.
		w.mu.Unlock()
		return
	}
	w.driving[slug] = true
	w.wakeWG.Add(1)
	w.mu.Unlock()

	go func() {
		defer func() {
			w.mu.Lock()
			delete(w.driving, slug)
			w.mu.Unlock()
			w.wakeWG.Done()
		}()

		_, endSpan := w.traceOp(context.Background(), "lifecycle.wake", slug)
		var opErr error
		defer func() { endSpan(opErr) }()

		// finalized is set once the wake reaches a stable terminal state (running,
		// or the app's intent was changed out from under us by a concurrent
		// stop/delete). Until then, the deferred guard reverts waking ->
		// hibernated so the app is NEVER left stuck in the transient waking state -
		// including on a panic. AbortWake is a conditional CAS (only acts while
		// status is still waking), so it never clobbers a newer intent.
		finalized := false
		defer func() {
			if r := recover(); r != nil {
				opErr = fmt.Errorf("wake panicked: %v", r)
				slog.Error("watcher: wake panicked", "slug", slug, "panic", r)
			}
			if !finalized {
				if aerr := w.store.AbortWake(slug); aerr != nil {
					slog.Warn("watcher: abort wake failed", "slug", slug, "err", aerr)
				}
			}
		}()

		app, err := w.store.GetAppBySlug(slug)
		if err != nil {
			opErr = err
			return
		}
		if app.Status != "waking" {
			// A concurrent stop/delete changed the app's intent after we won the
			// CAS. Abandon the wake and preserve the newer status (do not revert).
			finalized = true
			return
		}
		deployments, err := w.store.ListDeployments(app.ID)
		if err != nil || len(deployments) == 0 {
			opErr = err
			return
		}

		w.prx.SetPoolSize(slug, app.Replicas)
		w.prx.SetPoolCap(slug, deploy.ResolveMaxSessionsPerReplica(app.MaxSessionsPerReplica, w.cfg.DefaultMaxSessionsPerReplica))
		w.prx.SetPoolAppID(slug, app.ID)
		w.prx.SetPoolIdentityHeaders(slug, deploy.ResolveIdentityHeaders(app.IdentityHeaders, w.cfg.IdentityHeadersGlobal))

		deploymentID := deployments[0].ID
		// Replicas persisted as suspended take the warm Resume path; the rest
		// (and any resume failure) cold-boot. Reading the rows once here keeps the
		// per-replica goroutines lock-free.
		suspendedByIdx := make(map[int]bool)
		if reps, lerr := w.store.ListReplicas(app.ID); lerr == nil {
			for _, r := range reps {
				if r.Status == db.ReplicaStatusSuspended {
					suspendedByIdx[r.Index] = true
				}
			}
		} else {
			slog.Warn("watcher: list replicas for wake failed", "slug", slug, "err", lerr)
		}
		var wg sync.WaitGroup
		var started atomic.Int32
		for i := 0; i < app.Replicas; i++ {
			wg.Add(1)
			go func(idx int) {
				defer wg.Done()
				res, err := w.wakeReplica(slug, deployments[0].BundleDir, idx, suspendedByIdx[idx])
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
					DeploymentID: &deploymentID,
				}); err != nil {
					slog.Warn("watcher: persist woken replica failed", "slug", slug, "index", idx, "err", err)
				}
				started.Add(1)
			}(i)
		}
		wg.Wait()
		if started.Load() == 0 {
			// No replica came up; the deferred guard reverts waking -> hibernated
			// so a later request retries instead of being stuck in waking.
			return
		}
		// Finalize waking -> running via a conditional CAS. If a concurrent
		// stop/delete moved the app off waking during the deploy, FinishWake wins
		// 0 rows and we leave their newer status (the deferred guard's AbortWake is
		// also conditional, so it is a no-op in that case). On a DB error we leave
		// !finalized so the guard reverts to a stable terminal state.
		woke, ferr := w.store.FinishWake(slug)
		if ferr != nil {
			opErr = ferr
			slog.Warn("watcher: finish wake failed", "slug", slug, "err", ferr)
			return
		}
		finalized = true
		if woke {
			w.recordTransition("wake")
			return
		}
		// FinishWake lost: a concurrent stop/delete moved the app off "waking"
		// while this wake was deploying, so it left live replicas behind for an
		// app the operator no longer wants running. Tear down the replicas this
		// wake started (idempotent with the operator's own teardown) when the
		// current intent is stopped/deleting, or when the app row is already gone
		// (a delete that finished). The status gate avoids killing replicas of an
		// app that was stopped then re-started during the wake window. (The fully
		// race-free fix is a shared per-slug lifecycle lock between the API
		// mutators and the watcher; that is a broader hardening that also covers
		// crash-restart.)
		tearDownStarted := func(reason string) {
			slog.Info("watcher: wake superseded; stopping replicas it started", "slug", slug, "reason", reason)
			w.prx.Deregister(slug)
			if serr := w.mgr.Stop(slug); serr != nil {
				slog.Warn("watcher: stop superseded-wake replicas failed", "slug", slug, "err", serr)
			}
		}
		cur, gerr := w.store.GetAppBySlug(slug)
		switch {
		case errors.Is(gerr, db.ErrNotFound):
			tearDownStarted("deleted")
		case gerr == nil && (cur.Status == "stopped" || cur.Status == "deleting"):
			tearDownStarted(cur.Status)
		}
	}()
}
