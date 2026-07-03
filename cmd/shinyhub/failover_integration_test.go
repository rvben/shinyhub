package main

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rvben/shinyhub/internal/db"
	"github.com/rvben/shinyhub/internal/dbtest"
	"github.com/rvben/shinyhub/internal/leader"
	"github.com/rvben/shinyhub/internal/worker"
)

// crashableOwnerStore wraps a real *db.Store as a leader.OwnerStore and lets a
// test simulate an instance crash: once crashed, renewals fail (the instance can
// no longer reach the DB) and it never releases the lease (a crashed process does
// not run its shutdown path), so the DB lease must expire on its own before
// another instance can take over - exactly the unplanned-crash failover path.
type crashableOwnerStore struct {
	*db.Store
	crashed atomic.Bool
}

func (c *crashableOwnerStore) AcquireOwner(id string, ttl time.Duration) (bool, int64, error) {
	if c.crashed.Load() {
		return false, 0, errors.New("crashed: db unreachable")
	}
	return c.Store.AcquireOwner(id, ttl)
}

func (c *crashableOwnerStore) RenewOwner(id string, epoch int64, ttl time.Duration) (bool, error) {
	if c.crashed.Load() {
		return false, errors.New("crashed: db unreachable")
	}
	return c.Store.RenewOwner(id, epoch, ttl)
}

func (c *crashableOwnerStore) ReleaseOwner(id string, epoch int64) error {
	if c.crashed.Load() {
		return nil // a crashed instance never releases its lease
	}
	return c.Store.ReleaseOwner(id, epoch)
}

var _ leader.OwnerStore = (*crashableOwnerStore)(nil)

func failoverWaitFor(t *testing.T, timeout time.Duration, cond func() bool, msg string) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatal(msg)
}

// TestFailover_StandbyTakesOverAndRoutes wires the REAL failover machinery -
// leader.Elector + OwnerScope, two worker.Registry instances over one shared
// db.Store, refreshUntilReady, and the ownerAndReadyPredicate gate - and drives
// an actual crash-and-takeover:
//
//  1. A acquires the lease and becomes owner-and-ready; B is a standby with its
//     gate closed.
//  2. A registers + heartbeats a worker. B's registry is stale (built before the
//     worker existed) and B's gate is closed, so B neither routes to it nor
//     admits mutations.
//  3. A crashes (renewals fail, no release). After the DB lease expires, B
//     acquires, runs the fail-closed refresh, opens its gate, and now routes to
//     the worker A registered.
//
// Runs on SQLite and, when SHINYHUB_TEST_POSTGRES_DSN is set, Postgres.
func TestFailover_StandbyTakesOverAndRoutes(t *testing.T) {
	store := dbtest.New(t)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	// The DB stores the lease expiry in whole seconds, so a sub-second TTL is
	// clamped to 1s; use a whole-second TTL so the local and DB deadlines agree.
	const (
		ttl        = 1 * time.Second
		renewEvery = 100 * time.Millisecond
	)

	// Two registries over the same shared store, both built BEFORE any worker
	// exists, so a standby's index is genuinely stale until it refreshes.
	regA, err := worker.NewRegistry(store)
	if err != nil {
		t.Fatalf("registry A: %v", err)
	}
	regB, err := worker.NewRegistry(store)
	if err != nil {
		t.Fatalf("registry B: %v", err)
	}

	var readyA, readyB atomic.Bool

	// ownerWork mirrors the production readiness portion: close the gate, run the
	// fail-closed registry refresh, open the gate, then hold until ownership is
	// lost. Uses the real refreshUntilReady + Registry.Refresh.
	makeOwnerWork := func(reg *worker.Registry, ready *atomic.Bool) func(context.Context, int64) {
		return func(octx context.Context, _ int64) {
			ready.Store(false)
			defer ready.Store(false)
			if !refreshUntilReady(octx, reg, 10*time.Millisecond, logger) {
				return
			}
			ready.Store(true)
			<-octx.Done()
		}
	}

	scopeA := leader.NewOwnerScope(makeOwnerWork(regA, &readyA))
	scopeB := leader.NewOwnerScope(makeOwnerWork(regB, &readyB))
	defer scopeA.Stop()
	defer scopeB.Stop()

	crashA := &crashableOwnerStore{Store: store}
	electorA := leader.New(crashA, leader.Config{
		InstanceID: "a", TTL: ttl, RenewEvery: renewEvery,
		OnAcquire: scopeA.Acquire, OnLose: scopeA.Lose, Logger: logger,
	})
	electorB := leader.New(store, leader.Config{
		InstanceID: "b", TTL: ttl, RenewEvery: renewEvery,
		OnAcquire: scopeB.Acquire, OnLose: scopeB.Lose, Logger: logger,
	})

	// The real production gate, for each instance.
	gateA := ownerAndReadyPredicate(electorA.IsOwner, &readyA)
	gateB := ownerAndReadyPredicate(electorB.IsOwner, &readyB)

	// Start A first and let it settle as the owner, so B is deterministically the
	// standby.
	ctxA, cancelA := context.WithCancel(context.Background())
	defer cancelA()
	go electorA.Run(ctxA)
	failoverWaitFor(t, 10*time.Second, gateA, "A never became owner-and-ready")

	ctxB, cancelB := context.WithCancel(context.Background())
	defer cancelB()
	go electorB.Run(ctxB)

	// B must be a standby while A holds a live lease: gate closed.
	if gateB() {
		t.Fatal("standby B opened its gate while A held the lease")
	}

	// A (the active) registers a worker and brings it up via a heartbeat.
	node, err := regA.Register(worker.RegisterParams{
		Name: "w1", AdvertiseAddr: "203.0.113.10:9000", Tier: "burst", Fingerprint: "fp",
	})
	if err != nil {
		t.Fatalf("register on active: %v", err)
	}
	if _, _, err := regA.Heartbeat(node.NodeID, "fp", 0); err != nil {
		t.Fatalf("heartbeat on active: %v", err)
	}
	if _, ok := regA.WorkerForTier("burst"); !ok {
		t.Fatal("active A does not route to the worker it just registered")
	}

	// Preconditions before failover: A routes and is gated open; B is stale and
	// gated closed (it neither sees the worker nor would admit mutations).
	if !gateA() {
		t.Fatal("active A gate closed unexpectedly")
	}
	if _, ok := regB.Worker(node.NodeID); ok {
		t.Fatal("standby B saw the worker before refresh (precondition: stale by design)")
	}
	if gateB() {
		t.Fatal("standby B gate open before takeover")
	}

	// Kill the active: renewals fail and it never releases. B must take over only
	// after the DB lease expires.
	crashA.crashed.Store(true)

	// B takes over: acquires the lease, runs the fail-closed refresh, opens its
	// gate. This is the core Phase 3 failover guarantee.
	failoverWaitFor(t, 10*time.Second, gateB, "standby B never became owner-and-ready after the active crashed")

	// And the new active now routes to the worker the dead active had registered -
	// proving the registry was refreshed from the shared store on takeover.
	if _, ok := regB.Worker(node.NodeID); !ok {
		t.Fatal("new active B does not see the worker after refresh")
	}
	if w, ok := regB.WorkerForTier("burst"); !ok || w.NodeID != node.NodeID {
		t.Fatalf("new active B does not route to the worker after takeover (ok=%v)", ok)
	}

	// The crashed instance must have relinquished (it cannot serve mutations as a
	// split owner once its lease deadline passed).
	failoverWaitFor(t, 5*time.Second, func() bool { return !gateA() },
		"crashed instance A never relinquished its gate")
}
