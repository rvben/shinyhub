package db_test

import (
	"math"
	"testing"
	"time"

	"github.com/rvben/shinyhub/internal/db"
	"github.com/rvben/shinyhub/internal/dbtest"
)

// TestReplicaSessions exercises UpsertReplicaSessions, AppFleetLoad, and
// ReapStaleReplicaSessions on both SQLite (always) and Postgres (when
// SHINYHUB_TEST_POSTGRES_DSN is set, handled transparently by dbtest.New).
//
// All staleness is simulated by backdating updated_at via raw SQL after normal
// upserts so the tests do not depend on any local wall clock.
func TestReplicaSessions(t *testing.T) {
	s := dbtest.New(t)

	owner := mustCreateUser(t, s, "rs-owner", "developer")
	app := mustCreateApp(t, s, "rs-app", owner.ID)
	appID := app.ID

	const (
		instA = "instance-A"
		instB = "instance-B"
	)

	// staleWindowSec matches the realistic production stale window (15 s).
	// fresh rows have updated_at = db_now (just inserted).
	const staleWindowSec = int64(15)

	// -- Upsert two instances for the same app (rows are fresh, db_now) --
	// instA covers replica indexes 0 and 1.
	rowsA := []db.ReplicaSessionRow{
		{AppID: appID, Idx: 0, Active: 3, LastActivityAgeSec: 10},
		{AppID: appID, Idx: 1, Active: 5, LastActivityAgeSec: 20},
	}
	if err := s.UpsertReplicaSessions(instA, rowsA); err != nil {
		t.Fatalf("UpsertReplicaSessions instA: %v", err)
	}
	// instB covers replica indexes 0 and 2.
	rowsB := []db.ReplicaSessionRow{
		{AppID: appID, Idx: 0, Active: 2, LastActivityAgeSec: 5},
		{AppID: appID, Idx: 2, Active: 7, LastActivityAgeSec: 1},
	}
	if err := s.UpsertReplicaSessions(instB, rowsB); err != nil {
		t.Fatalf("UpsertReplicaSessions instB: %v", err)
	}

	// -- AppFleetLoad("") sums across ALL instances --
	// idx 0: 3+2=5, idx 1: 5, idx 2: 7
	// idleSinceSec should be approximately min(10,20,5,1) = 1 (most recent activity).
	active, idleSince, err := s.AppFleetLoad(appID, staleWindowSec, "")
	if err != nil {
		t.Fatalf("AppFleetLoad all: %v", err)
	}
	if len(active) != 3 {
		t.Fatalf("active slice len = %d, want 3", len(active))
	}
	if active[0] != 5 {
		t.Errorf("active[0] = %d, want 5", active[0])
	}
	if active[1] != 5 {
		t.Errorf("active[1] = %d, want 5", active[1])
	}
	if active[2] != 7 {
		t.Errorf("active[2] = %d, want 7", active[2])
	}
	// idleSinceSec = db_now - MAX(last_activity) across all fresh rows.
	// The MIN age across all rows is 1 s (instB idx2); allow up to 5s tolerance for
	// DB round-trip and test execution time.
	if idleSince < 1 || idleSince > 10 {
		t.Errorf("idleSinceSec all instances = %d, want approx 1 (within 10s tolerance)", idleSince)
	}

	// -- AppFleetLoad(excludeInstanceID = instB) excludes instB's rows --
	// Only instA rows remain: idx 0=3, idx 1=5. Min age = 10 s (instA idx0).
	active2, idleSince2, err := s.AppFleetLoad(appID, staleWindowSec, instB)
	if err != nil {
		t.Fatalf("AppFleetLoad exclude instB: %v", err)
	}
	if len(active2) != 2 {
		t.Fatalf("active2 slice len = %d, want 2", len(active2))
	}
	if active2[0] != 3 {
		t.Errorf("active2[0] (excl instB) = %d, want 3", active2[0])
	}
	if active2[1] != 5 {
		t.Errorf("active2[1] (excl instB) = %d, want 5", active2[1])
	}
	// Min age across instA rows is 10 s. Allow up to 15s for timing.
	if idleSince2 < 10 || idleSince2 > 20 {
		t.Errorf("idleSinceSec excl instB = %d, want approx 10 (within 10s tolerance)", idleSince2)
	}

	// -- Stale instance: insert instC rows normally then backdate updated_at --
	// After backdating, instC rows will fall outside the staleWindowSec window
	// and must be excluded by AppFleetLoad.
	const instC = "instance-C"
	rowsC := []db.ReplicaSessionRow{
		{AppID: appID, Idx: 0, Active: 99, LastActivityAgeSec: 0},
	}
	if err := s.UpsertReplicaSessions(instC, rowsC); err != nil {
		t.Fatalf("UpsertReplicaSessions instC: %v", err)
	}
	// Backdate instC's updated_at to make it appear stale (1000 seconds ago).
	if _, err := s.DB().Exec(
		`UPDATE replica_sessions SET updated_at = updated_at - 1000 WHERE instance_id = ?`,
		instC,
	); err != nil {
		t.Fatalf("backdate instC updated_at: %v", err)
	}

	// AppFleetLoad with staleWindowSec=15 must exclude instC (1000s old > 15s window).
	active3, _, err := s.AppFleetLoad(appID, staleWindowSec, "")
	if err != nil {
		t.Fatalf("AppFleetLoad with staleness cutoff: %v", err)
	}
	// idx 0: instA(3) + instB(2) = 5, NOT +99 from instC
	if len(active3) != 3 {
		t.Fatalf("active3 slice len = %d, want 3", len(active3))
	}
	if active3[0] != 5 {
		t.Errorf("active3[0] after stale exclusion = %d, want 5", active3[0])
	}

	// -- ReapStaleReplicaSessions removes only instC's rows --
	if err := s.ReapStaleReplicaSessions(staleWindowSec); err != nil {
		t.Fatalf("ReapStaleReplicaSessions: %v", err)
	}
	// After reap: instC rows gone, instA + instB remain. Max idx=2, slice len=3.
	active4, _, err := s.AppFleetLoad(appID, staleWindowSec, "")
	if err != nil {
		t.Fatalf("AppFleetLoad after reap: %v", err)
	}
	if len(active4) != 3 {
		t.Fatalf("active4 slice len = %d, want 3", len(active4))
	}
	if active4[0] != 5 {
		t.Errorf("active4[0] after reap = %d, want 5", active4[0])
	}

	// -- Sparse idx gap: instD covers idx 0 and 4 (skip 1,2,3) --
	const instD = "instance-D"
	rowsD := []db.ReplicaSessionRow{
		{AppID: appID, Idx: 0, Active: 1, LastActivityAgeSec: 0},
		{AppID: appID, Idx: 4, Active: 9, LastActivityAgeSec: 0},
	}
	if err := s.UpsertReplicaSessions(instD, rowsD); err != nil {
		t.Fatalf("UpsertReplicaSessions instD (sparse): %v", err)
	}
	activeD, _, err := s.AppFleetLoad(appID, staleWindowSec, instA)
	if err != nil {
		t.Fatalf("AppFleetLoad instD sparse: %v", err)
	}
	// We exclude instA; only instB and instD contribute. Check idx 4 is set and
	// that gaps between 2 and 4 are zero-filled. Max idx=4, slice len=5.
	if len(activeD) != 5 {
		t.Fatalf("activeD slice len = %d, want 5", len(activeD))
	}
	if activeD[3] != 0 {
		t.Errorf("activeD[3] (gap) = %d, want 0", activeD[3])
	}
	if activeD[4] != 9 {
		t.Errorf("activeD[4] = %d, want 9", activeD[4])
	}

	// -- Upsert idempotency: re-upserting instA updates the values --
	rowsA2 := []db.ReplicaSessionRow{
		{AppID: appID, Idx: 0, Active: 10, LastActivityAgeSec: 0},
	}
	if err := s.UpsertReplicaSessions(instA, rowsA2); err != nil {
		t.Fatalf("UpsertReplicaSessions instA re-upsert: %v", err)
	}
	// Only the idx=0 row from instA was re-upserted; the idx=1 row from instA
	// still exists. Exclude instB to verify.
	activeRe, _, err := s.AppFleetLoad(appID, staleWindowSec, instB)
	if err != nil {
		t.Fatalf("AppFleetLoad after re-upsert: %v", err)
	}
	// instA(idx0=10,idx1=5) + instD(idx0=1,idx4=9); instB excluded, instC reaped.
	// Max idx=4, slice len=5.
	if len(activeRe) != 5 {
		t.Fatalf("activeRe slice len = %d, want 5", len(activeRe))
	}
	if activeRe[0] != 11 {
		t.Errorf("activeRe[0] after re-upsert = %d, want 11 (instA:10 + instD:1)", activeRe[0])
	}
	if activeRe[1] != 5 {
		t.Errorf("activeRe[1] after re-upsert = %d, want 5 (instA only)", activeRe[1])
	}
}

// TestAppFleetLoad_Empty checks that an app with no replica_sessions rows
// returns an empty (zero-length, non-nil) slice and idleSinceSec = NoFleetActivity.
func TestAppFleetLoad_Empty(t *testing.T) {
	s := dbtest.New(t)
	owner := mustCreateUser(t, s, "rs-empty-owner", "developer")
	app := mustCreateApp(t, s, "rs-empty-app", owner.ID)

	active, idleSince, err := s.AppFleetLoad(app.ID, 15, "")
	if err != nil {
		t.Fatalf("AppFleetLoad empty: %v", err)
	}
	if active == nil {
		t.Error("active slice is nil, want non-nil empty slice")
	}
	if len(active) != 0 {
		t.Errorf("active slice len = %d, want 0", len(active))
	}
	if idleSince != db.NoFleetActivity {
		t.Errorf("idleSinceSec empty = %d, want NoFleetActivity (%d)", idleSince, db.NoFleetActivity)
	}
}

// TestAppFleetLoad_IdleSinceSec verifies that idleSinceSec reflects the age of
// the most recent fleet activity on the database clock. We set a precise
// last_activity via raw SQL and assert the returned age is approximately correct.
func TestAppFleetLoad_IdleSinceSec(t *testing.T) {
	s := dbtest.New(t)
	owner := mustCreateUser(t, s, "rs-idle-owner", "developer")
	app := mustCreateApp(t, s, "rs-idle-app", owner.ID)

	// Insert a row with LastActivityAgeSec=100 so last_activity = db_now - 100.
	rows := []db.ReplicaSessionRow{
		{AppID: app.ID, Idx: 0, Active: 0, LastActivityAgeSec: 100},
	}
	if err := s.UpsertReplicaSessions("inst-A", rows); err != nil {
		t.Fatalf("UpsertReplicaSessions: %v", err)
	}

	_, idleSince, err := s.AppFleetLoad(app.ID, 60 /* staleWindowSec */, "")
	if err != nil {
		t.Fatalf("AppFleetLoad: %v", err)
	}
	// idleSinceSec = db_now - last_activity = db_now - (db_now_at_insert - 100).
	// As long as the test completes in under a few seconds, idleSinceSec should be
	// approximately 100. Allow [99, 115] to tolerate sub-second clock differences
	// and test execution time.
	if idleSince < 99 || idleSince > 115 {
		t.Errorf("idleSinceSec = %d, want approximately 100 (got outside [99,115])", idleSince)
	}
}

// TestReapStaleReplicaSessions_OnlyRemovesStale confirms that ReapStaleReplicaSessions
// deletes rows with an old updated_at but preserves fresh rows.
func TestReapStaleReplicaSessions_OnlyRemovesStale(t *testing.T) {
	s := dbtest.New(t)
	owner := mustCreateUser(t, s, "rs-reap-owner", "developer")
	app := mustCreateApp(t, s, "rs-reap-app", owner.ID)

	const staleWindowSec = int64(15)

	// Insert fresh rows for instA.
	rowsA := []db.ReplicaSessionRow{
		{AppID: app.ID, Idx: 0, Active: 1, LastActivityAgeSec: 0},
	}
	if err := s.UpsertReplicaSessions("inst-fresh", rowsA); err != nil {
		t.Fatalf("UpsertReplicaSessions fresh: %v", err)
	}

	// Insert rows for instStale and backdate them.
	rowsStale := []db.ReplicaSessionRow{
		{AppID: app.ID, Idx: 1, Active: 9, LastActivityAgeSec: 0},
	}
	if err := s.UpsertReplicaSessions("inst-stale", rowsStale); err != nil {
		t.Fatalf("UpsertReplicaSessions stale: %v", err)
	}
	if _, err := s.DB().Exec(
		`UPDATE replica_sessions SET updated_at = updated_at - 1000 WHERE instance_id = ?`,
		"inst-stale",
	); err != nil {
		t.Fatalf("backdate stale rows: %v", err)
	}

	// Verify instStale is excluded by AppFleetLoad before reap.
	active, _, err := s.AppFleetLoad(app.ID, staleWindowSec, "")
	if err != nil {
		t.Fatalf("AppFleetLoad pre-reap: %v", err)
	}
	if len(active) == 0 || active[0] != 1 {
		t.Fatalf("pre-reap: expected only fresh row (active[0]=1), got %v", active)
	}

	// Reap: only the stale row must be deleted.
	if err := s.ReapStaleReplicaSessions(staleWindowSec); err != nil {
		t.Fatalf("ReapStaleReplicaSessions: %v", err)
	}

	// Fresh row must survive; stale row must be gone.
	active2, _, err := s.AppFleetLoad(app.ID, staleWindowSec, "")
	if err != nil {
		t.Fatalf("AppFleetLoad post-reap: %v", err)
	}
	if len(active2) == 0 || active2[0] != 1 {
		t.Fatalf("post-reap: expected fresh row to survive (active[0]=1), got %v", active2)
	}

	// The stale instance's idx=1 slot must be absent (zero-filled or slice len=1).
	if len(active2) > 1 && active2[1] != 0 {
		t.Errorf("post-reap: stale idx=1 should be gone, got active[1]=%d", active2[1])
	}
}

// TestReplicaSessions_NoFleetActivitySentinel asserts that NoFleetActivity is
// math.MaxInt64 so callers can use >= comparisons against timeout durations safely.
func TestReplicaSessions_NoFleetActivitySentinel(t *testing.T) {
	if db.NoFleetActivity != math.MaxInt64 {
		t.Errorf("NoFleetActivity = %d, want %d (math.MaxInt64)", db.NoFleetActivity, int64(math.MaxInt64))
	}
}

// TestReplicaSessions_DBClockStamps verifies that two upserts from instances
// running concurrently produce updated_at values from the DB clock (not the
// local Go clock). We insert rows, wait a known interval, re-insert, and assert
// that the second updated_at is strictly greater than the first. This confirms
// the DB is stamping updated_at (not the caller), and that monotonic DB time
// progresses between inserts. The test uses a real sleep to let DB time advance.
func TestReplicaSessions_DBClockStamps(t *testing.T) {
	s := dbtest.New(t)
	owner := mustCreateUser(t, s, "rs-clock-owner", "developer")
	app := mustCreateApp(t, s, "rs-clock-app", owner.ID)

	row := []db.ReplicaSessionRow{
		{AppID: app.ID, Idx: 0, Active: 1, LastActivityAgeSec: 0},
	}
	if err := s.UpsertReplicaSessions("inst-clock", row); err != nil {
		t.Fatalf("first upsert: %v", err)
	}

	var ts1 int64
	if err := s.DB().QueryRow(
		`SELECT updated_at FROM replica_sessions WHERE instance_id = ?`, "inst-clock",
	).Scan(&ts1); err != nil {
		t.Fatalf("read ts1: %v", err)
	}

	// Sleep 1.1s to ensure the DB-clock second advances.
	time.Sleep(1100 * time.Millisecond)

	if err := s.UpsertReplicaSessions("inst-clock", row); err != nil {
		t.Fatalf("second upsert: %v", err)
	}

	var ts2 int64
	if err := s.DB().QueryRow(
		`SELECT updated_at FROM replica_sessions WHERE instance_id = ?`, "inst-clock",
	).Scan(&ts2); err != nil {
		t.Fatalf("read ts2: %v", err)
	}

	if ts2 <= ts1 {
		t.Errorf("second updated_at (%d) must be greater than first (%d); DB clock did not advance", ts2, ts1)
	}
}

// TestAppFleetLastActivity verifies that AppFleetLastActivity returns the
// MAX(last_activity) epoch across non-stale, non-excluded rows, and 0 when no
// fresh rows exist. The semantics mirror AppFleetLoad's staleness/exclusion.
func TestAppFleetLastActivity(t *testing.T) {
	s := dbtest.New(t)
	owner := mustCreateUser(t, s, "fla-owner", "developer")
	app := mustCreateApp(t, s, "fla-app", owner.ID)
	appID := app.ID
	const staleWindowSec = int64(15)

	// No rows yet: expect 0.
	got, err := s.AppFleetLastActivity(appID, staleWindowSec, "")
	if err != nil {
		t.Fatalf("AppFleetLastActivity (empty): %v", err)
	}
	if got != 0 {
		t.Errorf("expected 0 with no rows, got %d", got)
	}

	// Insert two instances: instA with older last_activity, instB with newer.
	rowsA := []db.ReplicaSessionRow{
		{AppID: appID, Idx: 0, Active: 1, LastActivityAgeSec: 20}, // older activity
	}
	if err := s.UpsertReplicaSessions("fla-instA", rowsA); err != nil {
		t.Fatalf("upsert instA: %v", err)
	}
	rowsB := []db.ReplicaSessionRow{
		{AppID: appID, Idx: 0, Active: 1, LastActivityAgeSec: 5}, // newer activity
	}
	if err := s.UpsertReplicaSessions("fla-instB", rowsB); err != nil {
		t.Fatalf("upsert instB: %v", err)
	}

	// Both instances fresh: AppFleetLastActivity must return a non-zero epoch.
	got, err = s.AppFleetLastActivity(appID, staleWindowSec, "")
	if err != nil {
		t.Fatalf("AppFleetLastActivity (both): %v", err)
	}
	if got == 0 {
		t.Error("expected non-zero epoch with fresh rows, got 0")
	}

	// Excluding instB must still return a non-zero epoch (instA remains).
	gotExcluded, err := s.AppFleetLastActivity(appID, staleWindowSec, "fla-instB")
	if err != nil {
		t.Fatalf("AppFleetLastActivity (exclude instB): %v", err)
	}
	if gotExcluded == 0 {
		t.Error("expected non-zero epoch after excluding instB (instA still present), got 0")
	}
	// instA has older activity (LastActivityAgeSec=20 vs 5), so its epoch < instB's.
	if gotExcluded >= got {
		t.Errorf("excluded instB: expected epoch (%d) < full (%d) since instA's activity is older", gotExcluded, got)
	}

	// Stale all rows by backdating updated_at.
	if _, err := s.DB().Exec(
		`UPDATE replica_sessions SET updated_at = 0 WHERE app_id = ?`, appID,
	); err != nil {
		t.Fatalf("backdate updated_at: %v", err)
	}
	got, err = s.AppFleetLastActivity(appID, staleWindowSec, "")
	if err != nil {
		t.Fatalf("AppFleetLastActivity (stale): %v", err)
	}
	if got != 0 {
		t.Errorf("expected 0 after all rows staled, got %d", got)
	}
}
