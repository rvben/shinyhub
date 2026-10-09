package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// EnqueueRollingRestart uses the durable activation queue without inventing a
// data publication. A NULL run ID distinguishes forced process replacement
// from a schedule activation whose data is already loaded by some replicas.
func (s *Store) EnqueueRollingRestart(app *App, deployment *Deployment) (*ScheduleActivation, error) {
	ctx := context.Background()
	tx, err := s.d.beginWrite(ctx, s.rawDB(), scheduleActivationLockKey)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback() //nolint:errcheck
	var busy int
	if err := tx.QueryRow(`SELECT COUNT(*) FROM schedule_activations WHERE app_id=? AND status IN ('pending','deferred_interval','deferred_capacity','running','repairing')`, app.ID).Scan(&busy); err != nil {
		return nil, err
	}
	if busy > 0 {
		return nil, ErrScheduleActivationBusy
	}
	var generation int64
	if err := tx.QueryRow(`SELECT COALESCE(MAX(generation),1) FROM app_data_publication WHERE app_id=?`, app.ID).Scan(&generation); err != nil {
		return nil, err
	}
	if generation < 1 {
		generation = 1
	}
	var id int64
	err = tx.QueryRow(`INSERT INTO schedule_activations (app_id,app_slug,schedule_name,source_deployment_id,source_app_version,source_content_digest,action,roll_fallback,target_generation,status,phase,due_at)
 VALUES (?,?,'manual restart',?,?,?,'roll','defer',?,'pending','pending',?) RETURNING id`, app.ID, app.Slug, deployment.ID, deployment.Version, deployment.ContentDigest, generation, time.Now().UTC()).Scan(&id)
	if err != nil {
		return nil, err
	}
	a, err := getActivationTx(ctx, tx, id)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return a, nil
}

// ActivationDeployment allocates a unique durable serving identity while
// retaining the same immutable bundle/version. Allocation and linkage are
// atomic, so crash recovery cannot create two generations for one activation.
func (s *Store) ActivationDeployment(activationID int64, source *Deployment) (*Deployment, error) {
	ctx := context.Background()
	tx, err := s.d.beginWrite(ctx, s.rawDB(), scheduleActivationLockKey)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback() //nolint:errcheck
	var linked sql.NullInt64
	if err := tx.QueryRow("SELECT activation_deployment_id FROM schedule_activations WHERE id=? AND app_id=?", activationID, source.AppID).Scan(&linked); err != nil {
		return nil, err
	}
	if linked.Valid {
		if err := tx.Commit(); err != nil {
			return nil, err
		}
		return s.GetDeploymentByID(linked.Int64)
	}
	token, err := newActivationToken()
	if err != nil {
		return nil, err
	}
	var id int64
	err = tx.QueryRow(`INSERT INTO deployments (app_id,version,bundle_dir,status,content_digest,activation_token,prepared,origin_kind,origin_channel,restored_from_id,prior_schedule_snapshot_recorded,serving_activation_id)
 SELECT app_id,version,bundle_dir,'pending',content_digest,?,prepared,'direct','api',NULL,1,? FROM deployments WHERE id=? AND status='succeeded' RETURNING id`, token, activationID, source.ID).Scan(&id)
	if err != nil {
		return nil, err
	}
	_, err = tx.Exec(`INSERT INTO deployment_prior_schedule_snapshots (deployment_id,name,cron_expr,command_json,inputs_json,enabled,timeout_seconds,overlap_policy,missed_policy,deploy_trigger,timezone,on_success,min_roll_interval_seconds,roll_fallback,max_defer_age_seconds)
 SELECT ?,name,cron_expr,command_json,inputs_json,enabled,timeout_seconds,overlap_policy,missed_policy,deploy_trigger,timezone,on_success,min_roll_interval_seconds,roll_fallback,max_defer_age_seconds FROM app_schedules WHERE app_id=?`, id, source.AppID)
	if err != nil {
		return nil, err
	}
	if _, err = tx.Exec("UPDATE schedule_activations SET activation_deployment_id=? WHERE id=?", id, activationID); err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return s.GetDeploymentByID(id)
}

// ResetFailedActivationDeployment is safe only after its runtime was confirmed
// absent. A failed readiness attempt can then allocate a fresh generation.
func (s *Store) ResetFailedActivationDeployment(activationID, id int64) error {
	res, err := s.db.Exec(`UPDATE schedule_activations SET activation_deployment_id=NULL WHERE id=? AND activation_deployment_id=? AND EXISTS (SELECT 1 FROM deployments WHERE id=? AND status='failed')`, activationID, id, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return fmt.Errorf("activation candidate is not failed")
	}
	return nil
}

func (s *Store) ActivationDeploymentID(activationID int64) (int64, error) {
	var id sql.NullInt64
	err := s.db.QueryRow("SELECT activation_deployment_id FROM schedule_activations WHERE id=?", activationID).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, ErrNotFound
	}
	return id.Int64, err
}

// IsServingActivationDeployment identifies candidates that never change declarations.
func (s *Store) IsServingActivationDeployment(id int64) (bool, error) {
	var yes bool
	err := s.db.QueryRow("SELECT serving_activation_id IS NOT NULL FROM deployments WHERE id=?", id).Scan(&yes)
	return yes, err
}

// RefreshManualActivationTarget records the current publication without creating one.
func (s *Store) RefreshManualActivationTarget(id, appID int64) (int64, error) {
	var generation int64
	err := s.db.QueryRow("SELECT COALESCE(MAX(generation),1) FROM app_data_publication WHERE app_id=?", appID).Scan(&generation)
	if err != nil {
		return 0, err
	}
	if generation < 1 {
		generation = 1
	}
	_, err = s.db.Exec("UPDATE schedule_activations SET target_generation=? WHERE id=? AND schedule_run_id IS NULL AND status='running'", generation, id)
	return generation, err
}

// Reap only candidates with terminal owners and no guarded-start checkpoint.
// Candidates with runtime identities remain fenced for physical cleanup.
func reapUnstartedServingActivationsTx(tx writeTx) error {
	_, err := tx.Exec(`UPDATE deployments SET status='failed',failure_reason='serving activation ended before publication'
 WHERE status='pending' AND serving_activation_id IS NOT NULL
 AND EXISTS (SELECT 1 FROM schedule_activations a WHERE a.id=deployments.serving_activation_id AND a.status IN ('succeeded','failed','cancelled','superseded','not_needed','blocked_unsupported','target_deleted'))
 AND NOT EXISTS (SELECT 1 FROM deployment_replicas r WHERE r.deployment_id=deployments.id)`)
	return err
}
