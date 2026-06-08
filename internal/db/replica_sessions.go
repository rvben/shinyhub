package db

import "fmt"

// ReplicaSessionRow holds the per-(app_id, replica-index) session data that
// one instance reports for a batch upsert.
type ReplicaSessionRow struct {
	AppID        int64
	Idx          int
	Active       int64
	LastActivity int64
}

// UpsertReplicaSessions batch-upserts the caller's per-(app_id, idx) session
// counts into the replica_sessions table. updatedAt is the epoch-seconds
// timestamp to stamp every row with (typically time.Now().Unix() from the
// caller); passing a fixed value in tests allows deterministic staleness
// assertions.
//
// Each upsert sets active and last_activity to the supplied values and
// refreshes updated_at. Rows already present for (instance_id, app_id, idx)
// are overwritten; rows not present are created.
func (s *Store) UpsertReplicaSessions(instanceID string, updatedAt int64, rows []ReplicaSessionRow) error {
	if len(rows) == 0 {
		return nil
	}
	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("upsert replica sessions begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	for _, r := range rows {
		if _, err := tx.Exec(`
			INSERT INTO replica_sessions (instance_id, app_id, idx, active, last_activity, updated_at)
			VALUES (?, ?, ?, ?, ?, ?)
			ON CONFLICT(instance_id, app_id, idx) DO UPDATE SET
				active        = excluded.active,
				last_activity = excluded.last_activity,
				updated_at    = excluded.updated_at`,
			instanceID, r.AppID, r.Idx, r.Active, r.LastActivity, updatedAt,
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

// AppFleetLoad aggregates the active session counts reported by all non-stale,
// non-excluded instances for the given app.
//
// It considers only rows where:
//   - app_id  = appID
//   - updated_at >= staleCutoffEpoch  (rows older than this are treated as from
//     a crashed/unreachable instance and are ignored; this is always conservative:
//     it can only delay scale-down / hibernation, never wrongly trigger one)
//   - instance_id != excludeInstanceID (pass "" to include every instance)
//
// The returned active slice is sized to cover indexes 0..maxIdx (the highest
// replica index seen in the matching rows). active[i] is the sum of Active
// across all contributing instances for replica index i; gaps not reported
// by any instance are zero-filled.
//
// maxLastActivity is the maximum last_activity epoch across all matching rows
// (0 when no rows match).
func (s *Store) AppFleetLoad(appID int64, staleCutoffEpoch int64, excludeInstanceID string) (active []int64, maxLastActivity int64, err error) {
	rows, err := s.db.Query(`
		SELECT idx, SUM(active), MAX(last_activity)
		FROM replica_sessions
		WHERE app_id   = ?
		  AND updated_at >= ?
		  AND (? = '' OR instance_id != ?)
		GROUP BY idx
		ORDER BY idx`,
		appID, staleCutoffEpoch, excludeInstanceID, excludeInstanceID,
	)
	if err != nil {
		return nil, 0, fmt.Errorf("app fleet load query: %w", err)
	}
	defer rows.Close()

	type idxRow struct {
		idx             int
		sumActive       int64
		maxLastActivity int64
	}
	var results []idxRow
	var globalMaxLast int64

	for rows.Next() {
		var r idxRow
		if err := rows.Scan(&r.idx, &r.sumActive, &r.maxLastActivity); err != nil {
			return nil, 0, fmt.Errorf("app fleet load scan: %w", err)
		}
		results = append(results, r)
		if r.maxLastActivity > globalMaxLast {
			globalMaxLast = r.maxLastActivity
		}
	}
	if err := rows.Err(); err != nil {
		return nil, 0, fmt.Errorf("app fleet load rows: %w", err)
	}

	if len(results) == 0 {
		return []int64{}, 0, nil
	}

	maxIdx := results[len(results)-1].idx
	out := make([]int64, maxIdx+1)
	for _, r := range results {
		out[r.idx] = r.sumActive
	}
	return out, globalMaxLast, nil
}

// ReapStaleReplicaSessions deletes rows whose updated_at is older than
// cutoffEpoch, removing the footprint of crashed or restarted instances that
// are no longer reporting. The cutoff is conservative: excluding stale rows
// can only delay scale-down or hibernation, never wrongly trigger one.
func (s *Store) ReapStaleReplicaSessions(cutoffEpoch int64) error {
	if _, err := s.db.Exec(
		`DELETE FROM replica_sessions WHERE updated_at < ?`, cutoffEpoch,
	); err != nil {
		return fmt.Errorf("reap stale replica sessions: %w", err)
	}
	return nil
}
