package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/rvben/shinyhub/internal/schedulespec"
)

// scheduleConvergenceLockKey serializes desired producer obligations and
// last-writer publication on Postgres. SQLite's BEGIN IMMEDIATE provides the
// equivalent serialization.
const scheduleConvergenceLockKey int64 = 0x434f4e56 // "CONV"

type ScheduleProducerState struct {
	ScheduleID            int64     `json:"schedule_id"`
	ContentDigest         string    `json:"content_digest"`
	ProducerFingerprint   string    `json:"producer_fingerprint"`
	ProducerCommandJSON   string    `json:"-"`
	DeploymentID          *int64    `json:"deployment_id"`
	AppVersion            string    `json:"app_version"`
	ScheduleRunID         *int64    `json:"schedule_run_id"`
	PublicationGeneration int64     `json:"publication_generation"`
	DataWriteSequence     int64     `json:"data_write_sequence"`
	PublishedAt           time.Time `json:"published_at"`
}

type ScheduleDeployObligation struct {
	ID                     int64      `json:"id"`
	ScheduleID             int64      `json:"schedule_id"`
	DeploymentID           int64      `json:"deployment_id"`
	AppVersion             string     `json:"app_version"`
	ContentDigest          string     `json:"content_digest"`
	ProducerFingerprint    string     `json:"producer_fingerprint"`
	ProducerCommandJSON    string     `json:"-"`
	TimeoutSeconds         int        `json:"timeout_seconds"`
	OnSuccess              string     `json:"on_success"`
	MinRollIntervalSeconds int        `json:"min_roll_interval_seconds"`
	RollFallback           string     `json:"roll_fallback"`
	MaxDeferAgeSeconds     int        `json:"max_defer_age_seconds"`
	Status                 string     `json:"status"`
	ScheduleRunID          *int64     `json:"schedule_run_id"`
	Attempts               int        `json:"attempts"`
	LastError              string     `json:"last_error,omitempty"`
	NextAttemptAt          *time.Time `json:"next_attempt_at,omitempty"`
	CreatedAt              time.Time  `json:"created_at"`
	UpdatedAt              time.Time  `json:"updated_at"`
	FinishedAt             *time.Time `json:"finished_at,omitempty"`
}

const deployObligationColumns = `
id, schedule_id, deployment_id, app_version, content_digest,
producer_fingerprint, producer_command_json, timeout_seconds, on_success,
min_roll_interval_seconds, roll_fallback, max_defer_age_seconds, status,
schedule_run_id, attempts, last_error, next_attempt_at, created_at, updated_at, finished_at`

func scanDeployObligation(s rowScanner) (*ScheduleDeployObligation, error) {
	var o ScheduleDeployObligation
	var runID sql.NullInt64
	var nextAttempt, finished sql.NullTime
	if err := s.Scan(&o.ID, &o.ScheduleID, &o.DeploymentID, &o.AppVersion, &o.ContentDigest,
		&o.ProducerFingerprint, &o.ProducerCommandJSON, &o.TimeoutSeconds, &o.OnSuccess,
		&o.MinRollIntervalSeconds, &o.RollFallback, &o.MaxDeferAgeSeconds, &o.Status,
		&runID, &o.Attempts, &o.LastError, &nextAttempt, &o.CreatedAt, &o.UpdatedAt, &finished); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	if runID.Valid {
		v := runID.Int64
		o.ScheduleRunID = &v
	}
	if finished.Valid {
		v := finished.Time
		o.FinishedAt = &v
	}
	if nextAttempt.Valid {
		v := nextAttempt.Time
		o.NextAttemptAt = &v
	}
	return &o, nil
}

// ReconcileDeployObligationsForDeployment materializes convergence intent for
// every persisted enabled schedule on one promoted deployment. Manifest
// membership is deliberately irrelevant: retained and API-created schedules
// participate exactly like schedules declared in the uploaded bundle.
func (s *Store) ReconcileDeployObligationsForDeployment(appID, deploymentID int64) ([]*ScheduleDeployObligation, error) {
	ctx := context.Background()
	tx, err := s.d.beginWrite(ctx, s.rawDB(), scheduleConvergenceLockKey)
	if err != nil {
		return nil, fmt.Errorf("reconcile deploy obligations: begin: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()

	var version, digest, status string
	if err := tx.QueryRowContext(ctx, `
		SELECT version, COALESCE(content_digest, ''), status
		FROM deployments WHERE id = ? AND app_id = ?`, deploymentID, appID).Scan(&version, &digest, &status); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("reconcile deploy obligations: load deployment: %w", err)
	}
	if status != DeploymentSucceeded {
		return nil, fmt.Errorf("reconcile deploy obligations: deployment %d is %s, not succeeded", deploymentID, status)
	}
	if digest == "" {
		return nil, fmt.Errorf("reconcile deploy obligations: deployment %d has no content digest", deploymentID)
	}

	rows, err := tx.QueryContext(ctx, `
		SELECT id, app_id, name, cron_expr, command_json, enabled, timeout_seconds,
		       overlap_policy, missed_policy, deploy_trigger, timezone, on_success,
		       min_roll_interval_seconds, roll_fallback, max_defer_age_seconds,
		       created_at, updated_at
		FROM app_schedules WHERE app_id = ? ORDER BY id`, appID)
	if err != nil {
		return nil, fmt.Errorf("reconcile deploy obligations: list schedules: %w", err)
	}
	var schedules []*Schedule
	for rows.Next() {
		schedule, scanErr := scanSchedule(rows)
		if scanErr != nil {
			_ = rows.Close()
			return nil, scanErr
		}
		schedules = append(schedules, schedule)
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}

	var out []*ScheduleDeployObligation
	for _, schedule := range schedules {
		o, err := reconcileScheduleObligationTx(ctx, tx, schedule, deploymentID, version, digest)
		if err != nil {
			return nil, fmt.Errorf("reconcile schedule %q: %w", schedule.Name, err)
		}
		if o != nil {
			out = append(out, o)
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("reconcile deploy obligations: commit: %w", err)
	}
	committed = true
	return out, nil
}

func reconcileScheduleObligationTx(ctx context.Context, tx writeTx, schedule *Schedule, deploymentID int64, version, digest string) (*ScheduleDeployObligation, error) {
	canonical, fingerprint, err := schedulespec.ProducerIdentity(schedule.CommandJSON)
	if err != nil {
		return nil, err
	}

	if !schedule.Enabled || schedule.DeployTrigger == schedulespec.DeployTriggerNever {
		_, err := tx.ExecContext(ctx, `
			UPDATE schedule_deploy_obligations
			SET status = 'superseded', updated_at = CURRENT_TIMESTAMP,
			    finished_at = COALESCE(finished_at, CURRENT_TIMESTAMP)
			WHERE schedule_id = ? AND status IN ('pending', 'dispatching', 'running')`, schedule.ID)
		return nil, err
	}

	// A newly desired identity supersedes every older obligation, including a
	// running one. Its process may still finish and become the physical last
	// writer; completion will then put this current obligation back to pending.
	if _, err := tx.ExecContext(ctx, `
		UPDATE schedule_deploy_obligations
		SET status = 'superseded', updated_at = CURRENT_TIMESTAMP,
		    finished_at = COALESCE(finished_at, CURRENT_TIMESTAMP)
		WHERE schedule_id = ?
		  AND NOT (deployment_id = ? AND producer_fingerprint = ?)
		  AND status IN ('pending', 'dispatching', 'running')`, schedule.ID, deploymentID, fingerprint); err != nil {
		return nil, err
	}

	state, stateErr := producerStateTx(ctx, tx, schedule.ID)
	if stateErr != nil && !errors.Is(stateErr, ErrNotFound) {
		return nil, stateErr
	}
	satisfied := false
	switch schedule.DeployTrigger {
	case schedulespec.DeployTriggerFirstDeploy:
		satisfied = stateErr == nil && state.PublicationGeneration > 0
	case schedulespec.DeployTriggerBundleChange:
		satisfied = stateErr == nil && state.ContentDigest == digest && state.ProducerFingerprint == fingerprint
	default:
		return nil, fmt.Errorf("invalid deploy trigger %q", schedule.DeployTrigger)
	}
	if satisfied {
		unrepaired, err := scheduleProducerRepairRequiredTx(ctx, tx, schedule.ID)
		if err != nil {
			return nil, err
		}
		satisfied = !unrepaired
	}

	initialStatus := "pending"
	var satisfiedRunID any
	if satisfied {
		initialStatus = "satisfied"
		if state.ScheduleRunID != nil {
			satisfiedRunID = *state.ScheduleRunID
		}
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO schedule_deploy_obligations
			(schedule_id, deployment_id, app_version, content_digest,
			 producer_fingerprint, producer_command_json, timeout_seconds,
			 on_success, min_roll_interval_seconds, roll_fallback,
			 max_defer_age_seconds, status, schedule_run_id, finished_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?,
		        CASE WHEN ? = 'satisfied' THEN CURRENT_TIMESTAMP ELSE NULL END)
		ON CONFLICT(schedule_id, deployment_id, producer_fingerprint) DO NOTHING`,
		schedule.ID, deploymentID, version, digest, fingerprint, canonical,
		schedule.TimeoutSeconds, schedule.OnSuccess, schedule.MinRollIntervalSeconds,
		schedule.RollFallback, schedule.MaxDeferAgeSeconds, initialStatus, satisfiedRunID, initialStatus); err != nil {
		return nil, err
	}

	if satisfied {
		if _, err := tx.ExecContext(ctx, `
			UPDATE schedule_deploy_obligations
			SET status = 'satisfied', last_error = '', updated_at = CURRENT_TIMESTAMP,
			    schedule_run_id = ?,
			    finished_at = COALESCE(finished_at, CURRENT_TIMESTAMP)
			WHERE schedule_id = ? AND deployment_id = ? AND producer_fingerprint = ?`,
			satisfiedRunID, schedule.ID, deploymentID, fingerprint); err != nil {
			return nil, err
		}
	} else {
		// A formerly satisfied/superseded row is not proof after another writer
		// publishes. Genuine producer failures stay failed until an explicit retry
		// or a new desired identity appears.
		if _, err := tx.ExecContext(ctx, `
			UPDATE schedule_deploy_obligations
			SET status = 'pending', schedule_run_id = NULL, last_error = '', next_attempt_at = NULL,
			    updated_at = CURRENT_TIMESTAMP, finished_at = NULL
			WHERE schedule_id = ? AND deployment_id = ? AND producer_fingerprint = ?
			  AND status IN ('satisfied', 'superseded')`, schedule.ID, deploymentID, fingerprint); err != nil {
			return nil, err
		}
	}
	return scanDeployObligation(tx.QueryRowContext(ctx, `SELECT `+deployObligationColumns+`
		FROM schedule_deploy_obligations
		WHERE schedule_id = ? AND deployment_id = ? AND producer_fingerprint = ?`,
		schedule.ID, deploymentID, fingerprint))
}

func producerStateTx(ctx context.Context, tx writeTx, scheduleID int64) (*ScheduleProducerState, error) {
	var p ScheduleProducerState
	var deploymentID, runID sql.NullInt64
	err := tx.QueryRowContext(ctx, `
		SELECT schedule_id, content_digest, producer_fingerprint, producer_command_json,
		       deployment_id, app_version, schedule_run_id, publication_generation,
		       data_write_sequence, published_at
		FROM schedule_producer_state WHERE schedule_id = ?`, scheduleID).Scan(
		&p.ScheduleID, &p.ContentDigest, &p.ProducerFingerprint, &p.ProducerCommandJSON,
		&deploymentID, &p.AppVersion, &runID, &p.PublicationGeneration,
		&p.DataWriteSequence, &p.PublishedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if deploymentID.Valid {
		v := deploymentID.Int64
		p.DeploymentID = &v
	}
	if runID.Valid {
		v := runID.Int64
		p.ScheduleRunID = &v
	}
	return &p, nil
}

func scheduleProducerRepairRequiredTx(ctx context.Context, tx writeTx, scheduleID int64) (bool, error) {
	var required int
	err := tx.QueryRowContext(ctx, `
		SELECT CASE WHEN EXISTS (
			SELECT 1 FROM schedule_data_uncertainty WHERE schedule_id = ?
		) THEN 1 ELSE 0 END`, scheduleID).Scan(&required)
	if err != nil {
		return false, err
	}
	return required != 0, nil
}

// ScheduleProducerRepairRequired reports whether this producer has a physical
// write attempt that failed after its last successful publication. Success by
// another schedule cannot clear this per-producer repair obligation.
func (s *Store) ScheduleProducerRepairRequired(scheduleID int64) (bool, error) {
	var required int
	if err := s.db.QueryRow(`
		SELECT CASE WHEN EXISTS (
			SELECT 1 FROM schedule_data_uncertainty WHERE schedule_id = ?
		) THEN 1 ELSE 0 END`, scheduleID).Scan(&required); err != nil {
		return false, fmt.Errorf("check schedule %d producer repair: %w", scheduleID, err)
	}
	return required != 0, nil
}

func (s *Store) GetScheduleProducerState(scheduleID int64) (*ScheduleProducerState, error) {
	var p ScheduleProducerState
	var deploymentID, runID sql.NullInt64
	err := s.db.QueryRow(`
		SELECT schedule_id, content_digest, producer_fingerprint, producer_command_json,
		       deployment_id, app_version, schedule_run_id, publication_generation,
		       data_write_sequence, published_at
		FROM schedule_producer_state WHERE schedule_id = ?`, scheduleID).Scan(
		&p.ScheduleID, &p.ContentDigest, &p.ProducerFingerprint, &p.ProducerCommandJSON,
		&deploymentID, &p.AppVersion, &runID, &p.PublicationGeneration,
		&p.DataWriteSequence, &p.PublishedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if deploymentID.Valid {
		v := deploymentID.Int64
		p.DeploymentID = &v
	}
	if runID.Valid {
		v := runID.Int64
		p.ScheduleRunID = &v
	}
	return &p, nil
}

// ReconcileAllDeployObligations repairs missing desired-state rows after a
// crash and admits persisted policies for unchanged apps.
func (s *Store) ReconcileAllDeployObligations() error {
	rows, err := s.db.Query(`
		SELECT d.app_id, d.id
		FROM deployments d
		WHERE d.status = 'succeeded'
		  AND d.id = (SELECT d2.id FROM deployments d2
		              WHERE d2.app_id = d.app_id AND d2.status = 'succeeded'
		              ORDER BY d2.id DESC LIMIT 1)
		ORDER BY d.app_id`)
	if err != nil {
		return fmt.Errorf("list current deployments: %w", err)
	}
	var targets [][2]int64
	for rows.Next() {
		var appID, deploymentID int64
		if err := rows.Scan(&appID, &deploymentID); err != nil {
			_ = rows.Close()
			return err
		}
		targets = append(targets, [2]int64{appID, deploymentID})
	}
	if err := rows.Close(); err != nil {
		return err
	}
	for _, target := range targets {
		if _, err := s.ReconcileDeployObligationsForDeployment(target[0], target[1]); err != nil {
			return err
		}
	}
	return nil
}

// ClaimNextDeployObligation atomically leases one pending obligation for
// in-process dispatch. A crash before the run row is bound is repaired by
// RecoverDeployObligations at startup.
func (s *Store) ClaimNextDeployObligation() (*ScheduleDeployObligation, error) {
	return s.claimNextDeployObligation(0, 0, false)
}

// ClaimNextDeployObligationFor scopes synchronous request draining to the app
// and deployment that request reconciled. An unrelated app's poisoned
// obligation must never make an otherwise healthy deploy return 500.
func (s *Store) ClaimNextDeployObligationFor(appID, deploymentID int64) (*ScheduleDeployObligation, error) {
	return s.claimNextDeployObligation(appID, deploymentID, true)
}

func (s *Store) claimNextDeployObligation(appID, deploymentID int64, scoped bool) (*ScheduleDeployObligation, error) {
	ctx := context.Background()
	tx, err := s.d.beginWrite(ctx, s.rawDB(), scheduleConvergenceLockKey)
	if err != nil {
		return nil, err
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()
	scope := ""
	var args []any
	if scoped {
		scope = " AND sc.app_id = ? AND o.deployment_id = ?"
		args = append(args, appID, deploymentID)
	}
	o, err := scanDeployObligation(tx.QueryRowContext(ctx, `
		SELECT `+prefixedDeployObligationColumns("o")+`
		FROM schedule_deploy_obligations o
		JOIN app_schedules sc ON sc.id = o.schedule_id
		WHERE o.status = 'pending'
		  AND (o.next_attempt_at IS NULL OR o.next_attempt_at <= CURRENT_TIMESTAMP)
		  AND sc.enabled = 1
		  AND sc.deploy_trigger <> 'never'
		  AND o.deployment_id = (
		    SELECT d.id FROM deployments d
		    WHERE d.app_id = sc.app_id AND d.status = 'succeeded'
		    ORDER BY d.id DESC LIMIT 1
		  )
		  AND NOT EXISTS (
		    SELECT 1 FROM deployments staging
		    WHERE staging.app_id = sc.app_id AND staging.status = 'pending'
		  )
		  AND o.producer_command_json = sc.command_json
		  AND (
		    EXISTS (SELECT 1 FROM schedule_data_uncertainty uncertainty
		            WHERE uncertainty.schedule_id = sc.id)
		    OR
		    (sc.deploy_trigger = 'first_deploy' AND NOT EXISTS (
		      SELECT 1 FROM schedule_producer_state ps WHERE ps.schedule_id = sc.id
		    ))
		    OR
		    (sc.deploy_trigger = 'bundle_change' AND NOT EXISTS (
		      SELECT 1 FROM schedule_producer_state ps
		      WHERE ps.schedule_id = sc.id
		        AND ps.content_digest = o.content_digest
		        AND ps.producer_fingerprint = o.producer_fingerprint
		    ))
		  )
		`+scope+`
		ORDER BY o.created_at, o.id LIMIT 1`, args...))
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			if err := tx.Commit(); err != nil {
				return nil, err
			}
			committed = true
			return nil, ErrNotFound
		}
		return nil, err
	}
	res, err := tx.ExecContext(ctx, `
		UPDATE schedule_deploy_obligations
		SET status = 'dispatching', attempts = attempts + 1,
		    updated_at = CURRENT_TIMESTAMP, last_error = '', next_attempt_at = NULL
		WHERE id = ? AND status = 'pending'`, o.ID)
	if err != nil {
		return nil, err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return nil, fmt.Errorf("claim obligation %d lost race", o.ID)
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	committed = true
	o.Status = "dispatching"
	o.Attempts++
	return o, nil
}

func prefixedDeployObligationColumns(alias string) string {
	columns := []string{
		"id", "schedule_id", "deployment_id", "app_version", "content_digest",
		"producer_fingerprint", "producer_command_json", "timeout_seconds", "on_success",
		"min_roll_interval_seconds", "roll_fallback", "max_defer_age_seconds", "status",
		"schedule_run_id", "attempts", "last_error", "next_attempt_at", "created_at", "updated_at", "finished_at",
	}
	for i := range columns {
		columns[i] = alias + "." + columns[i]
	}
	return strings.Join(columns, ", ")
}

func (s *Store) ReleaseDeployObligation(id int64, cause error) error {
	msg := ""
	if cause != nil {
		msg = cause.Error()
	}
	// A poison obligation must not monopolize the global oldest-first outbox.
	// Attempts is already incremented at claim; cap exponential admission retry
	// at one minute while leaving genuine producer failures terminal.
	var attempts int
	if err := s.db.QueryRow(`SELECT attempts FROM schedule_deploy_obligations WHERE id = ?`, id).Scan(&attempts); err != nil {
		return err
	}
	exponent := attempts - 1
	if exponent < 0 {
		exponent = 0
	}
	delay := time.Second << min(exponent, 6)
	if delay > time.Minute {
		delay = time.Minute
	}
	_, err := s.db.Exec(`
		UPDATE schedule_deploy_obligations
		SET status = 'pending', last_error = ?, next_attempt_at = `+s.d.nowPlusSeconds(int(delay/time.Second))+`, updated_at = CURRENT_TIMESTAMP
		WHERE id = ? AND status = 'dispatching'`, msg, id)
	return err
}

func (s *Store) GetDeployObligation(id int64) (*ScheduleDeployObligation, error) {
	return scanDeployObligation(s.db.QueryRow(`SELECT `+deployObligationColumns+`
		FROM schedule_deploy_obligations WHERE id = ?`, id))
}

// RetryDeployObligation is the explicit retry boundary for genuine producer
// failures. Admission/interruption failures are repaired automatically.
func (s *Store) RetryDeployObligation(id int64) error {
	res, err := s.db.Exec(`
		UPDATE schedule_deploy_obligations
		SET status = 'pending', schedule_run_id = NULL, last_error = '',
		    next_attempt_at = NULL, finished_at = NULL, updated_at = CURRENT_TIMESTAMP
		WHERE id = ? AND status = 'failed'`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) RecoverDeployObligations() error {
	_, err := s.db.Exec(`
		UPDATE schedule_deploy_obligations
		SET status = 'pending', schedule_run_id = NULL,
		    last_error = 'server interrupted before producer completion',
		    next_attempt_at = NULL, updated_at = CURRENT_TIMESTAMP, finished_at = NULL
		WHERE status = 'dispatching'
		   OR (status = 'running' AND schedule_run_id IN (
		       SELECT id FROM schedule_runs WHERE status = 'interrupted'
		   ))`)
	return err
}

// InsertDeployScheduleRun binds the durable obligation and run admission in
// one transaction, closing the crash window between an in-memory dispatch and
// provenance creation.
func (s *Store) InsertDeployScheduleRun(p InsertScheduleRunParams) (int64, error) {
	if p.DeployObligationID == nil {
		return 0, errors.New("deploy obligation id is required")
	}
	ctx := context.Background()
	tx, err := s.d.beginWrite(ctx, s.rawDB(), scheduleConvergenceLockKey)
	if err != nil {
		return 0, err
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()
	var triggeredBy sql.NullInt64
	if p.TriggeredByUserID != nil {
		triggeredBy = sql.NullInt64{Int64: *p.TriggeredByUserID, Valid: true}
	}
	var id int64
	err = tx.QueryRowContext(ctx, `
		INSERT INTO schedule_runs (schedule_id, status, trigger, triggered_by_user_id, started_at, log_path,
			on_success, min_roll_interval_seconds, roll_fallback, max_defer_age_seconds,
			deployment_id, app_version, content_digest, producer_fingerprint,
			producer_command_json, publishes_data, deploy_obligation_id, provenance_admission)
		SELECT ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 1
		WHERE EXISTS (SELECT 1 FROM schedule_deploy_obligations
		              WHERE id = ? AND schedule_id = ? AND status = 'dispatching')
		RETURNING id`, p.ScheduleID, p.Status, p.Trigger, triggeredBy, p.StartedAt, p.LogPath,
		p.OnSuccess, p.MinRollIntervalSeconds, p.RollFallback, p.MaxDeferAgeSeconds,
		p.DeploymentID, p.AppVersion, p.ContentDigest, p.ProducerFingerprint,
		p.ProducerCommandJSON, boolToInt(p.PublishesData), p.DeployObligationID, *p.DeployObligationID, p.ScheduleID).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, fmt.Errorf("deploy obligation %d is no longer dispatchable", *p.DeployObligationID)
	}
	if err != nil {
		return 0, err
	}
	res, err := tx.ExecContext(ctx, `
		UPDATE schedule_deploy_obligations
		SET status = 'running', schedule_run_id = ?, updated_at = CURRENT_TIMESTAMP
		WHERE id = ? AND status = 'dispatching'`, id, *p.DeployObligationID)
	if err != nil {
		return 0, err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return 0, fmt.Errorf("bind deploy obligation %d lost race", *p.DeployObligationID)
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	committed = true
	return id, nil
}

func (s *Store) ListPinnedScheduleDeploymentDirs(appID int64) ([]string, error) {
	rows, err := s.db.Query(`
		SELECT DISTINCT d.bundle_dir
		FROM schedule_runs r
		JOIN app_schedules sc ON sc.id = r.schedule_id
		JOIN deployments d ON d.id = r.deployment_id
		WHERE sc.app_id = ? AND r.status = 'running' AND d.bundle_dir <> ''
		UNION
		SELECT DISTINCT d.bundle_dir
		FROM schedule_deploy_obligations o
		JOIN app_schedules sc ON sc.id = o.schedule_id
		JOIN deployments d ON d.id = o.deployment_id
		WHERE sc.app_id = ? AND o.status IN ('pending', 'dispatching', 'running')
		  AND d.bundle_dir <> ''`, appID, appID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var dirs []string
	for rows.Next() {
		var dir string
		if err := rows.Scan(&dir); err != nil {
			return nil, err
		}
		dirs = append(dirs, dir)
	}
	return dirs, rows.Err()
}
