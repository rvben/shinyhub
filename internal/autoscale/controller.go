// Package autoscale provides a provider-agnostic replica autoscale controller.
// It evaluates each opted-in app's session saturation on a fixed interval and
// drives the incremental scale primitives to converge the replica count on a
// target average sessions-per-replica, within the app's configured bounds and
// the runtime ceiling. It never scales worker hosts and never touches apps that
// have not opted in.
package autoscale

import (
	"context"
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

// Controller evaluates and converges replica counts. Its internal state
// (lastAction) is owned solely by the Run loop goroutine, so it needs no lock.
type Controller struct {
	cfg    Config
	lister Lister
	signal Signal
	scaler Scaler
	log    *slog.Logger

	// lastAction records when the controller last scaled each slug, for the
	// per-app cooldown. Pruned each tick to the current app set.
	lastAction map[string]time.Time
}

// New builds a controller. log may be nil, in which case the default logger is
// used.
func New(cfg Config, lister Lister, signal Signal, scaler Scaler, log *slog.Logger) *Controller {
	if log == nil {
		log = slog.Default()
	}
	return &Controller{
		cfg:        cfg,
		lister:     lister,
		signal:     signal,
		scaler:     scaler,
		log:        log,
		lastAction: make(map[string]time.Time),
	}
}

// Run evaluates opted-in apps every ScanInterval until ctx is cancelled.
func (c *Controller) Run(ctx context.Context) {
	t := time.NewTicker(c.cfg.ScanInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-t.C:
			c.reconcile(now)
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
	live := make(map[string]struct{}, len(apps))
	for _, a := range apps {
		live[a.Slug] = struct{}{}
		c.reconcileApp(a, now)
	}
	// Drop cooldown state for apps that are no longer opted in / actionable so
	// the map cannot grow without bound across app churn.
	for slug := range c.lastAction {
		if _, ok := live[slug]; !ok {
			delete(c.lastAction, slug)
		}
	}
}

// reconcileApp evaluates one app and takes at most one scaling decision.
func (c *Controller) reconcileApp(a *db.App, now time.Time) {
	counts := c.signal.ReplicaSessionCounts(a.Slug)
	if counts == nil {
		// The proxy has no pool registered for this app; nothing to measure.
		return
	}
	var total int64
	for _, n := range counts {
		if n > 0 {
			total += n
		}
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

	desired := desiredReplicas(scaleInput{
		activeSessions: total,
		current:        a.Replicas,
		cap:            sessionCap,
		target:         target,
		min:            a.AutoscaleMinReplicas,
		max:            a.AutoscaleMaxReplicas,
		runtimeMax:     c.cfg.RuntimeMax,
		saturated:      saturated,
	})
	if desired == a.Replicas {
		return
	}
	if last, ok := c.lastAction[a.Slug]; ok && now.Sub(last) < c.cfg.Cooldown {
		return
	}

	if desired > a.Replicas {
		c.scaleUp(a.Slug, desired-a.Replicas)
	} else {
		// Scale down conservatively: one replica per tick, so a graceful drain
		// completes and the signal is re-measured before removing the next.
		if _, err := c.scaler.ScaleDown(a.Slug, c.cfg.DrainGrace); err != nil {
			c.log.Error("autoscale: scale down", "slug", a.Slug, "err", err)
			return
		}
		c.log.Info("autoscale: scaled down", "slug", a.Slug, "from", a.Replicas, "toward", desired)
	}
	c.lastAction[a.Slug] = now
}

// scaleUp jumps toward the desired count in one tick, since adding capacity
// under load should be fast. It stops early on the first no-op (ceiling reached
// / app no longer running) or error.
func (c *Controller) scaleUp(slug string, steps int) {
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
		added++
	}
	if added > 0 {
		c.log.Info("autoscale: scaled up", "slug", slug, "added", added)
	}
}
