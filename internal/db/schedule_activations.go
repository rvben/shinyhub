package db

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// scheduleActivationLockKey serializes generation allocation and per-app queue
// coalescing on Postgres. SQLite already serializes writers with BEGIN IMMEDIATE.
const scheduleActivationLockKey int64 = 0x41435456 // "ACTV"

var ErrScheduleActivationBusy = errors.New("schedule activation is already running or repairing")

type ScheduleActivation struct {
	ID                     int64      `json:"id"`
	AppID                  *int64     `json:"app_id"`
	AppSlug                string     `json:"app_slug"`
	ScheduleID             *int64     `json:"schedule_id"`
	ScheduleName           string     `json:"schedule_name"`
	ScheduleRunID          *int64     `json:"schedule_run_id"`
	Action                 string     `json:"action"`
	MinRollIntervalSeconds int        `json:"min_roll_interval_seconds"`
	RollFallback           string     `json:"roll_fallback"`
	MaxDeferAgeSeconds     int        `json:"max_defer_age_seconds"`
	TargetGeneration       int64      `json:"target_generation"`
	Status                 string     `json:"status"`
	Phase                  string     `json:"phase"`
	DueAt                  time.Time  `json:"due_at"`
	DeferReason            string     `json:"defer_reason,omitempty"`
	Attempts               int        `json:"attempts"`
	CapacityDeferrals      int        `json:"capacity_deferrals"`
	CapacityDeferredAt     *time.Time `json:"capacity_deferred_at,omitempty"`
	SurgeIndex             int        `json:"surge_index"`
	NextSlot               int        `json:"next_slot"`
	LastError              string     `json:"last_error,omitempty"`
	SupersededByID         *int64     `json:"superseded_by_id,omitempty"`
	CreatedAt              time.Time  `json:"created_at"`
	UpdatedAt              time.Time  `json:"updated_at"`
	StartedAt              *time.Time `json:"started_at,omitempty"`
	FinishedAt             *time.Time `json:"finished_at,omitempty"`
}

type CompleteScheduleRunParams struct {
	RunID      int64
	Status     string
	ExitCode   *int
	FinishedAt time.Time
}

const activationColumns = `
id, app_id, app_slug, schedule_id, schedule_name, schedule_run_id, action, min_roll_interval_seconds,
roll_fallback, max_defer_age_seconds, target_generation, status, phase, due_at, defer_reason, attempts,
capacity_deferrals, capacity_deferred_at, surge_index,
next_slot, last_error, superseded_by_id, created_at, updated_at, started_at, finished_at`

// CompleteScheduleRunAndEnqueueActivation is the transactional outbox boundary
// between scheduled work and serving-data activation. A succeeded run and its
// roll request either both commit or neither does. Repeating a completion is
// idempotent and returns the original activation.
func (s *Store) CompleteScheduleRunAndEnqueueActivation(p CompleteScheduleRunParams) (*ScheduleActivation, error) {
	ctx := context.Background()
	tx, err := s.d.beginWrite(ctx, s.rawDB(), scheduleActivationLockKey)
	if err != nil {
		return nil, fmt.Errorf("complete schedule run: begin: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()

	var currentStatus, action, appSlug, scheduleName, rollFallback string
	var scheduleID, appID int64
	var minInterval, maxDeferAge int
	err = tx.QueryRowContext(ctx, `
		SELECT r.status, r.on_success, r.min_roll_interval_seconds,
		       r.roll_fallback, r.max_defer_age_seconds,
		       r.schedule_id, sc.name, sc.app_id, a.slug
		FROM schedule_runs r
		JOIN app_schedules sc ON sc.id = r.schedule_id
		JOIN apps a ON a.id = sc.app_id
		WHERE r.id = ?`, p.RunID).Scan(
		&currentStatus, &action, &minInterval, &rollFallback, &maxDeferAge,
		&scheduleID, &scheduleName, &appID, &appSlug,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("complete schedule run: load: %w", err)
	}

	if currentStatus != "running" {
		activation, getErr := getActivationByRunTx(ctx, tx, p.RunID)
		if getErr != nil && !errors.Is(getErr, ErrNotFound) {
			return nil, getErr
		}
		if err := tx.Commit(); err != nil {
			return nil, fmt.Errorf("complete schedule run: commit idempotent read: %w", err)
		}
		committed = true
		return activation, nil
	}

	var ec sql.NullInt64
	if p.ExitCode != nil {
		ec = sql.NullInt64{Int64: int64(*p.ExitCode), Valid: true}
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE schedule_runs SET status = ?, exit_code = ?, finished_at = ?
		WHERE id = ? AND status = 'running'`, p.Status, ec, p.FinishedAt, p.RunID); err != nil {
		return nil, fmt.Errorf("complete schedule run: update terminal state: %w", err)
	}
	if p.Status != "succeeded" || action != "roll" {
		if err := tx.Commit(); err != nil {
			return nil, fmt.Errorf("complete schedule run: commit: %w", err)
		}
		committed = true
		return nil, nil
	}

	var generation int64
	if err := tx.QueryRowContext(ctx, `
		UPDATE apps SET data_generation = data_generation + 1, updated_at = CURRENT_TIMESTAMP
		WHERE id = ? RETURNING data_generation`, appID).Scan(&generation); err != nil {
		return nil, fmt.Errorf("complete schedule run: allocate generation: %w", err)
	}
	if _, err := tx.ExecContext(ctx,
		`UPDATE schedule_runs SET target_generation = ? WHERE id = ?`, generation, p.RunID,
	); err != nil {
		return nil, fmt.Errorf("complete schedule run: snapshot generation: %w", err)
	}

	// Damping begins after the most recent completed activation. Queued work is
	// coalesced into the newest generation while retaining the earliest due time,
	// so a busy schedule can never postpone its own already-due activation.
	dueAt := p.FinishedAt
	var damperFloor time.Time
	var lastFinished sql.NullTime
	err = tx.QueryRowContext(ctx, `
		SELECT finished_at FROM schedule_activations
		WHERE app_id = ? AND status = 'succeeded'
		ORDER BY finished_at DESC, id DESC LIMIT 1`, appID).Scan(&lastFinished)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("complete schedule run: load damper anchor: %w", err)
	}
	if lastFinished.Valid {
		damped := lastFinished.Time.Add(time.Duration(minInterval) * time.Second)
		if damped.After(dueAt) {
			dueAt = damped
		}
		if damped.After(p.FinishedAt) {
			damperFloor = damped
		}
	}
	var queuedDue time.Time
	err = tx.QueryRowContext(ctx, `
		SELECT due_at FROM schedule_activations
		WHERE app_id = ? AND status IN ('pending', 'deferred_interval', 'deferred_capacity')
		ORDER BY due_at ASC, id ASC LIMIT 1`, appID).Scan(&queuedDue)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("complete schedule run: load queued due time: %w", err)
	}
	if err == nil && queuedDue.Before(dueAt) {
		// Preserve queue seniority without violating the newly snapshotted
		// policy. An older zero-damper request cannot pull a newer one-hour
		// request ahead of its own last-success floor.
		dueAt = queuedDue
		if !damperFloor.IsZero() && damperFloor.After(dueAt) {
			dueAt = damperFloor
		}
	}
	activationStatus := "pending"
	if dueAt.After(p.FinishedAt) {
		activationStatus = "deferred_interval"
	}

	var activationID int64
	err = tx.QueryRowContext(ctx, `
		INSERT INTO schedule_activations
			(app_id, app_slug, schedule_id, schedule_name, schedule_run_id, action,
			 min_roll_interval_seconds, roll_fallback, max_defer_age_seconds,
			 target_generation, status, phase, due_at)
		VALUES (?, ?, ?, ?, ?, 'roll', ?, ?, ?, ?, ?, 'pending', ?)
		RETURNING id`,
		appID, appSlug, scheduleID, scheduleName, p.RunID, minInterval, rollFallback,
		maxDeferAge, generation, activationStatus, dueAt,
	).Scan(&activationID)
	if err != nil {
		return nil, fmt.Errorf("complete schedule run: enqueue activation: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE schedule_activations
		SET status = 'superseded', superseded_by_id = ?, finished_at = ?, updated_at = CURRENT_TIMESTAMP
		WHERE app_id = ? AND id <> ?
		  AND status IN ('pending', 'deferred_interval', 'deferred_capacity')`,
		activationID, p.FinishedAt, appID, activationID,
	); err != nil {
		return nil, fmt.Errorf("complete schedule run: coalesce queue: %w", err)
	}

	activation, err := getActivationTx(ctx, tx, activationID)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("complete schedule run: commit activation: %w", err)
	}
	committed = true
	return activation, nil
}

func (s *Store) GetScheduleActivation(id int64) (*ScheduleActivation, error) {
	return scanScheduleActivation(s.db.QueryRow(`SELECT `+activationColumns+` FROM schedule_activations WHERE id = ?`, id))
}

// CancelQueuedScheduleActivation terminalizes the newest still-nondestructive
// activation for a schedule. Running or repairing work may own live runtime
// identity and must finish recovery instead of being erased by cancellation.
func (s *Store) CancelQueuedScheduleActivation(scheduleID int64, finishedAt time.Time) (*ScheduleActivation, error) {
	ctx := context.Background()
	tx, err := s.d.beginWrite(ctx, s.rawDB(), scheduleActivationLockKey)
	if err != nil {
		return nil, fmt.Errorf("cancel schedule activation: begin: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()
	var id int64
	err = tx.QueryRowContext(ctx, `SELECT id FROM schedule_activations
		WHERE schedule_id = ? AND status IN ('pending', 'deferred_interval', 'deferred_capacity')
		ORDER BY id DESC LIMIT 1`, scheduleID).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		var busy int
		if countErr := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM schedule_activations
			WHERE schedule_id = ? AND status IN ('running', 'repairing')`, scheduleID).Scan(&busy); countErr != nil {
			return nil, fmt.Errorf("cancel schedule activation: inspect state: %w", countErr)
		}
		if busy > 0 {
			return nil, ErrScheduleActivationBusy
		}
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("cancel schedule activation: select: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE schedule_activations
		SET status = 'cancelled', phase = 'complete', defer_reason = '', last_error = '',
		    finished_at = ?, updated_at = CURRENT_TIMESTAMP
		WHERE id = ? AND status IN ('pending', 'deferred_interval', 'deferred_capacity')`, finishedAt, id); err != nil {
		return nil, fmt.Errorf("cancel schedule activation: update: %w", err)
	}
	a, err := getActivationTx(ctx, tx, id)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("cancel schedule activation: commit: %w", err)
	}
	committed = true
	return a, nil
}

func (s *Store) ListScheduleActivationsByApp(appID int64, limit int) ([]*ScheduleActivation, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	rows, err := s.db.Query(`SELECT `+activationColumns+`
		FROM schedule_activations WHERE app_id = ? ORDER BY id DESC LIMIT ?`, appID, limit)
	if err != nil {
		return nil, fmt.Errorf("list schedule activations: %w", err)
	}
	defer rows.Close()
	var out []*ScheduleActivation
	for rows.Next() {
		activation, err := scanScheduleActivation(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, activation)
	}
	return out, rows.Err()
}

// LatestScheduleActivationsByApp returns exactly the most relevant activation
// for every schedule snapshot, including terminal history whose source
// schedule was deleted. Unlike taking the first N app-wide history rows, a
// high-cadence schedule cannot crowd a low-cadence schedule out of the UI.
func (s *Store) LatestScheduleActivationsByApp(appID int64) ([]*ScheduleActivation, error) {
	rows, err := s.db.Query(`SELECT `+prefixedActivationColumns("a")+`
		FROM schedule_activations a
		WHERE a.app_id = ? AND NOT EXISTS (
			SELECT 1 FROM schedule_activations newer
			WHERE newer.app_id = a.app_id AND newer.schedule_id = a.schedule_id AND (
				CASE WHEN newer.status IN ('running', 'repairing') THEN 0 ELSE 1 END
				< CASE WHEN a.status IN ('running', 'repairing') THEN 0 ELSE 1 END
				OR (
					CASE WHEN newer.status IN ('running', 'repairing') THEN 0 ELSE 1 END
					= CASE WHEN a.status IN ('running', 'repairing') THEN 0 ELSE 1 END
					AND newer.id > a.id
				)
			)
		)
		ORDER BY a.id DESC`, appID)
	if err != nil {
		return nil, fmt.Errorf("list latest schedule activations: %w", err)
	}
	defer rows.Close()
	var out []*ScheduleActivation
	for rows.Next() {
		a, err := scanScheduleActivation(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

func prefixedActivationColumns(alias string) string {
	columns := []string{
		"id", "app_id", "app_slug", "schedule_id", "schedule_name", "schedule_run_id", "action", "min_roll_interval_seconds",
		"roll_fallback", "max_defer_age_seconds", "target_generation", "status", "phase", "due_at", "defer_reason", "attempts",
		"capacity_deferrals", "capacity_deferred_at", "surge_index",
		"next_slot", "last_error", "superseded_by_id", "created_at", "updated_at", "started_at", "finished_at",
	}
	for i := range columns {
		columns[i] = alias + "." + columns[i]
	}
	return strings.Join(columns, ", ")
}

// ClaimNextScheduleActivation claims at most one due activation globally.
// This feature is enabled only on a single control-plane process, and that
// process invokes this method serially. A row still marked running therefore
// belongs to a prior ProcessNext call that returned before its terminal write
// committed (or to the previous server process), so it is reclaimed before
// new work. That makes transient finish/defer failures self-healing without
// permitting two live rollout owners.
func (s *Store) ClaimNextScheduleActivation(now time.Time) (*ScheduleActivation, error) {
	ctx := context.Background()
	tx, err := s.d.beginWrite(ctx, s.rawDB(), scheduleActivationLockKey)
	if err != nil {
		return nil, fmt.Errorf("claim schedule activation: begin: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()
	var id int64
	var priorStatus, priorPhase string
	err = tx.QueryRowContext(ctx, `
		SELECT id, status, phase FROM schedule_activations
		WHERE status = 'running'
		   OR (status IN ('repairing', 'pending', 'deferred_interval', 'deferred_capacity') AND due_at <= ?)
		ORDER BY CASE status WHEN 'running' THEN 0 WHEN 'repairing' THEN 1 ELSE 2 END,
		         due_at ASC, id ASC LIMIT 1`, now).Scan(&id, &priorStatus, &priorPhase)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("claim schedule activation: select: %w", err)
	}
	phase := "starting_surge"
	if priorStatus == "running" || priorStatus == "repairing" || priorPhase == "recovering" {
		phase = "recovering"
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE schedule_activations SET status = 'running', phase = 'starting_surge',
		defer_reason = '', started_at = COALESCE(started_at, ?),
		updated_at = CURRENT_TIMESTAMP WHERE id = ?`, now, id); err != nil {
		return nil, fmt.Errorf("claim schedule activation: update: %w", err)
	}
	if phase == "recovering" {
		if _, err := tx.ExecContext(ctx, `UPDATE schedule_activations SET phase = ? WHERE id = ?`, phase, id); err != nil {
			return nil, fmt.Errorf("claim schedule activation: mark recovery: %w", err)
		}
	}
	a, err := getActivationTx(ctx, tx, id)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("claim schedule activation: commit: %w", err)
	}
	committed = true
	return a, nil
}

func (s *Store) UpdateScheduleActivationProgress(id int64, phase string, surgeIndex, nextSlot int) error {
	res, err := s.db.Exec(`UPDATE schedule_activations
		SET phase = ?, surge_index = ?, next_slot = ?, updated_at = CURRENT_TIMESTAMP
		WHERE id = ? AND status = 'running'`, phase, surgeIndex, nextSlot, id)
	if err != nil {
		return fmt.Errorf("update schedule activation progress: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) DeferScheduleActivation(id int64, status, reason string, dueAt, deferredAt time.Time) error {
	if status != "deferred_interval" && status != "deferred_capacity" && status != "pending" && status != "repairing" {
		return fmt.Errorf("defer schedule activation: invalid status %q", status)
	}
	// Charge an attempt only when a rollout actually ran. Capacity admission is
	// not a rollout attempt, and charging at claim time is unsafe: if persisting
	// the capacity defer fails, reclaiming the running row would charge again.
	attemptIncrement := 1
	if status == "deferred_capacity" {
		attemptIncrement = 0
	}
	capacityIncrement := 0
	if status == "deferred_capacity" {
		capacityIncrement = 1
	}
	res, err := s.db.Exec(`UPDATE schedule_activations
		SET status = ?, phase = CASE WHEN ? = 'repairing' THEN phase ELSE 'pending' END,
		    due_at = ?, defer_reason = ?,
		    attempts = attempts + ?, capacity_deferrals = capacity_deferrals + ?,
		    capacity_deferred_at = CASE WHEN ? = 'deferred_capacity'
		        THEN COALESCE(capacity_deferred_at, ?) ELSE capacity_deferred_at END,
		    updated_at = CURRENT_TIMESTAMP
		WHERE id = ? AND status = 'running'`, status, status, dueAt, reason,
		attemptIncrement, capacityIncrement, status, deferredAt, id)
	if err != nil {
		return fmt.Errorf("defer schedule activation: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) FinishScheduleActivation(id int64, status, lastError string, finishedAt time.Time, countAttempt bool) error {
	switch status {
	case "succeeded", "failed", "not_needed", "blocked_unsupported", "target_deleted":
	default:
		return fmt.Errorf("finish schedule activation: invalid status %q", status)
	}
	ctx := context.Background()
	tx, err := s.d.beginWrite(ctx, s.rawDB(), scheduleActivationLockKey)
	if err != nil {
		return fmt.Errorf("finish schedule activation: begin: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()
	var appSlug, scheduleName, phase string
	var scheduleRunID sql.NullInt64
	var targetGeneration int64
	if err := tx.QueryRowContext(ctx, `SELECT app_slug, schedule_name, schedule_run_id,
		target_generation, phase FROM schedule_activations WHERE id = ? AND status = 'running'`, id).Scan(
		&appSlug, &scheduleName, &scheduleRunID, &targetGeneration, &phase,
	); errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	} else if err != nil {
		return fmt.Errorf("finish schedule activation: load audit snapshot: %w", err)
	}
	attemptIncrement := 0
	if countAttempt {
		attemptIncrement = 1
	}
	res, err := tx.ExecContext(ctx, `UPDATE schedule_activations
		SET status = ?, phase = 'complete', last_error = ?, finished_at = ?,
		    attempts = attempts + ?,
		    updated_at = CURRENT_TIMESTAMP
		WHERE id = ? AND status = 'running'`, status, lastError, finishedAt, attemptIncrement, id)
	if err != nil {
		return fmt.Errorf("finish schedule activation: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	if status == "succeeded" {
		rows, err := tx.QueryContext(ctx, `SELECT id, due_at, min_roll_interval_seconds
			FROM schedule_activations
			WHERE app_id = (SELECT app_id FROM schedule_activations WHERE id = ?)
			  AND status IN ('pending', 'deferred_interval', 'deferred_capacity')`, id)
		if err != nil {
			return fmt.Errorf("finish schedule activation: load queued damping: %w", err)
		}
		type queuedActivation struct {
			id       int64
			dueAt    time.Time
			interval int
		}
		var queued []queuedActivation
		for rows.Next() {
			var q queuedActivation
			if err := rows.Scan(&q.id, &q.dueAt, &q.interval); err != nil {
				_ = rows.Close()
				return fmt.Errorf("finish schedule activation: scan queued damping: %w", err)
			}
			queued = append(queued, q)
		}
		if err := rows.Close(); err != nil {
			return fmt.Errorf("finish schedule activation: close queued damping: %w", err)
		}
		for _, q := range queued {
			damped := finishedAt.Add(time.Duration(q.interval) * time.Second)
			if !damped.After(q.dueAt) {
				continue
			}
			if _, err := tx.ExecContext(ctx, `UPDATE schedule_activations
				SET due_at = ?, status = 'deferred_interval', defer_reason = 'minimum roll interval',
				    updated_at = CURRENT_TIMESTAMP WHERE id = ?`, damped, q.id); err != nil {
				return fmt.Errorf("finish schedule activation: rebase queued damping: %w", err)
			}
		}
	}
	detail, err := json.Marshal(struct {
		ActivationID     int64  `json:"activation_id"`
		ScheduleRunID    *int64 `json:"schedule_run_id,omitempty"`
		ScheduleName     string `json:"schedule_name"`
		TargetGeneration int64  `json:"target_generation"`
		Status           string `json:"status"`
		Phase            string `json:"phase"`
		Error            string `json:"error,omitempty"`
	}{
		ActivationID: id, ScheduleRunID: nullableInt64(scheduleRunID), ScheduleName: scheduleName,
		TargetGeneration: targetGeneration, Status: status, Phase: phase, Error: lastError,
	})
	if err != nil {
		return fmt.Errorf("finish schedule activation: encode audit detail: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO audit_events
		(action, resource_type, resource_id, detail, ip_address)
		VALUES ('schedule_activation_outcome', 'app', ?, ?, '')`, appSlug, string(detail)); err != nil {
		return fmt.Errorf("finish schedule activation: record audit outcome: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("finish schedule activation: commit: %w", err)
	}
	committed = true
	return nil
}

func nullableInt64(v sql.NullInt64) *int64 {
	if !v.Valid {
		return nil
	}
	n := v.Int64
	return &n
}

// RequeueRunningScheduleActivations makes a process crash retryable. Canonical
// slot generations let the rollout skip work that committed before the crash;
// an unpersisted surge is cleaned up by normal startup orphan recovery.
func (s *Store) RequeueRunningScheduleActivations(now time.Time) (int64, error) {
	res, err := s.db.Exec(`UPDATE schedule_activations
		SET status = 'repairing', phase = 'recovering', due_at = ?, defer_reason = 'server restarted during activation',
		updated_at = CURRENT_TIMESTAMP WHERE status = 'running'`, now)
	if err != nil {
		return 0, fmt.Errorf("requeue running schedule activations: %w", err)
	}
	return res.RowsAffected()
}

func (s *Store) ScheduleActivationInFlight(appID int64) (bool, error) {
	var n int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM schedule_activations a
		WHERE a.app_id = ?
		  AND a.status IN ('pending', 'deferred_interval', 'deferred_capacity', 'running', 'repairing')
		  AND (
			a.status IN ('running', 'repairing')
			OR EXISTS (
				SELECT 1 FROM replicas r
				WHERE r.app_id = a.app_id AND r.activation_id = a.id
				  AND (r.pid IS NOT NULL OR r.worker_id <> '' OR r.endpoint_url <> '')
			)
		  )`, appID).Scan(&n)
	return n > 0, err
}

// PruneScheduleActivations bounds terminal history while preserving every
// nonterminal action and the newest succeeded activation per app (the damper
// anchor). Retained activation rows keep their source run attributable because
// PruneScheduleRuns excludes referenced run IDs.
func (s *Store) PruneScheduleActivations(keepTerminalPerSchedule int) (int64, error) {
	if keepTerminalPerSchedule <= 0 {
		return 0, nil
	}
	res, err := s.db.Exec(`
		DELETE FROM schedule_activations
		WHERE id IN (
			SELECT id FROM (
				SELECT id, ROW_NUMBER() OVER (
					PARTITION BY schedule_id ORDER BY id DESC
				) AS terminal_rank
				FROM schedule_activations
				WHERE status IN (
					'succeeded', 'failed', 'cancelled', 'superseded', 'not_needed',
					'blocked_unsupported', 'target_deleted'
				)
			) ranked
			WHERE terminal_rank > ?
		)
		AND id NOT IN (
			SELECT succeeded.id FROM schedule_activations succeeded
			WHERE succeeded.status = 'succeeded' AND succeeded.app_id IS NOT NULL
			  AND NOT EXISTS (
				SELECT 1 FROM schedule_activations newer
				WHERE newer.app_id = succeeded.app_id AND newer.status = 'succeeded'
				  AND (newer.finished_at > succeeded.finished_at OR
				       (newer.finished_at = succeeded.finished_at AND newer.id > succeeded.id))
			  )
		)`, keepTerminalPerSchedule)
	if err != nil {
		return 0, fmt.Errorf("prune schedule activations: %w", err)
	}
	return res.RowsAffected()
}

func (s *Store) StampReplicaActivation(appID int64, index int, generation, activationID int64) error {
	res, err := s.db.Exec(`UPDATE replicas SET data_generation = ?, activation_id = ?, updated_at = `+s.d.nowEpoch()+`
		WHERE app_id = ? AND idx = ?`, generation, activationID, appID, index)
	if err != nil {
		return fmt.Errorf("stamp replica activation: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// UpsertActivationReplica persists runtime identity and activation attribution
// in one statement. Recovery must never observe a new PID whose generation or
// owning activation has not been recorded yet.
func (s *Store) UpsertActivationReplica(p UpsertReplicaParams, generation, activationID int64) error {
	desired := p.DesiredState
	if desired == "" {
		desired = "running"
	}
	_, err := s.db.Exec(`
		INSERT INTO replicas (app_id, idx, pid, port, status, provider, tier,
		                      endpoint_url, worker_id, app_version, desired_state,
		                      deployment_id, data_generation, activation_id,
		                      startup_peak_rss_bytes, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, `+s.d.nowEpoch()+`)
		ON CONFLICT(app_id, idx) DO UPDATE SET
			pid = excluded.pid, port = excluded.port, status = excluded.status,
			provider = excluded.provider, tier = excluded.tier,
			endpoint_url = excluded.endpoint_url, worker_id = excluded.worker_id,
			app_version = excluded.app_version, desired_state = excluded.desired_state,
			deployment_id = excluded.deployment_id,
			data_generation = excluded.data_generation, activation_id = excluded.activation_id,
			startup_peak_rss_bytes = CASE WHEN excluded.startup_peak_rss_bytes > 0
				THEN excluded.startup_peak_rss_bytes ELSE replicas.startup_peak_rss_bytes END,
			updated_at = excluded.updated_at`,
		p.AppID, p.Index, p.PID, p.Port, p.Status, p.Provider, p.Tier,
		p.EndpointURL, p.WorkerID, p.AppVersion, desired, p.DeploymentID,
		generation, activationID, p.StartupPeakRSSBytes,
	)
	if err != nil {
		return fmt.Errorf("upsert activation replica: %w", err)
	}
	return nil
}

// ClearReplicaRuntimeIdentity records a confirmed stop before a transient
// activation row is deleted. If deletion then fails, a retry can distinguish a
// durable stop tombstone from an unverified PID/container and finish cleanup
// without either leaking forever or guessing that a process is gone.
func (s *Store) ClearReplicaRuntimeIdentity(appID int64, index int) error {
	res, err := s.db.Exec(`UPDATE replicas SET pid = NULL, port = NULL, status = 'stopped',
		endpoint_url = '', worker_id = '', updated_at = `+s.d.nowEpoch()+`
		WHERE app_id = ? AND idx = ?`, appID, index)
	if err != nil {
		return fmt.Errorf("clear replica runtime identity: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// InvalidateReplicaSnapshot makes an old suspended process image impossible to
// resume while preserving its existing intent ("warm" for a shrunken slot,
// "stopped" for a fully hibernated app). The next wake therefore cold-boots.
func (s *Store) InvalidateReplicaSnapshot(appID int64, index int, generation, activationID int64) error {
	res, err := s.db.Exec(`UPDATE replicas SET pid = NULL, port = NULL, status = 'stopped',
		provider = '', endpoint_url = '', worker_id = '',
		data_generation = ?, activation_id = ?, updated_at = `+s.d.nowEpoch()+`
		WHERE app_id = ? AND idx = ?`, generation, activationID, appID, index)
	if err != nil {
		return fmt.Errorf("invalidate warm replica: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

func getActivationByRunTx(ctx context.Context, tx writeTx, runID int64) (*ScheduleActivation, error) {
	return scanScheduleActivation(tx.QueryRowContext(ctx,
		`SELECT `+activationColumns+` FROM schedule_activations WHERE schedule_run_id = ?`, runID))
}

func getActivationTx(ctx context.Context, tx writeTx, id int64) (*ScheduleActivation, error) {
	return scanScheduleActivation(tx.QueryRowContext(ctx,
		`SELECT `+activationColumns+` FROM schedule_activations WHERE id = ?`, id))
}

func scanScheduleActivation(row rowScanner) (*ScheduleActivation, error) {
	var a ScheduleActivation
	var appID, scheduleID, runID, superseded sql.NullInt64
	var capacityDeferred, started, finished sql.NullTime
	err := row.Scan(
		&a.ID, &appID, &a.AppSlug, &scheduleID, &a.ScheduleName, &runID, &a.Action, &a.MinRollIntervalSeconds,
		&a.RollFallback, &a.MaxDeferAgeSeconds, &a.TargetGeneration, &a.Status, &a.Phase, &a.DueAt, &a.DeferReason,
		&a.Attempts, &a.CapacityDeferrals, &capacityDeferred, &a.SurgeIndex, &a.NextSlot, &a.LastError, &superseded,
		&a.CreatedAt, &a.UpdatedAt, &started, &finished,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("scan schedule activation: %w", err)
	}
	if appID.Valid {
		v := appID.Int64
		a.AppID = &v
	}
	if scheduleID.Valid {
		v := scheduleID.Int64
		a.ScheduleID = &v
	}
	if runID.Valid {
		v := runID.Int64
		a.ScheduleRunID = &v
	}
	if superseded.Valid {
		v := superseded.Int64
		a.SupersededByID = &v
	}
	if capacityDeferred.Valid {
		v := capacityDeferred.Time
		a.CapacityDeferredAt = &v
	}
	if started.Valid {
		v := started.Time
		a.StartedAt = &v
	}
	if finished.Valid {
		v := finished.Time
		a.FinishedAt = &v
	}
	return &a, nil
}
