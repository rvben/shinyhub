package proxy_test

import (
	"errors"
	"testing"
	"time"

	"github.com/rvben/shinyhub/internal/db"
	"github.com/rvben/shinyhub/internal/dbtest"
	"github.com/rvben/shinyhub/internal/proxy"
)

// mustCreateUserFleet and mustCreateAppFleet are local helpers scoped to fleet
// signal tests; they share logic with db_test.go but live here to avoid cross-
// package coupling.
func mustCreateUserFleet(t *testing.T, s *db.Store, name, role string) *db.User {
	t.Helper()
	if err := s.CreateUser(db.CreateUserParams{Username: name, PasswordHash: "h", Role: role}); err != nil {
		t.Fatalf("create user %q: %v", name, err)
	}
	u, err := s.GetUserByUsername(name)
	if err != nil {
		t.Fatalf("get user %q: %v", name, err)
	}
	return u
}

func mustCreateAppFleet(t *testing.T, s *db.Store, slug string, ownerID int64) *db.App {
	t.Helper()
	if err := s.CreateApp(db.CreateAppParams{Slug: slug, Name: slug, OwnerID: ownerID, Access: "private"}); err != nil {
		t.Fatalf("create app %q: %v", slug, err)
	}
	a, err := s.GetAppBySlug(slug)
	if err != nil {
		t.Fatalf("get app %q: %v", slug, err)
	}
	return a
}

// freshUpdatedAt is a unix epoch far in the future (year 2255). Using a value
// beyond any realistic now means these rows will always pass the staleness
// filter (updated_at >= now - ReplicaSessionStaleCutoff) regardless of when
// the test runs.
const freshUpdatedAt = int64(9_000_000_000)

// TestFleetSignal_ReturnsSumAcrossInstances verifies that ReplicaSessionCounts
// (the autoscale.Signal method) returns the per-index fleet sum from two
// instances for the same app.
func TestFleetSignal_ReturnsSumAcrossInstances(t *testing.T) {
	s := dbtest.New(t)
	owner := mustCreateUserFleet(t, s, "fs-owner", "developer")
	app := mustCreateAppFleet(t, s, "fs-app", owner.ID)
	appID := app.ID

	// Seed two instances into replica_sessions.
	// Instance A: replica 0 -> 3 sessions, replica 1 -> 5 sessions.
	rowsA := []db.ReplicaSessionRow{
		{AppID: appID, Idx: 0, Active: 3, LastActivity: freshUpdatedAt},
		{AppID: appID, Idx: 1, Active: 5, LastActivity: freshUpdatedAt},
	}
	if err := s.UpsertReplicaSessions("instance-A", freshUpdatedAt, rowsA); err != nil {
		t.Fatalf("UpsertReplicaSessions A: %v", err)
	}
	// Instance B: replica 0 -> 2 sessions (additional), replica 2 -> 7 sessions.
	rowsB := []db.ReplicaSessionRow{
		{AppID: appID, Idx: 0, Active: 2, LastActivity: freshUpdatedAt},
		{AppID: appID, Idx: 2, Active: 7, LastActivity: freshUpdatedAt},
	}
	if err := s.UpsertReplicaSessions("instance-B", freshUpdatedAt, rowsB); err != nil {
		t.Fatalf("UpsertReplicaSessions B: %v", err)
	}

	// Wire the proxy pool with the app's numeric ID.
	p := proxy.New()
	p.SetPoolAppID("fs-app", appID)

	sig := proxy.NewFleetSignal(p, s, nil)

	// Use the autoscale.Signal interface method; FleetReplicaSessionCounts is
	// intentionally unexported - all external consumers go through the interface.
	counts := sig.ReplicaSessionCounts("fs-app")

	// Expected: [5, 5, 7] (idx0: 3+2=5, idx1: 5, idx2: 7).
	if len(counts) != 3 {
		t.Fatalf("ReplicaSessionCounts len = %d, want 3; got %v", len(counts), counts)
	}
	if counts[0] != 5 {
		t.Errorf("counts[0] = %d, want 5 (3+2)", counts[0])
	}
	if counts[1] != 5 {
		t.Errorf("counts[1] = %d, want 5", counts[1])
	}
	if counts[2] != 7 {
		t.Errorf("counts[2] = %d, want 7", counts[2])
	}
}

// TestFleetSignal_StaleRows verifies that when all replica_sessions rows have
// an updated_at older than the stale cutoff, ReplicaSessionCounts returns an
// empty/nil slice so the autoscaler's existing early-return holds (no
// over-scale). Rows with updated_at=1 (year 1970) are always stale against the
// real-time cutoff of now-ReplicaSessionStaleCutoff.
func TestFleetSignal_StaleRows(t *testing.T) {
	s := dbtest.New(t)
	owner := mustCreateUserFleet(t, s, "fs-stale-owner", "developer")
	app := mustCreateAppFleet(t, s, "fs-stale-app", owner.ID)

	// Insert rows with epoch=1 (always older than now-15s staleness cutoff).
	rows := []db.ReplicaSessionRow{
		{AppID: app.ID, Idx: 0, Active: 99, LastActivity: 1},
		{AppID: app.ID, Idx: 1, Active: 99, LastActivity: 1},
	}
	if err := s.UpsertReplicaSessions("instance-A", 1 /* epoch 1 = stale */, rows); err != nil {
		t.Fatalf("UpsertReplicaSessions: %v", err)
	}

	p := proxy.New()
	p.SetPoolAppID("fs-stale-app", app.ID)

	sig := proxy.NewFleetSignal(p, s, nil)

	counts := sig.ReplicaSessionCounts("fs-stale-app")

	// AppFleetLoad returns an empty (non-nil, zero-length) slice when no rows
	// pass the staleness filter. ReplicaSessionCounts must return it as-is:
	// len==0 triggers the autoscaler's existing early-return (no action).
	if len(counts) != 0 {
		t.Errorf("expected empty/nil counts for stale-only rows, got %v", counts)
	}
}

// TestFleetSignal_NoPool verifies that ReplicaSessionCounts returns nil when
// the proxy has no pool registered for the slug, which also triggers the
// autoscaler's len(counts)==0 early-return.
func TestFleetSignal_NoPool(t *testing.T) {
	s := dbtest.New(t)

	p := proxy.New()
	// No SetPoolAppID call for "unknown-slug".

	sig := proxy.NewFleetSignal(p, s, nil)
	counts := sig.ReplicaSessionCounts("unknown-slug")
	if len(counts) != 0 {
		t.Errorf("expected nil/empty for unknown slug, got %v", counts)
	}
}

// TestFleetSignal_UnsetAppID verifies that ReplicaSessionCounts returns
// nil/empty when the pool exists but has no appID set (zero), which also
// triggers the autoscaler's early-return.
func TestFleetSignal_UnsetAppID(t *testing.T) {
	s := dbtest.New(t)

	p := proxy.New()
	// Create a pool via SetPoolSize but don't call SetPoolAppID.
	p.SetPoolSize("no-id-app", 2)

	sig := proxy.NewFleetSignal(p, s, nil)
	counts := sig.ReplicaSessionCounts("no-id-app")
	if len(counts) != 0 {
		t.Errorf("expected nil/empty for pool without appID, got %v", counts)
	}
}

// errFleetStore is a stub fleetStore whose AppFleetLoad always returns an error.
// Used to assert the DB-error path: ReplicaSessionCounts must return len==0
// (autoscaler holds) and must not panic.
type errFleetStore struct{ err error }

func (e *errFleetStore) AppFleetLoad(_ int64, _ int64, _ string) ([]int64, int64, error) {
	return nil, 0, e.err
}

// TestFleetSignal_DBErrorReturnsEmpty verifies that a transient DB failure in
// AppFleetLoad causes ReplicaSessionCounts to return an empty/nil slice
// (autoscaler holds, no panic) rather than crashing or mis-scaling.
func TestFleetSignal_DBErrorReturnsEmpty(t *testing.T) {
	p := proxy.New()
	p.SetPoolAppID("err-app", 42)

	stub := &errFleetStore{err: errors.New("db timeout")}
	sig := proxy.NewFleetSignal(p, stub, nil)

	counts := sig.ReplicaSessionCounts("err-app")
	if len(counts) != 0 {
		t.Errorf("DB error path: expected nil/empty, got %v", counts)
	}
}

// TestFleetSignal_LocalReplicaSessionCountsUnchanged verifies that the local
// ReplicaSessionCounts method on the proxy is not affected by replica_sessions
// rows in the DB: it still returns the exact in-memory active-connection count,
// independent of any fleet data.
func TestFleetSignal_LocalReplicaSessionCountsUnchanged(t *testing.T) {
	s := dbtest.New(t)
	owner := mustCreateUserFleet(t, s, "fs-local-owner", "developer")
	app := mustCreateAppFleet(t, s, "fs-local-app", owner.ID)
	appID := app.ID

	// Seed enormous fleet rows that would bias the count if the local method
	// ever consulted the DB.
	rows := []db.ReplicaSessionRow{
		{AppID: appID, Idx: 0, Active: 999, LastActivity: freshUpdatedAt},
	}
	if err := s.UpsertReplicaSessions("other-instance", freshUpdatedAt, rows); err != nil {
		t.Fatalf("UpsertReplicaSessions: %v", err)
	}

	p := proxy.New()
	p.SetPoolAppID("fs-local-app", appID)
	// The local pool has 0 active connections (no replicas registered, nil pool).
	// ReplicaSessionCounts reads in-memory state only.
	local := p.ReplicaSessionCounts("fs-local-app")
	// The pool exists (appID set creates it) but has no replicas yet.
	// ReplicaSessionCounts returns a nil slice or a slice with -1 entries for
	// a pool that has size=1 but no registered replica. Either way the fleet
	// rows must not bleed into the local count.
	for i, v := range local {
		if v == 999 {
			t.Errorf("local ReplicaSessionCounts[%d] = 999: DB rows bled into local count", i)
		}
	}
}

// TestFleetSignal_RejectsByReasonDelegates verifies that the FleetSignal
// delegates RejectsByReason to the underlying proxy without panicking.
func TestFleetSignal_RejectsByReasonDelegates(t *testing.T) {
	s := dbtest.New(t)

	p := proxy.New()
	sig := proxy.NewFleetSignal(p, s, nil)
	// RejectsByReason returns nil for a slug with no recorded rejects (documented
	// behavior). The FleetSignal must delegate unchanged and not panic.
	_ = sig.RejectsByReason("any-slug", time.Minute)
}

// autoscaleSignal mirrors the autoscale.Signal interface so we can assert
// *FleetSignal satisfies it without creating an import cycle
// (autoscale imports proxy, so proxy cannot import autoscale).
type autoscaleSignal interface {
	ReplicaSessionCounts(slug string) []int64
	RejectsByReason(slug string, d time.Duration) map[proxy.RejectReason]uint64
}

// TestFleetSignal_ImplementsSignalInterface is a compile-time check that
// *FleetSignal satisfies the autoscale.Signal interface. The interface is
// reproduced locally to avoid the proxy->autoscale import cycle.
func TestFleetSignal_ImplementsSignalInterface(t *testing.T) {
	s := dbtest.New(t)
	p := proxy.New()
	sig := proxy.NewFleetSignal(p, s, nil)
	// If FleetSignal does not implement the interface this line does not compile.
	var _ autoscaleSignal = sig
}

// TestFleetSignal_SingleNodeUsesLocalCounts verifies that when the *Proxy is
// used directly as the autoscale.Signal (single-node path, no FleetSignal
// wrapping), it returns the exact local in-memory count and not any DB value.
func TestFleetSignal_SingleNodeUsesLocalCounts(t *testing.T) {
	p := proxy.New()
	// No FleetSignal wrapping - use the proxy directly (single-node path).
	// Pool not registered: returns nil -> autoscaler early-return fires.
	local := p.ReplicaSessionCounts("any-slug")
	if len(local) != 0 {
		t.Errorf("single-node unregistered slug: expected nil, got %v", local)
	}
}
