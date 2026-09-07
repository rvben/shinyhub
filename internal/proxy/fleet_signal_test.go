package proxy_test

import (
	"errors"
	"testing"

	"github.com/rvben/shinyhub/internal/autoscale"
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
	if _, err := s.CreateApp(db.CreateAppParams{Slug: slug, Name: slug, OwnerID: ownerID, Access: "private"}); err != nil {
		t.Fatalf("create app %q: %v", slug, err)
	}
	a, err := s.GetAppBySlug(slug)
	if err != nil {
		t.Fatalf("get app %q: %v", slug, err)
	}
	return a
}

// TestFleetSignal_ReturnsSumAcrossInstances verifies that ReplicaSessionCounts
// (the autoscale.Signal method) returns the per-index fleet sum from two
// instances for the same app. Rows are inserted with the DB clock (no external
// timestamp), so they are always fresh relative to the stale window.
func TestFleetSignal_ReturnsSumAcrossInstances(t *testing.T) {
	s := dbtest.New(t)
	owner := mustCreateUserFleet(t, s, "fs-owner", "developer")
	app := mustCreateAppFleet(t, s, "fs-app", owner.ID)
	appID := app.ID

	// Seed two instances into replica_sessions.
	// Instance A: replica 0 -> 3 sessions, replica 1 -> 5 sessions.
	rowsA := []db.ReplicaSessionRow{
		{AppID: appID, Idx: 0, Active: 3, LastActivityAgeSec: 0},
		{AppID: appID, Idx: 1, Active: 5, LastActivityAgeSec: 0},
	}
	if err := s.UpsertReplicaSessions("instance-A", rowsA); err != nil {
		t.Fatalf("UpsertReplicaSessions A: %v", err)
	}
	// Instance B: replica 0 -> 2 sessions (additional), replica 2 -> 7 sessions.
	rowsB := []db.ReplicaSessionRow{
		{AppID: appID, Idx: 0, Active: 2, LastActivityAgeSec: 0},
		{AppID: appID, Idx: 2, Active: 7, LastActivityAgeSec: 0},
	}
	if err := s.UpsertReplicaSessions("instance-B", rowsB); err != nil {
		t.Fatalf("UpsertReplicaSessions B: %v", err)
	}

	// Wire the proxy pool with the app's numeric ID.
	p := proxy.New()
	p.SetPoolAppID("fs-app", appID)

	sig := proxy.NewFleetSignal(p, s, nil)

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
// an updated_at outside the stale window, ReplicaSessionCounts returns an
// empty/nil slice so the autoscaler's existing early-return holds (no
// over-scale). Staleness is simulated by backdating updated_at via raw SQL.
func TestFleetSignal_StaleRows(t *testing.T) {
	s := dbtest.New(t)
	owner := mustCreateUserFleet(t, s, "fs-stale-owner", "developer")
	app := mustCreateAppFleet(t, s, "fs-stale-app", owner.ID)

	// Insert fresh rows, then backdate updated_at by 1000 seconds so they fall
	// outside the production stale window (ReplicaSessionStaleCutoff = 15 s).
	rows := []db.ReplicaSessionRow{
		{AppID: app.ID, Idx: 0, Active: 99, LastActivityAgeSec: 0},
		{AppID: app.ID, Idx: 1, Active: 99, LastActivityAgeSec: 0},
	}
	if err := s.UpsertReplicaSessions("instance-A", rows); err != nil {
		t.Fatalf("UpsertReplicaSessions: %v", err)
	}
	if _, err := s.DB().Exec(
		`UPDATE replica_sessions SET updated_at = updated_at - 1000 WHERE instance_id = ?`,
		"instance-A",
	); err != nil {
		t.Fatalf("backdate updated_at: %v", err)
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

func (e *errFleetStore) AppFleetLoad(_ int64, _ int64, _ string) (active []int64, idleSinceSec int64, err error) {
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

var _ autoscale.Signal = (*proxy.FleetSignal)(nil)
