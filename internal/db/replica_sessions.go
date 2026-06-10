package db

import (
	"fmt"
	"math"
)

// ReplicaSessionRow holds the per-(app_id, replica-index) session data that
// one instance reports for a batch upsert. All timestamp semantics use the
// database clock so the signal is robust to control-plane clock skew.
type ReplicaSessionRow struct {
	AppID  int64
	Idx    int
	Active int64
	// LastActivityAgeSec is the number of seconds since this replica last saw
	// activity on the reporting instance, computed as a skew-independent
	// duration: max(0, local_now - local_lastSeen). The DB stores last_activity
	// as (db_now - LastActivityAgeSec), placing it on the shared database clock
	// so any instance can compare it against the DB clock without clock skew.
	LastActivityAgeSec int64
}

// UpsertReplicaSessions batch-upserts the caller's per-(app_id, idx) session
// counts into the replica_sessions table. The database clock stamps every row:
//
//   - updated_at = db_now (heartbeat freshness on the DB clock).
//   - last_activity = db_now - LastActivityAgeSec (age duration applied to the
//     DB clock so idle comparisons across instances never mix wall clocks).
//
// Rows already present for (instance_id, app_id, idx) are overwritten; rows
// not present are created.
func (s *Store) UpsertReplicaSessions(instanceID string, rows []ReplicaSessionRow) error {
	if len(rows) == 0 {
		return nil
	}
	dbNow := s.d.nowEpoch()
	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("upsert replica sessions begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	for _, r := range rows {
		q := `
			INSERT INTO replica_sessions (instance_id, app_id, idx, active, last_activity, updated_at)
			VALUES (?, ?, ?, ?, ` + dbNow + ` - ?, ` + dbNow + `)
			ON CONFLICT(instance_id, app_id, idx) DO UPDATE SET
				active        = excluded.active,
				last_activity = excluded.last_activity,
				updated_at    = excluded.updated_at`
		if _, err := tx.Exec(q,
			instanceID, r.AppID, r.Idx, r.Active, r.LastActivityAgeSec,
		); err != nil {
			return fmt.Errorf("upsert replica session (instance=%s app=%d idx=%d): %w",
				instanceID, r.AppID, r.Idx, err)
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("upsert replica sessions commit: %w", err)
	}
	return nil
}

// NoFleetActivity is the sentinel value returned by AppFleetLoad as idleSinceSec
// when no fresh rows exist. Callers treat this as "infinitely idle" - the fleet
// has no live peer data, so the idle condition is satisfied.
const NoFleetActivity = int64(math.MaxInt64)

// AppFleetLoad aggregates the active session counts reported by all non-stale,
// non-excluded instances for the given app. All freshness and idle comparisons
// are on the database clock, so the result is robust to control-plane clock skew.
//
// It considers only rows where:
//   - app_id  = appID
//   - updated_at >= (db_now - staleWindowSec)  (rows older than this window are
//     treated as from a crashed/unreachable instance and are ignored)
//   - instance_id != excludeInstanceID (pass "" to include every instance)
//
// The returned active slice is sized to cover indexes 0..maxIdx (the highest
// replica index seen in the matching rows). active[i] is the sum of Active
// across all contributing instances for replica index i; gaps not reported
// by any instance are zero-filled.
//
// idleSinceSec is db_now - MAX(last_activity) over all matching rows: the
// number of seconds since the most recent fleet activity, measured entirely
// on the database clock. If there are no fresh rows, NoFleetActivity is
// returned so callers treat "no live peer data" as idle.
func (s *Store) AppFleetLoad(appID int64, staleWindowSec int64, excludeInstanceID string) (active []int64, idleSinceSec int64, err error) {
	dbNow := s.d.nowEpoch()
	q := `
		SELECT idx, SUM(active), ` + dbNow + ` - MAX(last_activity)
		FROM replica_sessions
		WHERE app_id   = ?
		  AND updated_at >= (` + dbNow + ` - ?)
		  AND (? = '' OR instance_id != ?)
		GROUP BY idx
		ORDER BY idx`
	rows, err := s.db.Query(q,
		appID, staleWindowSec, excludeInstanceID, excludeInstanceID,
	)
	if err != nil {
		return nil, 0, fmt.Errorf("app fleet load query: %w", err)
	}
	defer rows.Close()

	type idxRow struct {
		idx            int
		sumActive      int64
		ageSinceMaxAct int64
	}
	var results []idxRow
	var globalMaxAgeSec int64

	for rows.Next() {
		var r idxRow
		if err := rows.Scan(&r.idx, &r.sumActive, &r.ageSinceMaxAct); err != nil {
			return nil, 0, fmt.Errorf("app fleet load scan: %w", err)
		}
		results = append(results, r)
		// ageSinceMaxAct is db_now - MAX(last_activity) for this idx group.
		// Across idx groups, we want the minimum age (most recent activity fleet-wide).
		if len(results) == 1 || r.ageSinceMaxAct < globalMaxAgeSec {
			globalMaxAgeSec = r.ageSinceMaxAct
		}
	}
	if err := rows.Err(); err != nil {
		return nil, 0, fmt.Errorf("app fleet load rows: %w", err)
	}

	if len(results) == 0 {
		return []int64{}, NoFleetActivity, nil
	}

	maxIdx := results[len(results)-1].idx
	out := make([]int64, maxIdx+1)
	for _, r := range results {
		out[r.idx] = r.sumActive
	}
	return out, globalMaxAgeSec, nil
}

// AppFleetLastActivity returns the MAX(last_activity) Unix epoch across all
// non-stale, non-excluded replica_sessions rows for appID. It uses the same
// staleness and exclusion semantics as AppFleetLoad. Returns 0 when no fresh
// rows exist (no live peer data). All values are on the database clock so
// cross-instance comparisons are robust to control-plane clock skew.
func (s *Store) AppFleetLastActivity(appID int64, staleWindowSec int64, excludeInstanceID string) (int64, error) {
	dbNow := s.d.nowEpoch()
	q := s.d.rebind(`
		SELECT COALESCE(MAX(last_activity), 0)
		FROM replica_sessions
		WHERE app_id   = ?
		  AND updated_at >= (` + dbNow + ` - ?)
		  AND (? = '' OR instance_id != ?)`)
	var maxLastActivity int64
	err := s.db.QueryRow(q, appID, staleWindowSec, excludeInstanceID, excludeInstanceID).Scan(&maxLastActivity)
	if err != nil {
		return 0, fmt.Errorf("app fleet last activity: %w", err)
	}
	return maxLastActivity, nil
}

// ReapStaleReplicaSessions deletes rows whose updated_at is older than
// staleWindowSec seconds ago on the database clock. This removes the footprint
// of crashed or restarted instances that are no longer reporting. All time
// comparisons are on the database clock so the reaper is not affected by
// control-plane clock skew.
func (s *Store) ReapStaleReplicaSessions(staleWindowSec int64) error {
	dbNow := s.d.nowEpoch()
	q := `DELETE FROM replica_sessions WHERE updated_at < (` + dbNow + ` - ?)`
	if _, err := s.db.Exec(q, staleWindowSec); err != nil {
		return fmt.Errorf("reap stale replica sessions: %w", err)
	}
	return nil
}
