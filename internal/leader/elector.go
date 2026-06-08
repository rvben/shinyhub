// Package leader elects a single owner control-plane instance via a fenced,
// DB-backed lease. The owner is the only instance permitted to run the
// singleton background loops, the boot-time reconciles, and mutating
// operations; non-owners serve reads and the proxy.
package leader

import (
	"context"
	"log/slog"
	"sync"
	"time"
)

// OwnerStore is the narrow persistence the Elector needs. *db.Store implements it.
type OwnerStore interface {
	AcquireOwner(instanceID string, ttl time.Duration) (acquired bool, epoch int64, err error)
	RenewOwner(instanceID string, epoch int64, ttl time.Duration) (ok bool, err error)
	ReleaseOwner(instanceID string, epoch int64) error
}

// Config configures an Elector. TTL MUST be at least 2x RenewEvery so a single
// missed renewal does not expire an otherwise-healthy lease.
type Config struct {
	InstanceID string
	TTL        time.Duration
	RenewEvery time.Duration
	OnAcquire  func(epoch int64) // fired (synchronously) when this instance becomes owner
	OnLose     func()            // fired (synchronously) when this instance stops being owner
	Logger     *slog.Logger
}

// Elector runs the acquire/renew loop for one instance and reports ownership.
type Elector struct {
	cfg   Config
	store OwnerStore

	mu    sync.Mutex
	owner bool
	epoch int64
	// leaseExpiry is the local-clock deadline of the lease this instance last
	// secured (set on acquire and each successful renew to now+TTL). A transient
	// renew error keeps ownership only until this deadline; past it the instance
	// fails closed, because the DB lease can then be acquired by another instance
	// and continuing to serve mutations would make this a split owner.
	leaseExpiry time.Time
}

// clampTTL enforces the Config invariant that the lease TTL is at least twice
// the renewal interval, so a single missed renewal never expires an otherwise
// healthy lease. A non-positive TTL is raised to that floor too. When
// renewEvery is non-positive the TTL is returned unchanged (such a config is
// already degenerate and is rejected elsewhere).
func clampTTL(ttl, renewEvery time.Duration) time.Duration {
	if renewEvery <= 0 {
		return ttl
	}
	floor := 2 * renewEvery
	if ttl < floor {
		return floor
	}
	return ttl
}

// New constructs an Elector. Call Run in a goroutine to start it.
func New(store OwnerStore, cfg Config) *Elector {
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	if clamped := clampTTL(cfg.TTL, cfg.RenewEvery); clamped != cfg.TTL {
		cfg.Logger.Warn("lease TTL raised to 2x renew interval",
			"configured_ttl", cfg.TTL, "renew_every", cfg.RenewEvery, "effective_ttl", clamped)
		cfg.TTL = clamped
	}
	return &Elector{cfg: cfg, store: store}
}

// IsOwner reports whether this instance currently holds the lease.
func (e *Elector) IsOwner() bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.owner
}

// Epoch returns the fencing token of the currently-held lease, or 0 when this
// instance is not the owner. Use it for an inline fencing check on a mutating
// operation that must be stamped with the epoch under which it began.
func (e *Elector) Epoch() int64 {
	e.mu.Lock()
	defer e.mu.Unlock()
	if !e.owner {
		return 0
	}
	return e.epoch
}

func (e *Elector) snapshot() (bool, int64) {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.owner, e.epoch
}

func (e *Elector) set(owner bool, epoch int64) {
	e.mu.Lock()
	e.owner, e.epoch = owner, epoch
	e.mu.Unlock()
}

// bumpLeaseFrom records the local lease deadline as start+TTL, where start is a
// timestamp captured BEFORE the acquire/renew store call. The DB stamps the
// authoritative expires_at as (DB clock at commit)+TTL, which is no earlier than
// start+TTL, so deriving the local deadline from the pre-call time keeps it
// conservative: this instance never believes its lease outlives the DB lease by
// the store round-trip latency.
func (e *Elector) bumpLeaseFrom(start time.Time) {
	e.mu.Lock()
	e.leaseExpiry = start.Add(e.cfg.TTL)
	e.mu.Unlock()
}

// leaseExpired reports whether the local lease deadline has passed.
func (e *Elector) leaseExpired() bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	return !e.leaseExpiry.IsZero() && !time.Now().Before(e.leaseExpiry)
}

// relinquish drops ownership and fires OnLose.
func (e *Elector) relinquish() {
	e.set(false, 0)
	if e.cfg.OnLose != nil {
		e.cfg.OnLose()
	}
}

// Run drives the acquire/renew loop until ctx is cancelled, then releases the
// lease if held. It blocks; run it in a goroutine.
func (e *Elector) Run(ctx context.Context) {
	t := time.NewTicker(e.cfg.RenewEvery)
	defer t.Stop()
	e.step()
	for {
		select {
		case <-ctx.Done():
			if owner, epoch := e.snapshot(); owner {
				if err := e.store.ReleaseOwner(e.cfg.InstanceID, epoch); err != nil {
					e.cfg.Logger.Warn("release owner on shutdown", "err", err)
				}
				e.set(false, 0)
				if e.cfg.OnLose != nil {
					e.cfg.OnLose()
				}
			}
			return
		case <-t.C:
			e.step()
		}
	}
}

// step performs one acquire-or-renew cycle and fires transition callbacks.
func (e *Elector) step() {
	owner, epoch := e.snapshot()
	if owner {
		start := time.Now() // before the store call, so the local deadline is conservative
		ok, err := e.store.RenewOwner(e.cfg.InstanceID, epoch, e.cfg.TTL)
		if err != nil {
			// Transient: keep believing we own it and retry next tick - but only
			// while the lease we last secured has not locally expired. TTL > 2x
			// RenewEvery gives slack for a blip; once we pass the deadline we fail
			// closed, since the DB lease can be acquired by another instance and we
			// must not keep serving mutations as a split owner.
			if e.leaseExpired() {
				e.cfg.Logger.Warn("renew failing past lease expiry; relinquishing ownership", "err", err)
				e.relinquish()
			} else {
				e.cfg.Logger.Warn("renew owner", "err", err)
			}
			return
		}
		if !ok {
			e.cfg.Logger.Warn("lost control-plane ownership")
			e.relinquish()
			return
		}
		e.bumpLeaseFrom(start) // successful renew extends the local lease deadline
		return
	}
	start := time.Now() // before the store call, so the local deadline is conservative
	acquired, newEpoch, err := e.store.AcquireOwner(e.cfg.InstanceID, e.cfg.TTL)
	if err != nil {
		e.cfg.Logger.Warn("acquire owner", "err", err)
		return
	}
	if acquired {
		e.set(true, newEpoch)
		e.bumpLeaseFrom(start)
		e.cfg.Logger.Info("acquired control-plane ownership", "epoch", newEpoch)
		if e.cfg.OnAcquire != nil {
			e.cfg.OnAcquire(newEpoch)
		}
	}
}
