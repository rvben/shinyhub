// Package autoscale provides a provider-agnostic replica autoscale controller.
// It evaluates each opted-in app's session saturation on a fixed interval and
// drives the incremental scale primitives to converge the replica count on a
// target average sessions-per-replica, within the app's configured bounds and
// the runtime ceiling. It never scales worker hosts and never touches apps that
// have not opted in.
package autoscale

import (
	"context"
	"encoding/json"
	"log/slog"
	"time"

	"github.com/rvben/shinyhub/internal/db"
	"github.com/rvben/shinyhub/internal/proxy"
)

// Lister returns the apps that have opted into autoscaling and are actionable.
type Lister interface {
	ListAutoscaleApps() ([]*db.App, error)
}

// Signal is the proxy-level saturation signal: per-replica active session
// counts and the rolling pool-saturated rejection rollup.
type Signal interface {
	ReplicaSessionCounts(slug string) []int64
	RejectsByReason(slug string, d time.Duration) map[proxy.RejectReason]uint64
}

// Scaler drives the incremental scale primitives. ScaleUp grows the pool by one
// replica; ScaleDown gracefully removes one. Both return (false, nil) for the
// benign no-op cases (ceiling reached, floor reached, app not running).
type Scaler interface {
	ScaleUp(slug string) (bool, error)
	ScaleDown(slug string, grace time.Duration) (bool, error)
}

// Config holds the controller's resolved runtime settings.
type Config struct {
	// ScanInterval is how often the controller evaluates opted-in apps.
	ScanInterval time.Duration
	// Cooldown is the minimum time between successive scale actions on one app.
	Cooldown time.Duration
	// DrainGrace bounds how long ScaleDown waits for sessions to finish.
	DrainGrace time.Duration
	// RejectWindow is the look-back window for the pool-saturated reject signal.
	RejectWindow time.Duration
	// DefaultTarget is the fallback target fraction when an app's own target is 0.
	DefaultTarget float64
	// DefaultCap is the fallback per-replica session cap when an app's own cap is 0.
	DefaultCap int
	// RuntimeMax is the runtime-wide replica ceiling.
	RuntimeMax int
}

// CooldownStore persists the per-app autoscale cooldown so it survives process
// restart and failover to a standby control-plane instance, and supplies the DB
// clock the cooldown is measured against. *db.Store satisfies it. The read side
// of the cooldown is the persisted db.App.LastAutoscaleAt the Lister returns each
// tick; SetAppLastAutoscaleAt is the write side. NowEpoch returns the DB clock so
// the armed timestamp and the cooldown check use one clock (immune to wall-clock
// skew between instances), not the local clock of whichever instance is active.
type CooldownStore interface {
	NowEpoch() (int64, error)
	SetAppLastAutoscaleAt(slug string, epoch int64) error
}

// Controller evaluates and converges replica counts. Its internal state is owned
// solely by the Run loop goroutine, so it needs no lock. The per-app cooldown is
// persisted (apps.last_autoscale_at, read via the app list each tick) so it
// survives process restart and failover, rather than living in process memory.
type Controller struct {
	cfg      Config
	lister   Lister
	signal   Signal
	scaler   Scaler
	recorder AuditRecorder // records scale events to the audit log
	cooldown CooldownStore // persists the per-app cooldown timestamp
	log      *slog.Logger
	metrics  AutoscaleMetrics // nil until SetMetrics is called
}

// New builds a controller. log may be nil, in which case the default logger is
// used.
func New(cfg Config, lister Lister, signal Signal, scaler Scaler, recorder AuditRecorder, cooldown CooldownStore, log *slog.Logger) *Controller {
	if log == nil {
		log = slog.Default()
	}
	return &Controller{
		cfg:      cfg,
		lister:   lister,
		signal:   signal,
		scaler:   scaler,
		recorder: recorder,
		cooldown: cooldown,
		log:      log,
	}
}

// Run evaluates opted-in apps every ScanInterval until ctx is cancelled. Each
// tick reads the current time from the DB clock so the cooldown is measured
// against the same clock that stamped the last action, regardless of which
// instance is active. A DB-clock read failure skips the tick (the DB is the same
// one ListAutoscaleApps needs, so it would fail too).
func (c *Controller) Run(ctx context.Context) {
	t := time.NewTicker(c.cfg.ScanInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			epoch, err := c.cooldown.NowEpoch()
			if err != nil {
				c.log.Error("autoscale: read db clock", "err", err)
				continue
			}
			c.reconcile(time.Unix(epoch, 0))
		}
	}
}

// reconcile evaluates every opted-in app once.
func (c *Controller) reconcile(now time.Time) {
	apps, err := c.lister.ListAutoscaleApps()
	if err != nil {
		c.log.Error("autoscale: list apps", "err", err)
		return
	}
	for _, a := range apps {
		c.reconcileApp(a, now)
	}
}

// cooldownSeconds rounds a cooldown duration UP to whole seconds - the resolution
// of the persisted epoch-second timestamp the cooldown is measured against. A
// positive cooldown therefore always yields at least 1s and is never silently
// disabled by truncation; a zero or negative cooldown yields 0 (no throttle).
func cooldownSeconds(d time.Duration) int64 {
	if d <= 0 {
		return 0
	}
	return int64((d + time.Second - 1) / time.Second)
}

// reconcileApp evaluates one app and takes at most one scaling decision.
func (c *Controller) reconcileApp(a *db.App, now time.Time) {
	// Defence in depth against a row that was flagged enabled without the bounds
	// the API enforces on enable: a zero max would clamp every decision to one
	// replica and scale a healthy pool down. Such a row is a misconfiguration,
	// so hold rather than act on it.
	if a.AutoscaleMinReplicas < 1 || a.AutoscaleMaxReplicas < a.AutoscaleMinReplicas {
		c.log.Warn("autoscale: app has invalid bounds, skipping",
			"slug", a.Slug, "min", a.AutoscaleMinReplicas, "max", a.AutoscaleMaxReplicas)
		return
	}

	counts := c.signal.ReplicaSessionCounts(a.Slug)
	if len(counts) == 0 {
		// The proxy has no pool registered for this app, or the pool is empty;
		// either way there is no usable saturation signal, so do not act.
		return
	}
	// A nil slot (-1) or fewer registered slots than the desired count means the
	// pool is degraded and the self-healer is restoring it. Track this so we can
	// withhold scale-down: ScaleDown removes the highest index and could stop the
	// last healthy replica before the missing slot heals.
	poolDegraded := len(counts) < a.Replicas
	var total int64
	for _, n := range counts {
		if n < 0 {
			poolDegraded = true
			continue
		}
		total += n
	}

	sessionCap := a.MaxSessionsPerReplica
	if sessionCap <= 0 {
		sessionCap = c.cfg.DefaultCap
	}
	target := a.AutoscaleTarget
	if target <= 0 {
		target = c.cfg.DefaultTarget
	}
	saturated := c.signal.RejectsByReason(a.Slug, c.cfg.RejectWindow)[proxy.ReasonPoolSaturated] > 0

	desired, reason := desiredReplicas(scaleInput{
		activeSessions: total,
		// a.Replicas is a best-effort snapshot from the list query and is not
		// held under the per-slug scale lock. The scale primitives re-read the
		// live count under that lock and no-op when it already matches, so a
		// single decision can never be double-applied even if this snapshot is
		// momentarily stale against a concurrent API-driven scale.
		current:    a.Replicas,
		cap:        sessionCap,
		target:     target,
		min:        a.AutoscaleMinReplicas,
		max:        a.AutoscaleMaxReplicas,
		runtimeMax: c.cfg.RuntimeMax,
		saturated:  saturated,
	})
	if desired == a.Replicas {
		return
	}
	// Cooldown is the persisted apps.last_autoscale_at (DB-clock epoch seconds;
	// 0 = never), read from the per-tick app list, so it survives restart and
	// failover. Both now and the stored value come from the DB clock (see Run), so
	// the elapsed comparison is skew-free across instances. Resolution is whole
	// seconds (the stored value is epoch seconds); the configured cooldown is
	// rounded UP, so a positive cooldown is always at least 1s and never silently
	// disabled. Sub-second cooldowns are not meaningful here - the controller acts
	// at most once per ScanInterval, which is far coarser than a second.
	if a.LastAutoscaleAt != 0 && now.Unix()-a.LastAutoscaleAt < cooldownSeconds(c.cfg.Cooldown) {
		return
	}

	// arm persists the cooldown. It is called the moment an action first takes
	// effect - after the first successful scale-up step (not after the whole
	// multi-step loop, so a crash mid-loop still leaves the cooldown set for the
	// next owner), and right after a successful scale-down. A no-op (the primitive
	// refused at a ceiling/floor or for a non-running app) never calls it, so a
	// no-op cannot suppress a genuinely needed action in the next window.
	arm := func() {
		if err := c.cooldown.SetAppLastAutoscaleAt(a.Slug, now.Unix()); err != nil {
			c.log.Error("autoscale: persist cooldown", "slug", a.Slug, "err", err)
		}
	}
	var acted bool
	if desired > a.Replicas {
		acted = c.scaleUp(a.Slug, desired-a.Replicas, arm) > 0
	} else {
		// Never remove capacity from a degraded pool: defer to the self-healer,
		// which is restoring the missing slot, and let autoscale resume scaling
		// down only once the pool reports a full, healthy set.
		if poolDegraded {
			return
		}
		// Scale down conservatively: one replica per tick, so a graceful drain
		// completes and the signal is re-measured before removing the next.
		ok, err := c.scaler.ScaleDown(a.Slug, c.cfg.DrainGrace)
		if err != nil {
			c.log.Error("autoscale: scale down", "slug", a.Slug, "err", err)
			return
		}
		if ok {
			arm()
			c.log.Info("autoscale: scaled down", "slug", a.Slug, "from", a.Replicas, "toward", desired)
		}
		acted = ok
	}
	if acted {
		action := ActionScaleUp
		if desired < a.Replicas {
			action = ActionScaleDown
		}
		// "to" is the convergence target (desired replica count). For scale-down
		// the controller removes one replica per tick, so "to" may not be reached
		// in a single action; the log message above uses "toward" for the same reason.
		detail, _ := json.Marshal(map[string]any{
			"from":     a.Replicas,
			"to":       desired,
			"reason":   reason,
			"sessions": total,
			"target":   target,
		})
		c.recorder.LogAuditEvent(db.AuditEventParams{
			Action:       action,
			ResourceType: "app",
			ResourceID:   a.Slug,
			Detail:       string(detail),
		})
		if c.metrics != nil {
			dir := "up"
			if desired < a.Replicas {
				dir = "down"
			}
			c.metrics.RecordAutoscaleScale(dir)
		}
	}
}

// scaleUp jumps toward the desired count in one tick, since adding capacity
// under load should be fast. It stops early on the first no-op (ceiling reached
// / app no longer running) or error, and returns the number of replicas added.
// onFirstAction fires exactly once, immediately after the first successful step,
// so the caller can arm the cooldown before the loop finishes (a crash mid-loop
// then still leaves the cooldown set).
func (c *Controller) scaleUp(slug string, steps int, onFirstAction func()) int {
	added := 0
	for i := 0; i < steps; i++ {
		ok, err := c.scaler.ScaleUp(slug)
		if err != nil {
			c.log.Error("autoscale: scale up", "slug", slug, "err", err)
			break
		}
		if !ok {
			break
		}
		if added == 0 {
			onFirstAction()
		}
		added++
	}
	if added > 0 {
		c.log.Info("autoscale: scaled up", "slug", slug, "added", added)
	}
	return added
}
