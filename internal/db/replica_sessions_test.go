package db_test

import (
	"testing"

	"github.com/rvben/shinyhub/internal/db"
	"github.com/rvben/shinyhub/internal/dbtest"
)

// TestReplicaSessions exercises UpsertReplicaSessions, AppFleetLoad, and
// ReapStaleReplicaSessions on both SQLite (always) and Postgres (when
// SHINYHUB_TEST_POSTGRES_DSN is set, handled transparently by dbtest.New).
func TestReplicaSessions(t *testing.T) {
	s := dbtest.New(t)

	owner := mustCreateUser(t, s, "rs-owner", "developer")
	app := mustCreateApp(t, s, "rs-app", owner.ID)
	appID := app.ID

	const (
		instA = "instance-A"
		instB = "instance-B"
	)

	// Base epoch: rows from instA are "fresh" (updated_at = freshEpoch) and from
	// instB will be staged as stale later. Use a fixed epoch well in the past so
	// the test is deterministic.
	const freshEpoch = int64(1_750_000_000)
	const staleEpoch = int64(1_000_000_000)
	const cutoff = int64(1_500_000_000) // freshEpoch > cutoff, staleEpoch < cutoff

	// -- Upsert two instances for the same app --
	// instA covers replica indexes 0 and 1.
	rowsA := []db.ReplicaSessionRow{
		{AppID: appID, Idx: 0, Active: 3, LastActivity: freshEpoch - 10},
		{AppID: appID, Idx: 1, Active: 5, LastActivity: freshEpoch - 20},
	}
	if err := s.UpsertReplicaSessions(instA, freshEpoch, rowsA); err != nil {
		t.Fatalf("UpsertReplicaSessions instA: %v", err)
	}
	// instB covers replica indexes 0 and 2.
	rowsB := []db.ReplicaSessionRow{
		{AppID: appID, Idx: 0, Active: 2, LastActivity: freshEpoch - 5},
		{AppID: appID, Idx: 2, Active: 7, LastActivity: freshEpoch - 1},
	}
	if err := s.UpsertReplicaSessions(instB, freshEpoch, rowsB); err != nil {
		t.Fatalf("UpsertReplicaSessions instB: %v", err)
	}

	// -- AppFleetLoad("") sums across ALL instances --
	// idx 0: 3 + 2 = 5, idx 1: 5, idx 2: 7
	// maxLastActivity: freshEpoch-1 (from instB idx 2)
	active, maxLast, err := s.AppFleetLoad(appID, cutoff, "")
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
	wantMax := freshEpoch - 1
	if maxLast != wantMax {
		t.Errorf("maxLastActivity = %d, want %d", maxLast, wantMax)
	}

	// -- AppFleetLoad(excludeInstanceID = instB) excludes instB's rows --
	// Only instA rows remain: idx 0=3, idx 1=5. Max last_activity = freshEpoch-10.
	active2, maxLast2, err := s.AppFleetLoad(appID, cutoff, instB)
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
	if maxLast2 != freshEpoch-10 {
		t.Errorf("maxLast2 (excl instB) = %d, want %d", maxLast2, freshEpoch-10)
	}

	// -- Stale instance: insert instC rows with old updated_at --
	const instC = "instance-C"
	rowsC := []db.ReplicaSessionRow{
		{AppID: appID, Idx: 0, Active: 99, LastActivity: staleEpoch},
	}
	if err := s.UpsertReplicaSessions(instC, staleEpoch, rowsC); err != nil {
		t.Fatalf("UpsertReplicaSessions instC (stale): %v", err)
	}

	// AppFleetLoad with cutoff excludes instC (staleEpoch < cutoff).
	// instA(idx0=3,idx1=5) + instB(idx0=2,idx2=7); max idx=2 so slice len=3.
	active3, _, err := s.AppFleetLoad(appID, cutoff, "")
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

	// -- ReapStaleReplicaSessions removes only instC's rows (staleEpoch < cutoff) --
	if err := s.ReapStaleReplicaSessions(cutoff); err != nil {
		t.Fatalf("ReapStaleReplicaSessions: %v", err)
	}
	// After reap: instC rows gone, instA + instB remain. Max idx=2, slice len=3.
	// Max last_activity is still freshEpoch-1 (from instB idx 2).
	active4, maxLast4, err := s.AppFleetLoad(appID, cutoff, "")
	if err != nil {
		t.Fatalf("AppFleetLoad after reap: %v", err)
	}
	if len(active4) != 3 {
		t.Fatalf("active4 slice len = %d, want 3", len(active4))
	}
	if active4[0] != 5 {
		t.Errorf("active4[0] after reap = %d, want 5", active4[0])
	}
	if maxLast4 != freshEpoch-1 {
		t.Errorf("maxLast4 after reap = %d, want %d", maxLast4, freshEpoch-1)
	}

	// -- Sparse idx gap: instA covers idx 0 and 4 (skip 1,2,3) --
	const instD = "instance-D"
	rowsD := []db.ReplicaSessionRow{
		{AppID: appID, Idx: 0, Active: 1, LastActivity: freshEpoch},
		{AppID: appID, Idx: 4, Active: 9, LastActivity: freshEpoch},
	}
	if err := s.UpsertReplicaSessions(instD, freshEpoch, rowsD); err != nil {
		t.Fatalf("UpsertReplicaSessions instD (sparse): %v", err)
	}
	activeD, _, err := s.AppFleetLoad(appID, cutoff, instA)
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
		{AppID: appID, Idx: 0, Active: 10, LastActivity: freshEpoch + 100},
	}
	if err := s.UpsertReplicaSessions(instA, freshEpoch+100, rowsA2); err != nil {
		t.Fatalf("UpsertReplicaSessions instA re-upsert: %v", err)
	}
	// Only the idx=0 row from instA was re-upserted; the idx=1 row from instA
	// still exists. Exclude everything but instA to verify.
	activeRe, _, err := s.AppFleetLoad(appID, cutoff, instB)
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
// returns an empty (zero-length, non-nil) slice and maxLastActivity=0.
func TestAppFleetLoad_Empty(t *testing.T) {
	s := dbtest.New(t)
	owner := mustCreateUser(t, s, "rs-empty-owner", "developer")
	app := mustCreateApp(t, s, "rs-empty-app", owner.ID)

	active, maxLast, err := s.AppFleetLoad(app.ID, 0, "")
	if err != nil {
		t.Fatalf("AppFleetLoad empty: %v", err)
	}
	if active == nil {
		t.Error("active slice is nil, want non-nil empty slice")
	}
	if len(active) != 0 {
		t.Errorf("active slice len = %d, want 0", len(active))
	}
	if maxLast != 0 {
		t.Errorf("maxLastActivity = %d, want 0", maxLast)
	}
}
