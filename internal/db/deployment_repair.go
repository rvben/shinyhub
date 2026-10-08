package db

import (
	"fmt"
	"strings"
)

// deploymentRepairConditionSQL applies to a deployments row aliased as failed.
// Admission, startup recovery and API observations must use the same fence.
const deploymentRepairConditionSQL = `failed.status IN ('pending', 'failed')
	AND (failed.producer_barrier_entered = 1 OR failed.prior_schedule_snapshot_recorded = 0)
	AND failed.id > COALESCE((
		SELECT MAX(ok.id) FROM deployments ok
		WHERE ok.app_id = failed.app_id AND ok.status = 'succeeded'
	), 0)`

// DeploymentRepairRequiredForApps reports only deployment compatibility fences,
// not running or failed schedule writers. This is a page-sized API read; keeping
// it out of GetAppBySlug avoids adding work to watcher and access hot paths.
func (s *Store) DeploymentRepairRequiredForApps(appIDs []int64) (map[int64]bool, error) {
	result := make(map[int64]bool, len(appIDs))
	if len(appIDs) == 0 {
		return result, nil
	}
	args := make([]any, len(appIDs))
	for i, id := range appIDs {
		args[i] = id
		result[id] = false
	}
	rows, err := s.db.Query(`SELECT DISTINCT failed.app_id FROM deployments failed
		WHERE failed.app_id IN (`+strings.TrimSuffix(strings.Repeat("?,", len(appIDs)), ",")+`)
		AND `+deploymentRepairConditionSQL, args...)
	if err != nil {
		return nil, fmt.Errorf("load deployment repair state: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("scan deployment repair state: %w", err)
		}
		result[id] = true
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read deployment repair state: %w", err)
	}
	return result, nil
}
