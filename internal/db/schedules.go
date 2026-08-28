package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// sharedDataLockKey serializes all shared-data grants on Postgres so opposing
// grants (a->b and b->a) cannot both pass the cycle check before either inserts.
// The value is an arbitrary fixed advisory-lock key unique to this invariant.
const sharedDataLockKey int64 = 0x53484152 // "SHAR"

// ErrScheduleNameExists is returned by CreateSchedule when a schedule with the
// same name already exists for the given app. Callers that want idempotent
// create behaviour (e.g. --if-not-exists) should check with errors.Is.
var ErrScheduleNameExists = errors.New("schedule with that name already exists for this app")

// --- app_schedules ---

type Schedule struct {
	ID                     int64
	AppID                  int64
	Name                   string
	CronExpr               string
	CommandJSON            string
	Enabled                bool
	TimeoutSeconds         int
	OverlapPolicy          string
	MissedPolicy           string
	DeployTrigger          string
	OnSuccess              string
	MinRollIntervalSeconds int
	RollFallback           string
	MaxDeferAgeSeconds     int
	// Timezone is the optional per-schedule IANA timezone. nil means "inherit
	// the server default". A non-nil pointer to an empty string is normalised
	// to nil at the API layer before reaching the DB (empty = inherit).
	Timezone  *string
	CreatedAt time.Time
	UpdatedAt time.Time
}

// resolveLocation resolves an optional per-entity IANA timezone against a
// server default. nil/empty or an unloadable zone falls back to def (UTC if
// def is nil). Single source of truth for schedule timezone inheritance.
func resolveLocation(tz *string, def *time.Location) *time.Location {
	loc, _ := resolveLocationChecked(tz, def)
	return loc
}

func resolveLocationChecked(tz *string, def *time.Location) (*time.Location, error) {
	if def == nil {
		def = time.UTC
	}
	if tz != nil && *tz != "" {
		loc, err := time.LoadLocation(*tz)
		if err != nil {
			return def, fmt.Errorf("load schedule timezone %q: %w", *tz, err)
		}
		return loc, nil
	}
	return def, nil
}

// EffectiveLocation resolves the schedule's timezone with the given server
// default. Returns the resolved *time.Location.
//
// Resolution order:
//  1. s.Timezone (non-nil, non-empty) - use that IANA zone.
//  2. Otherwise return def (the server-configured default or UTC).
//
// If a stored timezone fails to load (corrupted DB row), def is used as a
// safe fallback. Delegates to resolveLocation, the single source of truth for
// this inherit/fallback logic.
func (s *Schedule) EffectiveLocation(def *time.Location) *time.Location {
	return resolveLocation(s.Timezone, def)
}

type CreateScheduleParams struct {
	AppID                  int64
	Name                   string
	CronExpr               string
	CommandJSON            string
	Enabled                bool
	TimeoutSeconds         int
	OverlapPolicy          string
	MissedPolicy           string
	DeployTrigger          string
	Timezone               *string
	OnSuccess              string
	MinRollIntervalSeconds int
	RollFallback           string
	MaxDeferAgeSeconds     int
}

type UpdateScheduleParams struct {
	Name                   *string
	CronExpr               *string
	CommandJSON            *string
	Enabled                *bool
	TimeoutSeconds         *int
	OverlapPolicy          *string
	MissedPolicy           *string
	DeployTrigger          *string
	OnSuccess              *string
	MinRollIntervalSeconds *int
	RollFallback           *string
	MaxDeferAgeSeconds     *int
	// Timezone uses a sentinel to distinguish three states:
	//   nil       - field not provided; leave as-is.
	//   non-nil pointer to empty string - clear to NULL (inherit).
	//   non-nil pointer to non-empty string - set to that zone.
	// The API layer is responsible for normalising empty-string client input
	// to a non-nil pointer before calling UpdateSchedule.
	Timezone *string
	// SetTimezone must be true for the Timezone field to be included in the
	// UPDATE, allowing nil (inherit/clear) to be distinguished from "not provided".
	SetTimezone bool
}

func (s *Store) CreateSchedule(p CreateScheduleParams) (int64, error) {
	var tz sql.NullString
	if p.Timezone != nil && *p.Timezone != "" {
		tz = sql.NullString{String: *p.Timezone, Valid: true}
	}
	var id int64
	err := s.db.QueryRow(`
		INSERT INTO app_schedules
			(app_id, name, cron_expr, command_json, enabled, timeout_seconds, overlap_policy, missed_policy, deploy_trigger, timezone,
			 on_success, min_roll_interval_seconds, roll_fallback, max_defer_age_seconds)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		RETURNING id`,
		p.AppID, p.Name, p.CronExpr, p.CommandJSON, boolToInt(p.Enabled), p.TimeoutSeconds, p.OverlapPolicy, p.MissedPolicy,
		normalizeDeployTrigger(p.DeployTrigger), tz,
		normalizeScheduleAction(p.OnSuccess), p.MinRollIntervalSeconds, normalizeRollFallback(p.RollFallback), p.MaxDeferAgeSeconds,
	).Scan(&id)
	if err != nil {
		if s.d.isUniqueViolation(err) {
			return 0, fmt.Errorf("create schedule: %w", ErrScheduleNameExists)
		}
		return 0, fmt.Errorf("create schedule: %w", err)
	}
	return id, nil
}

func (s *Store) GetSchedule(id int64) (*Schedule, error) {
	row := s.db.QueryRow(`
		SELECT id, app_id, name, cron_expr, command_json, enabled, timeout_seconds,
		       overlap_policy, missed_policy, deploy_trigger, timezone, on_success, min_roll_interval_seconds,
		       roll_fallback, max_defer_age_seconds, created_at, updated_at
		FROM app_schedules WHERE id = ?`, id)
	return scanSchedule(row)
}

// GetScheduleByName returns the schedule with the given name for the given app,
// or ErrNotFound when no such schedule exists.
func (s *Store) GetScheduleByName(appID int64, name string) (*Schedule, error) {
	row := s.db.QueryRow(`
		SELECT id, app_id, name, cron_expr, command_json, enabled, timeout_seconds,
		       overlap_policy, missed_policy, deploy_trigger, timezone, on_success, min_roll_interval_seconds,
		       roll_fallback, max_defer_age_seconds, created_at, updated_at
		FROM app_schedules WHERE app_id = ? AND name = ?`, appID, name)
	return scanSchedule(row)
}

func (s *Store) ListSchedulesByApp(appID int64) ([]*Schedule, error) {
	rows, err := s.db.Query(`
		SELECT id, app_id, name, cron_expr, command_json, enabled, timeout_seconds,
		       overlap_policy, missed_policy, deploy_trigger, timezone, on_success, min_roll_interval_seconds,
		       roll_fallback, max_defer_age_seconds, created_at, updated_at
		FROM app_schedules WHERE app_id = ? ORDER BY name`, appID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*Schedule{}
	for rows.Next() {
		sched, err := scanSchedule(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, sched)
	}
	return out, rows.Err()
}

func (s *Store) ListEnabledSchedules() ([]*Schedule, error) {
	rows, err := s.db.Query(`
		SELECT id, app_id, name, cron_expr, command_json, enabled, timeout_seconds,
		       overlap_policy, missed_policy, deploy_trigger, timezone, on_success, min_roll_interval_seconds,
		       roll_fallback, max_defer_age_seconds, created_at, updated_at
		FROM app_schedules WHERE enabled = 1`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*Schedule{}
	for rows.Next() {
		sched, err := scanSchedule(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, sched)
	}
	return out, rows.Err()
}

func (s *Store) UpdateSchedule(id int64, p UpdateScheduleParams) error {
	sets := []string{}
	args := []any{}
	if p.Name != nil {
		sets = append(sets, "name = ?")
		args = append(args, *p.Name)
	}
	if p.CronExpr != nil {
		sets = append(sets, "cron_expr = ?")
		args = append(args, *p.CronExpr)
	}
	if p.CommandJSON != nil {
		sets = append(sets, "command_json = ?")
		args = append(args, *p.CommandJSON)
	}
	if p.Enabled != nil {
		sets = append(sets, "enabled = ?")
		args = append(args, boolToInt(*p.Enabled))
	}
	if p.TimeoutSeconds != nil {
		sets = append(sets, "timeout_seconds = ?")
		args = append(args, *p.TimeoutSeconds)
	}
	if p.OverlapPolicy != nil {
		sets = append(sets, "overlap_policy = ?")
		args = append(args, *p.OverlapPolicy)
	}
	if p.MissedPolicy != nil {
		sets = append(sets, "missed_policy = ?")
		args = append(args, *p.MissedPolicy)
	}
	if p.DeployTrigger != nil {
		sets = append(sets, "deploy_trigger = ?")
		args = append(args, normalizeDeployTrigger(*p.DeployTrigger))
	}
	if p.OnSuccess != nil {
		sets = append(sets, "on_success = ?")
		args = append(args, normalizeScheduleAction(*p.OnSuccess))
	}
	if p.MinRollIntervalSeconds != nil {
		sets = append(sets, "min_roll_interval_seconds = ?")
		args = append(args, *p.MinRollIntervalSeconds)
	}
	if p.RollFallback != nil {
		sets = append(sets, "roll_fallback = ?")
		args = append(args, normalizeRollFallback(*p.RollFallback))
	}
	if p.MaxDeferAgeSeconds != nil {
		sets = append(sets, "max_defer_age_seconds = ?")
		args = append(args, *p.MaxDeferAgeSeconds)
	}
	if p.SetTimezone {
		sets = append(sets, "timezone = ?")
		if p.Timezone != nil && *p.Timezone != "" {
			args = append(args, sql.NullString{String: *p.Timezone, Valid: true})
		} else {
			args = append(args, sql.NullString{})
		}
	}
	if len(sets) == 0 {
		return nil
	}
	sets = append(sets, "updated_at = CURRENT_TIMESTAMP")
	args = append(args, id)
	q := "UPDATE app_schedules SET " + strings.Join(sets, ", ") + " WHERE id = ?"
	res, err := s.db.Exec(q, args...)
	if err != nil {
		return fmt.Errorf("update schedule: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) DeleteSchedule(id int64) error {
	res, err := s.db.Exec(`DELETE FROM app_schedules WHERE id = ?
		AND NOT EXISTS (SELECT 1 FROM schedule_data_uncertainty WHERE schedule_id = ?)`, id, id)
	if err != nil {
		return fmt.Errorf("delete schedule: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// DeleteScheduleIfIdle atomically preserves an executing run and any durable
// activation outcome that has not reached a terminal state. Keeping both
// predicates in this DELETE is important: a run completion changes its run to
// terminal and inserts the activation in one transaction, so every statement
// snapshot sees at least one blocker. A separate preflight query would leave a
// check-then-delete window that could cascade the source run out from under the
// newly-created activation.
func (s *Store) DeleteScheduleIfIdle(id int64) (bool, error) {
	res, err := s.db.Exec(`DELETE FROM app_schedules
		WHERE id = ? AND NOT EXISTS (
			SELECT 1 FROM schedule_runs WHERE schedule_id = ? AND status = 'running'
		) AND NOT EXISTS (
			SELECT 1 FROM schedule_activations
			WHERE schedule_id = ? AND status IN (
				'pending', 'deferred_interval', 'deferred_capacity', 'repairing', 'running'
			)
		) AND NOT EXISTS (
			SELECT 1 FROM schedule_data_uncertainty WHERE schedule_id = ?
		)`, id, id, id, id)
	if err != nil {
		return false, fmt.Errorf("delete idle schedule: %w", err)
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// DeleteSchedulePlaceholderIfUnused removes only a disabled planning row that
// never admitted a run. A terminal failed candidate run is intentionally a
// blocker: deleting its placeholder would cascade away the only diagnostic
// run/provenance record for a failed first deploy.
func (s *Store) DeleteSchedulePlaceholderIfUnused(id int64) (bool, error) {
	res, err := s.db.Exec(`DELETE FROM app_schedules
		WHERE id = ? AND enabled = 0 AND deploy_trigger = 'never'
		  AND NOT EXISTS (SELECT 1 FROM schedule_runs WHERE schedule_id = ?)`, id, id)
	if err != nil {
		return false, fmt.Errorf("delete unused schedule placeholder: %w", err)
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// --- schedule_runs ---

type ScheduleRun struct {
	ID                int64      `json:"id"`
	ScheduleID        int64      `json:"schedule_id"`
	Status            string     `json:"status"`
	Trigger           string     `json:"trigger"`
	TriggeredByUserID *int64     `json:"triggered_by_user_id"`
	StartedAt         time.Time  `json:"started_at"`
	FinishedAt        *time.Time `json:"finished_at"`
	// ExitCode is the scheduled command's process exit code. It is nil (JSON
	// null) until the run reaches a terminal state, and stays nil for an
	// interrupted run that never observed a process exit. A non-nil value is
	// always the real exit status, so a caller can trust exit_code == 0 to mean
	// "succeeded" without also inspecting Status.
	ExitCode *int `json:"exit_code"`
	// LogPath is the server-side filesystem path of the run's log file. It
	// is an internal detail consumed only by the log-streaming handler and
	// must never be serialized to API clients.
	LogPath string `json:"-"`
	// Activation fields keep the job outcome and its serving-data outcome
	// attributable on the same history row without conflating their states.
	OnSuccess              string `json:"on_success"`
	MinRollIntervalSeconds int    `json:"min_roll_interval_seconds"`
	RollFallback           string `json:"roll_fallback"`
	MaxDeferAgeSeconds     int    `json:"max_defer_age_seconds"`
	DeploymentID           *int64 `json:"deployment_id"`
	AppVersion             string `json:"app_version"`
	ContentDigest          string `json:"content_digest"`
	ProducerFingerprint    string `json:"producer_fingerprint"`
	ProducerCommandJSON    string `json:"-"`
	PublishesData          bool   `json:"publishes_data"`
	DeployObligationID     *int64 `json:"deploy_obligation_id,omitempty"`
	TargetGeneration       *int64 `json:"target_generation,omitempty"`
	ActivationID           *int64 `json:"activation_id,omitempty"`
	ActivationStatus       string `json:"activation_status,omitempty"`
	ActivationPhase        string `json:"activation_phase,omitempty"`
	ActivationError        string `json:"activation_error,omitempty"`
}

type InsertScheduleRunParams struct {
	ScheduleID             int64
	Status                 string
	Trigger                string
	TriggeredByUserID      *int64
	StartedAt              time.Time
	LogPath                string
	OnSuccess              string
	MinRollIntervalSeconds int
	RollFallback           string
	MaxDeferAgeSeconds     int
	DeploymentID           *int64
	AppVersion             string
	ContentDigest          string
	ProducerFingerprint    string
	ProducerCommandJSON    string
	PublishesData          bool
	DeployObligationID     *int64
}

type FinishScheduleRunParams struct {
	RunID  int64
	Status string
	// ExitCode is the process exit code to record. nil writes SQL NULL, used
	// for a run that finished without observing a process exit (interrupted by
	// a service restart).
	ExitCode   *int
	FinishedAt time.Time
}

func (s *Store) InsertScheduleRun(p InsertScheduleRunParams) (int64, error) {
	var uid sql.NullInt64
	if p.TriggeredByUserID != nil {
		uid = sql.NullInt64{Int64: *p.TriggeredByUserID, Valid: true}
	}
	var id int64
	err := s.db.QueryRow(`
		INSERT INTO schedule_runs (schedule_id, status, trigger, triggered_by_user_id, started_at, log_path,
		                           on_success, min_roll_interval_seconds, roll_fallback, max_defer_age_seconds,
		                           deployment_id, app_version, content_digest,
		                           producer_fingerprint, producer_command_json, publishes_data, deploy_obligation_id,
		                           provenance_admission)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 1)
		RETURNING id`,
		p.ScheduleID, p.Status, p.Trigger, uid, p.StartedAt, p.LogPath,
		normalizeScheduleAction(p.OnSuccess), p.MinRollIntervalSeconds,
		normalizeRollFallback(p.RollFallback), p.MaxDeferAgeSeconds,
		p.DeploymentID, p.AppVersion, p.ContentDigest,
		p.ProducerFingerprint, p.ProducerCommandJSON, boolToInt(p.PublishesData), p.DeployObligationID,
	).Scan(&id)
	if err != nil {
		return 0, fmt.Errorf("insert schedule run: %w", err)
	}
	return id, nil
}

func (s *Store) FinishScheduleRun(p FinishScheduleRunParams) error {
	var ec sql.NullInt64
	if p.ExitCode != nil {
		ec = sql.NullInt64{Int64: int64(*p.ExitCode), Valid: true}
	}
	res, err := s.db.Exec(`
		UPDATE schedule_runs SET status = ?, exit_code = ?, finished_at = ? WHERE id = ?`,
		p.Status, ec, p.FinishedAt, p.RunID,
	)
	if err != nil {
		return fmt.Errorf("finish schedule run: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// SetScheduleRunLogPath updates the log_path column on a schedule_runs row.
// Returns ErrNotFound if no row matches.
func (s *Store) SetScheduleRunLogPath(runID int64, logPath string) error {
	res, err := s.db.Exec(`UPDATE schedule_runs SET log_path = ? WHERE id = ?`, logPath, runID)
	if err != nil {
		return fmt.Errorf("set schedule run log path: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// ListAllScheduleIDs returns the IDs of every schedule across all apps. Used by
// the maintenance loop to bound each schedule's run history.
func (s *Store) ListAllScheduleIDs() ([]int64, error) {
	rows, err := s.db.Query(`SELECT id FROM app_schedules`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// PruneScheduleRuns keeps the newest keep runs for the given schedule and
// deletes the rest, returning the number removed. A non-positive keep is a
// no-op so callers can disable bounding. Called after each run completes so a
// frequently-firing schedule cannot accumulate run history without bound.
func (s *Store) PruneScheduleRuns(scheduleID int64, keep int) (int64, error) {
	if keep <= 0 {
		return 0, nil
	}
	res, err := s.db.Exec(`
		DELETE FROM schedule_runs
		WHERE schedule_id = ?
		  AND id NOT IN (
		    SELECT schedule_run_id FROM schedule_activations
		    WHERE schedule_run_id IS NOT NULL
		  )
		  AND id NOT IN (
		    SELECT schedule_run_id FROM schedule_data_uncertainty
		    WHERE schedule_run_id IS NOT NULL
		  )
		  AND id NOT IN (
		    SELECT id FROM schedule_runs
		    WHERE schedule_id = ?
		    ORDER BY started_at DESC, id DESC
		    LIMIT ?
		  )`, scheduleID, scheduleID, keep)
	if err != nil {
		return 0, fmt.Errorf("prune schedule runs: %w", err)
	}
	return res.RowsAffected()
}

// CountScheduleRuns returns the total number of run rows for a schedule,
// independent of any limit/offset page. Used to report an accurate total
// alongside a server-paginated page of run history.
func (s *Store) CountScheduleRuns(scheduleID int64) (int, error) {
	var n int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM schedule_runs WHERE schedule_id = ?`, scheduleID).Scan(&n)
	return n, err
}

const scheduleRunSelectColumns = `
	r.id, r.schedule_id, r.status, r.trigger, r.triggered_by_user_id, r.started_at,
	r.finished_at, r.exit_code, r.log_path, r.on_success, r.min_roll_interval_seconds,
	r.roll_fallback, r.max_defer_age_seconds, r.deployment_id, r.app_version, r.content_digest,
	r.producer_fingerprint, r.producer_command_json, r.publishes_data, r.deploy_obligation_id,
	r.target_generation, a.id, COALESCE(a.status, ''), COALESCE(a.phase, ''), COALESCE(a.last_error, '')`

func (s *Store) ListScheduleRuns(scheduleID int64, limit, offset int) ([]*ScheduleRun, error) {
	rows, err := s.db.Query(`SELECT `+scheduleRunSelectColumns+`
		FROM schedule_runs r LEFT JOIN schedule_activations a ON a.schedule_run_id = r.id
		WHERE r.schedule_id = ?
		ORDER BY r.started_at DESC LIMIT ? OFFSET ?`, scheduleID, limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*ScheduleRun{}
	for rows.Next() {
		r, err := scanScheduleRun(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func (s *Store) GetScheduleRun(runID int64) (*ScheduleRun, error) {
	row := s.db.QueryRow(`SELECT `+scheduleRunSelectColumns+`
		FROM schedule_runs r LEFT JOIN schedule_activations a ON a.schedule_run_id = r.id
		WHERE r.id = ?`, runID)
	return scanScheduleRun(row)
}

// MarkRunningSchedulesInterrupted terminalizes inherited rows after runtime
// orphan fencing. Every potential data writer is conservatively assigned a
// physical write sequence, because a crashed owner cannot prove whether that
// process reached its mutable-data command before disappearing.
func (s *Store) MarkRunningSchedulesInterrupted() (int64, error) {
	ctx := context.Background()
	tx, err := s.d.beginWrite(ctx, s.rawDB(), scheduleConvergenceLockKey)
	if err != nil {
		return 0, fmt.Errorf("mark interrupted: begin: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck
	type inheritedWriter struct{ runID, appID int64 }
	rows, err := tx.QueryContext(ctx, `
		SELECT r.id, sc.app_id
		FROM schedule_runs r JOIN app_schedules sc ON sc.id = r.schedule_id
		WHERE r.status = 'running' AND r.publishes_data = 1
		ORDER BY sc.app_id, r.id`)
	if err != nil {
		return 0, fmt.Errorf("mark interrupted: list writers: %w", err)
	}
	var writers []inheritedWriter
	for rows.Next() {
		var writer inheritedWriter
		if err := rows.Scan(&writer.runID, &writer.appID); err != nil {
			_ = rows.Close()
			return 0, fmt.Errorf("mark interrupted: scan writer: %w", err)
		}
		writers = append(writers, writer)
	}
	if err := rows.Close(); err != nil {
		return 0, fmt.Errorf("mark interrupted: close writers: %w", err)
	}
	for _, writer := range writers {
		var sequence int64
		if err := tx.QueryRowContext(ctx, `
			UPDATE apps SET data_write_sequence = data_write_sequence + 1,
			                updated_at = CURRENT_TIMESTAMP
			WHERE id = ? RETURNING data_write_sequence`, writer.appID).Scan(&sequence); err != nil {
			return 0, fmt.Errorf("mark interrupted: allocate app %d write sequence: %w", writer.appID, err)
		}
		if _, err := tx.ExecContext(ctx, `
			UPDATE schedule_runs
			SET status = 'interrupted', finished_at = CURRENT_TIMESTAMP,
			    data_write_sequence = ?
			WHERE id = ? AND status = 'running'`, sequence, writer.runID); err != nil {
			return 0, fmt.Errorf("mark interrupted: terminalize writer %d: %w", writer.runID, err)
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO schedule_data_uncertainty
				(schedule_id, data_write_sequence, schedule_run_id, status, recorded_at)
			SELECT schedule_id, ?, id, 'interrupted', CURRENT_TIMESTAMP
			FROM schedule_runs WHERE id = ?
			ON CONFLICT(schedule_id) DO UPDATE SET
				data_write_sequence = excluded.data_write_sequence,
				schedule_run_id = excluded.schedule_run_id,
				status = excluded.status,
				recorded_at = excluded.recorded_at`, sequence, writer.runID); err != nil {
			return 0, fmt.Errorf("mark interrupted: record writer %d uncertainty: %w", writer.runID, err)
		}
	}
	res, err := tx.ExecContext(ctx, `
		UPDATE schedule_runs SET status = 'interrupted', finished_at = CURRENT_TIMESTAMP
		WHERE status = 'running'`)
	if err != nil {
		return 0, fmt.Errorf("mark interrupted: terminalize remaining rows: %w", err)
	}
	nonWriters, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("mark interrupted: rows affected: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("mark interrupted: commit: %w", err)
	}
	return int64(len(writers)) + nonWriters, nil
}

func (s *Store) CountLegacyUnfencedScheduleRuns() (int64, error) {
	var count int64
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM legacy_unfenced_schedule_runs`).Scan(&count); err != nil {
		return 0, fmt.Errorf("count legacy unfenced schedule runs: %w", err)
	}
	return count, nil
}

// LegacyUnfencedScheduleRun identifies a process admitted by a pre-provenance
// server and captured during migration. Its process tree cannot be recovered
// safely from the database alone.
type LegacyUnfencedScheduleRun struct {
	RunID        int64
	RunStatus    string
	ScheduleID   int64
	ScheduleName string
	AppID        int64
	AppSlug      string
}

// ListLegacyUnfencedScheduleRuns returns the exact operator-facing identities
// behind the startup fence. It is used by the offline recovery command while
// the server is stopped.
func (s *Store) ListLegacyUnfencedScheduleRuns() ([]LegacyUnfencedScheduleRun, error) {
	rows, err := s.db.Query(`
		SELECT r.id, r.status, sc.id, sc.name, a.id, a.slug
		FROM legacy_unfenced_schedule_runs legacy
		JOIN schedule_runs r ON r.id = legacy.run_id
		JOIN app_schedules sc ON sc.id = r.schedule_id
		JOIN apps a ON a.id = sc.app_id
		ORDER BY a.slug, sc.name, r.id`)
	if err != nil {
		return nil, fmt.Errorf("list legacy unfenced schedule runs: %w", err)
	}
	defer rows.Close()
	var out []LegacyUnfencedScheduleRun
	for rows.Next() {
		var run LegacyUnfencedScheduleRun
		if err := rows.Scan(&run.RunID, &run.RunStatus, &run.ScheduleID, &run.ScheduleName, &run.AppID, &run.AppSlug); err != nil {
			return nil, fmt.Errorf("scan legacy unfenced schedule run: %w", err)
		}
		out = append(out, run)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list legacy unfenced schedule runs: %w", err)
	}
	return out, nil
}

// ResolveLegacyUnfencedScheduleRuns is the only supported way to clear the
// legacy process fence. The caller must first establish that every old process
// tree is gone. Clearing evidence and materializing conservative data-write
// uncertainty are one transaction, so a crash can never make consumers
// runnable between those operations.
func (s *Store) ResolveLegacyUnfencedScheduleRuns() (int64, error) {
	ctx := context.Background()
	tx, err := s.d.beginWrite(ctx, s.rawDB(), scheduleConvergenceLockKey)
	if err != nil {
		return 0, fmt.Errorf("resolve legacy schedule writers: begin: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck

	type legacyWriter struct {
		runID, scheduleID, appID int64
	}
	rows, err := tx.QueryContext(ctx, `
		SELECT r.id, r.schedule_id, sc.app_id
		FROM legacy_unfenced_schedule_runs legacy
		JOIN schedule_runs r ON r.id = legacy.run_id
		JOIN app_schedules sc ON sc.id = r.schedule_id
		ORDER BY sc.app_id, r.id`)
	if err != nil {
		return 0, fmt.Errorf("resolve legacy schedule writers: list: %w", err)
	}
	var writers []legacyWriter
	for rows.Next() {
		var writer legacyWriter
		if err := rows.Scan(&writer.runID, &writer.scheduleID, &writer.appID); err != nil {
			_ = rows.Close()
			return 0, fmt.Errorf("resolve legacy schedule writers: scan: %w", err)
		}
		writers = append(writers, writer)
	}
	if err := rows.Close(); err != nil {
		return 0, fmt.Errorf("resolve legacy schedule writers: close: %w", err)
	}

	for _, writer := range writers {
		var sequence int64
		if err := tx.QueryRowContext(ctx, `
			UPDATE apps SET data_write_sequence = data_write_sequence + 1,
			                updated_at = CURRENT_TIMESTAMP
			WHERE id = ? RETURNING data_write_sequence`, writer.appID).Scan(&sequence); err != nil {
			return 0, fmt.Errorf("resolve legacy schedule writer %d: allocate write sequence: %w", writer.runID, err)
		}
		if _, err := tx.ExecContext(ctx, `
			UPDATE schedule_runs
			SET status = CASE WHEN status = 'running' THEN 'interrupted' ELSE status END,
			    finished_at = CASE WHEN status = 'running' THEN CURRENT_TIMESTAMP ELSE finished_at END,
			    data_write_sequence = ?
			WHERE id = ?`, sequence, writer.runID); err != nil {
			return 0, fmt.Errorf("resolve legacy schedule writer %d: terminalize: %w", writer.runID, err)
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO schedule_data_uncertainty
				(schedule_id, data_write_sequence, schedule_run_id, status, recorded_at)
			VALUES (?, ?, ?, 'legacy_unfenced', CURRENT_TIMESTAMP)
			ON CONFLICT(schedule_id) DO UPDATE SET
				data_write_sequence = excluded.data_write_sequence,
				schedule_run_id = excluded.schedule_run_id,
				status = excluded.status,
				recorded_at = excluded.recorded_at`, writer.scheduleID, sequence, writer.runID); err != nil {
			return 0, fmt.Errorf("resolve legacy schedule writer %d: record uncertainty: %w", writer.runID, err)
		}
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM legacy_unfenced_schedule_runs`); err != nil {
		return 0, fmt.Errorf("resolve legacy schedule writers: clear fence: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("resolve legacy schedule writers: commit: %w", err)
	}
	return int64(len(writers)), nil
}

// LastSuccessfulRun returns the most recent succeeded run for a schedule, used
// by missed-run catch-up. Returns ErrNotFound if there's never been one.
func (s *Store) LastSuccessfulRun(scheduleID int64) (*ScheduleRun, error) {
	row := s.db.QueryRow(`SELECT `+scheduleRunSelectColumns+`
		FROM schedule_runs r LEFT JOIN schedule_activations a ON a.schedule_run_id = r.id
		WHERE r.schedule_id = ? AND r.status = 'succeeded'
		ORDER BY r.started_at DESC LIMIT 1`, scheduleID)
	return scanScheduleRun(row)
}

// LatestDeployRunID returns the highest deploy-triggered run for one exact
// bundle digest, regardless of status; 0 when there is none.
func (s *Store) LatestDeployRunID(scheduleID int64, contentDigest string) (int64, error) {
	var id int64
	err := s.db.QueryRow(`
		SELECT COALESCE(MAX(id), 0) FROM schedule_runs
		WHERE schedule_id = ? AND trigger = 'deploy' AND content_digest = ?`, scheduleID, contentDigest).Scan(&id)
	if err != nil {
		return 0, fmt.Errorf("latest deploy run for schedule %d digest %q: %w", scheduleID, contentDigest, err)
	}
	return id, nil
}

type UpsertScheduleByNameParams struct {
	AppID                  int64
	Name                   string
	CronExpr               string
	CommandJSON            string
	Enabled                bool
	TimeoutSeconds         int
	OverlapPolicy          string
	MissedPolicy           string
	DeployTrigger          string
	Timezone               *string
	OnSuccess              string
	MinRollIntervalSeconds int
	RollFallback           string
	MaxDeferAgeSeconds     int
}

type UpsertScheduleByNameResult struct {
	ID      int64
	Created bool
}

// UpsertSchedulesByName atomically applies a complete manifest schedule batch.
// No caller can observe a partially new declaration set if a later row fails.
func (s *Store) UpsertSchedulesByName(params []UpsertScheduleByNameParams) ([]UpsertScheduleByNameResult, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return nil, fmt.Errorf("begin schedule batch: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck
	results := make([]UpsertScheduleByNameResult, 0, len(params))
	for _, p := range params {
		var tz sql.NullString
		if p.Timezone != nil && *p.Timezone != "" {
			tz = sql.NullString{String: *p.Timezone, Valid: true}
		}
		var id int64
		scanErr := tx.QueryRow(`
INSERT INTO app_schedules
  (app_id, name, cron_expr, command_json, enabled, timeout_seconds, overlap_policy, missed_policy, deploy_trigger, timezone,
   on_success, min_roll_interval_seconds, roll_fallback, max_defer_age_seconds)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(app_id, name) DO NOTHING
RETURNING id`,
			p.AppID, p.Name, p.CronExpr, p.CommandJSON, boolToInt(p.Enabled),
			p.TimeoutSeconds, p.OverlapPolicy, p.MissedPolicy, normalizeDeployTrigger(p.DeployTrigger), tz,
			normalizeScheduleAction(p.OnSuccess), p.MinRollIntervalSeconds,
			normalizeRollFallback(p.RollFallback), p.MaxDeferAgeSeconds).Scan(&id)
		created := scanErr == nil
		if scanErr != nil && !errors.Is(scanErr, sql.ErrNoRows) {
			return nil, fmt.Errorf("insert schedule %q: %w", p.Name, scanErr)
		}
		if !created {
			if err := tx.QueryRow(`
UPDATE app_schedules
   SET cron_expr = ?, command_json = ?, enabled = ?, timeout_seconds = ?,
	   overlap_policy = ?, missed_policy = ?, deploy_trigger = ?, timezone = ?, on_success = ?,
	   min_roll_interval_seconds = ?, roll_fallback = ?, max_defer_age_seconds = ?, updated_at = CURRENT_TIMESTAMP
 WHERE app_id = ? AND name = ?
RETURNING id`,
				p.CronExpr, p.CommandJSON, boolToInt(p.Enabled), p.TimeoutSeconds,
				p.OverlapPolicy, p.MissedPolicy, normalizeDeployTrigger(p.DeployTrigger), tz,
				normalizeScheduleAction(p.OnSuccess), p.MinRollIntervalSeconds,
				normalizeRollFallback(p.RollFallback), p.MaxDeferAgeSeconds,
				p.AppID, p.Name).Scan(&id); err != nil {
				return nil, fmt.Errorf("update schedule (app=%d, name=%q): %w", p.AppID, p.Name, err)
			}
		}
		results = append(results, UpsertScheduleByNameResult{ID: id, Created: created})
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit schedule batch: %w", err)
	}
	return results, nil
}

// RecordDeploymentScheduleSnapshot stores the complete effective declaration
// set for one candidate deployment. The recorded bit distinguishes a genuine
// empty set from a deployment created before snapshot support.
func (s *Store) RecordDeploymentScheduleSnapshot(deploymentID, appID int64) error {
	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("record deployment schedule snapshot: begin: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck
	if _, err := tx.Exec(`DELETE FROM deployment_schedule_snapshots WHERE deployment_id = ?`, deploymentID); err != nil {
		return err
	}
	if _, err := tx.Exec(`
		INSERT INTO deployment_schedule_snapshots
			(deployment_id, name, cron_expr, command_json, enabled, timeout_seconds,
			 overlap_policy, missed_policy, deploy_trigger, timezone, on_success,
			 min_roll_interval_seconds, roll_fallback, max_defer_age_seconds)
		SELECT ?, name, cron_expr, command_json, enabled, timeout_seconds,
		       overlap_policy, missed_policy, deploy_trigger, timezone, on_success,
		       min_roll_interval_seconds, roll_fallback, max_defer_age_seconds
		FROM app_schedules WHERE app_id = ?`, deploymentID, appID); err != nil {
		return fmt.Errorf("record deployment schedule snapshot rows: %w", err)
	}
	res, err := tx.Exec(`UPDATE deployments SET schedule_snapshot_recorded = 1
		WHERE id = ? AND app_id = ? AND status = ?`, deploymentID, appID, DeploymentPending)
	if err != nil {
		return err
	}
	if n, err := res.RowsAffected(); err != nil || n != 1 {
		return fmt.Errorf("record deployment schedule snapshot: deployment %d is not pending", deploymentID)
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	return nil
}

// DeploymentScheduleSnapshot returns immutable declarations captured for a
// deployment. ErrNotFound means the deployment predates snapshot support; an
// empty slice with nil error is an explicitly recorded empty declaration set.
func (s *Store) DeploymentScheduleSnapshot(deploymentID int64) ([]*Schedule, error) {
	var recorded int
	if err := s.db.QueryRow(`SELECT schedule_snapshot_recorded FROM deployments WHERE id = ?`, deploymentID).Scan(&recorded); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	if recorded == 0 {
		return nil, ErrNotFound
	}
	rows, err := s.db.Query(`
		SELECT name, cron_expr, command_json, enabled, timeout_seconds,
		       overlap_policy, missed_policy, deploy_trigger, timezone, on_success,
		       min_roll_interval_seconds, roll_fallback, max_defer_age_seconds
		FROM deployment_schedule_snapshots WHERE deployment_id = ? ORDER BY name`, deploymentID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Schedule
	for rows.Next() {
		var schedule Schedule
		var enabled int
		var timezone sql.NullString
		if err := rows.Scan(&schedule.Name, &schedule.CronExpr, &schedule.CommandJSON, &enabled,
			&schedule.TimeoutSeconds, &schedule.OverlapPolicy, &schedule.MissedPolicy,
			&schedule.DeployTrigger, &timezone, &schedule.OnSuccess,
			&schedule.MinRollIntervalSeconds, &schedule.RollFallback,
			&schedule.MaxDeferAgeSeconds); err != nil {
			return nil, err
		}
		schedule.Enabled = enabled != 0
		if timezone.Valid {
			v := timezone.String
			schedule.Timezone = &v
		}
		out = append(out, &schedule)
	}
	return out, rows.Err()
}

// RestoreDeploymentPriorScheduleSnapshot rolls declarations back to the exact
// set captured atomically when a deployment intent was created. Startup uses
// this before failing an interrupted pending deployment, closing the crash
// window between declaration publication and deployment promotion.
func (s *Store) RestoreDeploymentPriorScheduleSnapshot(deploymentID, appID int64) ([]*Schedule, error) {
	var recorded int
	if err := s.db.QueryRow(`SELECT prior_schedule_snapshot_recorded FROM deployments WHERE id = ?`, deploymentID).Scan(&recorded); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	if recorded == 0 {
		return nil, ErrNotFound
	}
	rows, err := s.db.Query(`
		SELECT name, cron_expr, command_json, enabled, timeout_seconds,
		       overlap_policy, missed_policy, deploy_trigger, timezone, on_success,
		       min_roll_interval_seconds, roll_fallback, max_defer_age_seconds
		FROM deployment_prior_schedule_snapshots WHERE deployment_id = ? ORDER BY name`, deploymentID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var snapshots []*Schedule
	for rows.Next() {
		var schedule Schedule
		var enabled int
		var timezone sql.NullString
		if err := rows.Scan(&schedule.Name, &schedule.CronExpr, &schedule.CommandJSON, &enabled,
			&schedule.TimeoutSeconds, &schedule.OverlapPolicy, &schedule.MissedPolicy,
			&schedule.DeployTrigger, &timezone, &schedule.OnSuccess,
			&schedule.MinRollIntervalSeconds, &schedule.RollFallback,
			&schedule.MaxDeferAgeSeconds); err != nil {
			return nil, err
		}
		schedule.Enabled = enabled != 0
		if timezone.Valid {
			v := timezone.String
			schedule.Timezone = &v
		}
		snapshots = append(snapshots, &schedule)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return s.RestoreScheduleDeclarations(appID, snapshots)
}

// RestoreDeploymentScheduleSnapshot atomically replaces the app's effective
// declaration set with a rollback target's snapshot. Extra current schedules
// are retained for history but disabled; matching names preserve stable IDs.
func (s *Store) RestoreDeploymentScheduleSnapshot(deploymentID, appID int64) ([]*Schedule, error) {
	snapshots, err := s.DeploymentScheduleSnapshot(deploymentID)
	if err != nil {
		return nil, err
	}
	return s.RestoreScheduleDeclarations(appID, snapshots)
}

// RestoreScheduleDeclarations atomically replaces an app's effective schedule
// set from a caller-held pre-mutation snapshot. Deploy failure recovery uses it
// while producer gates are held, including for an app with no prior deployment.
func (s *Store) RestoreScheduleDeclarations(appID int64, snapshots []*Schedule) ([]*Schedule, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback() //nolint:errcheck
	if _, err := tx.Exec(`
		UPDATE app_schedules SET enabled = 0, deploy_trigger = 'never', updated_at = CURRENT_TIMESTAMP
		WHERE app_id = ?`, appID); err != nil {
		return nil, err
	}
	for _, schedule := range snapshots {
		var timezone sql.NullString
		if schedule.Timezone != nil && *schedule.Timezone != "" {
			timezone = sql.NullString{String: *schedule.Timezone, Valid: true}
		}
		if _, err := tx.Exec(`
			INSERT INTO app_schedules
				(app_id, name, cron_expr, command_json, enabled, timeout_seconds,
				 overlap_policy, missed_policy, deploy_trigger, timezone, on_success,
				 min_roll_interval_seconds, roll_fallback, max_defer_age_seconds)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT(app_id, name) DO UPDATE SET
				cron_expr = excluded.cron_expr, command_json = excluded.command_json,
				enabled = excluded.enabled, timeout_seconds = excluded.timeout_seconds,
				overlap_policy = excluded.overlap_policy, missed_policy = excluded.missed_policy,
				deploy_trigger = excluded.deploy_trigger, timezone = excluded.timezone,
				on_success = excluded.on_success,
				min_roll_interval_seconds = excluded.min_roll_interval_seconds,
				roll_fallback = excluded.roll_fallback,
				max_defer_age_seconds = excluded.max_defer_age_seconds,
				updated_at = CURRENT_TIMESTAMP`,
			appID, schedule.Name, schedule.CronExpr, schedule.CommandJSON, boolToInt(schedule.Enabled),
			schedule.TimeoutSeconds, schedule.OverlapPolicy, schedule.MissedPolicy,
			schedule.DeployTrigger, timezone, schedule.OnSuccess,
			schedule.MinRollIntervalSeconds, schedule.RollFallback,
			schedule.MaxDeferAgeSeconds); err != nil {
			return nil, fmt.Errorf("restore schedule %q: %w", schedule.Name, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return s.ListSchedulesByApp(appID)
}

// UpsertScheduleByName performs an atomic insert-or-update keyed on
// (app_id, name). Returns the row id and whether a new row was created.
//
// SQLite has no built-in way to tell INSERT from UPDATE in a single
// UPSERT (no equivalent to Postgres's xmax check), and callers need
// that signal to emit schedule_create vs schedule_update audit events.
// The implementation issues `INSERT ... ON CONFLICT DO NOTHING` first:
// SQLite acquires the database write lock at that point and resolves
// the unique-constraint check inside the engine, so a concurrent caller
// cannot observe the same gap and race into a duplicate insert. The
// follow-up UPDATE...RETURNING runs in the same transaction under the
// same write lock.
func (s *Store) UpsertScheduleByName(p UpsertScheduleByNameParams) (int64, bool, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return 0, false, fmt.Errorf("begin: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck

	var tz sql.NullString
	if p.Timezone != nil && *p.Timezone != "" {
		tz = sql.NullString{String: *p.Timezone, Valid: true}
	}

	var insertedID int64
	scanErr := tx.QueryRow(`
INSERT INTO app_schedules
  (app_id, name, cron_expr, command_json, enabled, timeout_seconds, overlap_policy, missed_policy, deploy_trigger, timezone,
   on_success, min_roll_interval_seconds, roll_fallback, max_defer_age_seconds)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(app_id, name) DO NOTHING
RETURNING id`,
		p.AppID, p.Name, p.CronExpr, p.CommandJSON,
		boolToInt(p.Enabled), p.TimeoutSeconds, p.OverlapPolicy, p.MissedPolicy,
		normalizeDeployTrigger(p.DeployTrigger), tz,
		normalizeScheduleAction(p.OnSuccess), p.MinRollIntervalSeconds,
		normalizeRollFallback(p.RollFallback), p.MaxDeferAgeSeconds,
	).Scan(&insertedID)
	if scanErr != nil && !errors.Is(scanErr, sql.ErrNoRows) {
		return 0, false, fmt.Errorf("insert schedule: %w", scanErr)
	}
	if scanErr == nil {
		// Row was inserted; no conflict.
		if err := tx.Commit(); err != nil {
			return 0, false, fmt.Errorf("commit insert: %w", err)
		}
		return insertedID, true, nil
	}

	var id int64
	err = tx.QueryRow(`
UPDATE app_schedules
   SET cron_expr = ?, command_json = ?, enabled = ?, timeout_seconds = ?,
	   overlap_policy = ?, missed_policy = ?, deploy_trigger = ?, timezone = ?, on_success = ?,
	   min_roll_interval_seconds = ?, roll_fallback = ?, max_defer_age_seconds = ?, updated_at = CURRENT_TIMESTAMP
 WHERE app_id = ? AND name = ?
RETURNING id`,
		p.CronExpr, p.CommandJSON, boolToInt(p.Enabled),
		p.TimeoutSeconds, p.OverlapPolicy, p.MissedPolicy, normalizeDeployTrigger(p.DeployTrigger), tz,
		normalizeScheduleAction(p.OnSuccess), p.MinRollIntervalSeconds,
		normalizeRollFallback(p.RollFallback), p.MaxDeferAgeSeconds,
		p.AppID, p.Name,
	).Scan(&id)
	if err != nil {
		return 0, false, fmt.Errorf("update schedule (app=%d, name=%q): %w", p.AppID, p.Name, err)
	}
	if err := tx.Commit(); err != nil {
		return 0, false, fmt.Errorf("commit update: %w", err)
	}
	return id, false, nil
}

// --- app_shared_data ---

type SharedDataMount struct {
	ID          int64
	AppID       int64
	SourceAppID int64
	SourceSlug  string // joined from apps.slug at query time
	CreatedAt   time.Time
}

// Shared-data grant errors. These are typed so the API layer can map them to
// precise status codes (400/409) instead of leaking a raw 500.
var (
	// ErrSelfMount is returned when an app tries to mount its own data dir.
	ErrSelfMount = errors.New("cannot mount data from self")
	// ErrDuplicateMount is returned when the same source is already mounted.
	ErrDuplicateMount = errors.New("data already mounted from this source")
	// ErrSharedDataCycle is returned when a grant would close a read cycle.
	ErrSharedDataCycle = errors.New("mount would create a circular dependency")
)

func (s *Store) GrantSharedData(consumerAppID, sourceAppID int64) error {
	if consumerAppID == sourceAppID {
		return ErrSelfMount
	}
	ctx := context.Background()

	// A grant means "consumer reads source". A cycle forms if the source can
	// already (transitively) read the consumer, so adding this edge closes a
	// loop. The cycle check and the insert must be atomic: beginWrite takes
	// the write lock up front (BEGIN IMMEDIATE on SQLite; a transaction plus
	// pg_advisory_xact_lock on Postgres) so two opposing grants (a->b and
	// b->a) serialize here instead of both passing the check before either
	// inserts.
	tx, err := s.d.beginWrite(ctx, s.rawDB(), sharedDataLockKey)
	if err != nil {
		return fmt.Errorf("grant shared data: begin: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()

	cyclic, err := sharedDataReaches(ctx, tx, sourceAppID, consumerAppID)
	if err != nil {
		return fmt.Errorf("grant shared data: %w", err)
	}
	if cyclic {
		return ErrSharedDataCycle
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO app_shared_data (app_id, source_app_id) VALUES (?, ?)`,
		consumerAppID, sourceAppID,
	); err != nil {
		if s.d.isUniqueViolation(err) {
			return ErrDuplicateMount
		}
		return fmt.Errorf("grant shared data: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("grant shared data: commit: %w", err)
	}
	committed = true
	return nil
}

// sharedDataQuerier is the read-only subset of both writeTx and *boundDB used
// by the reachability probe, so it can run standalone or inside a grant transaction.
type sharedDataQuerier interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// sharedDataReaches reports whether startAppID can reach targetAppID by following
// "reads" edges (app_id -> source_app_id) transitively.
func (s *Store) sharedDataReaches(startAppID, targetAppID int64) (bool, error) {
	return sharedDataReaches(context.Background(), s.db, startAppID, targetAppID)
}

func sharedDataReaches(ctx context.Context, q sharedDataQuerier, startAppID, targetAppID int64) (bool, error) {
	var hit int
	err := q.QueryRowContext(ctx, `
		WITH RECURSIVE reach(id) AS (
			SELECT source_app_id FROM app_shared_data WHERE app_id = ?
			UNION
			SELECT sd.source_app_id FROM app_shared_data sd
			JOIN reach r ON sd.app_id = r.id
		)
		SELECT 1 FROM reach WHERE id = ? LIMIT 1`,
		startAppID, targetAppID,
	).Scan(&hit)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

func (s *Store) RevokeSharedData(consumerAppID, sourceAppID int64) error {
	res, err := s.db.Exec(`
		DELETE FROM app_shared_data WHERE app_id = ? AND source_app_id = ?`,
		consumerAppID, sourceAppID,
	)
	if err != nil {
		return fmt.Errorf("revoke shared data: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) ListSharedDataSources(consumerAppID int64) ([]*SharedDataMount, error) {
	rows, err := s.db.Query(`
		SELECT sd.id, sd.app_id, sd.source_app_id, a.slug, sd.created_at
		FROM app_shared_data sd
		JOIN apps a ON a.id = sd.source_app_id
		WHERE sd.app_id = ? ORDER BY a.slug`, consumerAppID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*SharedDataMount{}
	for rows.Next() {
		var m SharedDataMount
		if err := rows.Scan(&m.ID, &m.AppID, &m.SourceAppID, &m.SourceSlug, &m.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, &m)
	}
	return out, rows.Err()
}

// ScheduleFreshness is one row of the cross-fleet freshness view: a schedule
// joined to its app, plus the last run and last successful run. It feeds the
// Prometheus collector, the `schedule status` CLI, and the admin banner.
type ScheduleFreshness struct {
	ScheduleID              int64
	Slug                    string
	Name                    string
	Enabled                 bool
	CronExpr                string
	Timezone                *string
	CreatedAt               time.Time
	TimeoutSeconds          int
	DeployTrigger           string
	CurrentDeploymentID     *int64
	CurrentAppVersion       string
	CurrentContentDigest    string
	LastRunID               *int64     // id of the most recent run, nil if never run
	LastRunAt               *time.Time // started_at of the most recent run, nil if never run
	LastRunStatus           string     // status of that run, "" if never run
	LastSuccessAt           *time.Time // finished_at of the most recent succeeded run, nil if never
	ProducerDeploymentID    *int64
	ProducerAppVersion      string
	ProducerContentDigest   string
	ActiveRunID             *int64     // newest status=running row, nil when none is active
	ActiveRunAt             *time.Time // started_at for ActiveRunID
	ActiveRunContentDigest  string
	DeployTriggerSatisfied  bool
	ProducerRepairRequired  bool
	ProducerFingerprint     string
	ProducerPublishedAt     *time.Time
	ConvergenceObligationID *int64
	ConvergenceStatus       string
	ConvergenceRunID        *int64
	ConvergenceError        string
	ActivationStatus        string
	ActivationPhase         string
	ActivationCreatedAt     *time.Time
	ActivationUpdatedAt     *time.Time
	ActivationDueAt         *time.Time
	ActivationGeneration    *int64
	ActivationError         string
}

// EffectiveLocation resolves this schedule's timezone against the server
// default, mirroring Schedule.EffectiveLocation.
func (f *ScheduleFreshness) EffectiveLocation(def *time.Location) *time.Location {
	return resolveLocation(f.Timezone, def)
}

// EffectiveLocationChecked preserves an invalid stored timezone as unknown for
// strict health/convergence consumers. Runtime scheduling retains the tolerant
// fallback above so one corrupted row cannot crash scheduler startup.
func (f *ScheduleFreshness) EffectiveLocationChecked(def *time.Location) (*time.Location, error) {
	return resolveLocationChecked(f.Timezone, def)
}

// ScheduleFreshness returns one row per schedule across all apps, with the last
// run, last success, authoritative producer, and current convergence
// obligation resolved via correlated subqueries (SQLite has no LATERAL).
// Last success uses finished_at so a slow-but-recently-finished job reads as
// fresh; producer provenance is deliberately independent of bounded history.
func (s *Store) ScheduleFreshness() ([]ScheduleFreshness, error) {
	return s.scheduleFreshness("")
}

// ScheduleFreshnessByApp returns the same freshness rows as ScheduleFreshness
// restricted to one app, so the per-app schedule list can report last run and
// staleness without scanning the fleet. Both share one query body, so the two
// views can never disagree about what "last success" means.
func (s *Store) ScheduleFreshnessByApp(appID int64) ([]ScheduleFreshness, error) {
	return s.scheduleFreshness("WHERE sc.app_id = ?", appID)
}

// scheduleFreshness runs the freshness query with an optional WHERE clause.
// The clause is a constant supplied by the callers above, never user input.
func (s *Store) scheduleFreshness(where string, args ...any) ([]ScheduleFreshness, error) {
	rows, err := s.db.Query(`
		SELECT sc.id, a.slug, sc.name, CASE WHEN sc.enabled = 1 THEN 1 ELSE 0 END,
		  sc.cron_expr, sc.timezone, sc.created_at, sc.timeout_seconds, sc.deploy_trigger,
		  (SELECT id FROM deployments WHERE app_id=sc.app_id AND status='succeeded' ORDER BY id DESC LIMIT 1),
		  (SELECT version FROM deployments WHERE app_id=sc.app_id AND status='succeeded' ORDER BY id DESC LIMIT 1),
		  (SELECT content_digest FROM deployments WHERE app_id=sc.app_id AND status='succeeded' ORDER BY id DESC LIMIT 1),
		  (SELECT id          FROM schedule_runs WHERE schedule_id=sc.id ORDER BY started_at DESC, id DESC LIMIT 1),
		  (SELECT started_at  FROM schedule_runs WHERE schedule_id=sc.id ORDER BY started_at DESC, id DESC LIMIT 1),
		  (SELECT status      FROM schedule_runs WHERE schedule_id=sc.id ORDER BY started_at DESC, id DESC LIMIT 1),
		  (SELECT finished_at FROM schedule_runs WHERE schedule_id=sc.id AND status='succeeded' ORDER BY started_at DESC, id DESC LIMIT 1),
		  (SELECT deployment_id FROM schedule_producer_state WHERE schedule_id=sc.id),
		  (SELECT app_version FROM schedule_producer_state WHERE schedule_id=sc.id),
		  (SELECT content_digest FROM schedule_producer_state WHERE schedule_id=sc.id),
		  (SELECT id         FROM schedule_runs WHERE schedule_id=sc.id AND status='running' ORDER BY started_at DESC, id DESC LIMIT 1),
		  (SELECT started_at FROM schedule_runs WHERE schedule_id=sc.id AND status='running' ORDER BY started_at DESC, id DESC LIMIT 1),
		  (SELECT content_digest FROM schedule_runs WHERE schedule_id=sc.id AND status='running' ORDER BY started_at DESC, id DESC LIMIT 1),
		  CASE
		    WHEN sc.deploy_trigger = 'never' AND NOT EXISTS (
		      SELECT 1 FROM schedule_data_uncertainty uncertainty WHERE uncertainty.schedule_id=sc.id
		    ) THEN 1
		    WHEN sc.deploy_trigger = 'first_deploy' AND EXISTS (
		      SELECT 1 FROM schedule_producer_state ps WHERE ps.schedule_id=sc.id
		    ) AND NOT EXISTS (
		      SELECT 1 FROM schedule_data_uncertainty uncertainty
		      WHERE uncertainty.schedule_id=sc.id
		    ) THEN 1
		    WHEN sc.deploy_trigger = 'bundle_change' AND EXISTS (
		      SELECT 1 FROM schedule_producer_state ps
		      WHERE ps.schedule_id=sc.id AND ps.content_digest=(
		        SELECT content_digest FROM deployments WHERE app_id=sc.app_id AND status='succeeded' ORDER BY id DESC LIMIT 1
		      ) AND ps.producer_command_json=sc.command_json
		    ) AND NOT EXISTS (
		      SELECT 1 FROM schedule_data_uncertainty uncertainty
		      WHERE uncertainty.schedule_id=sc.id
		    ) THEN 1
		    ELSE 0
		  END,
		  CASE WHEN EXISTS (
		    SELECT 1 FROM schedule_data_uncertainty uncertainty WHERE uncertainty.schedule_id=sc.id
		  ) THEN 1 ELSE 0 END,
		  (SELECT producer_fingerprint FROM schedule_producer_state WHERE schedule_id=sc.id),
		  (SELECT published_at FROM schedule_producer_state WHERE schedule_id=sc.id),
		  (SELECT id FROM schedule_deploy_obligations o
		   WHERE o.schedule_id=sc.id
		     AND o.deployment_id=(SELECT id FROM deployments WHERE app_id=sc.app_id AND status='succeeded' ORDER BY id DESC LIMIT 1)
		     AND o.producer_command_json=sc.command_json ORDER BY o.id DESC LIMIT 1),
		  (SELECT status FROM schedule_deploy_obligations o
		   WHERE o.schedule_id=sc.id
		     AND o.deployment_id=(SELECT id FROM deployments WHERE app_id=sc.app_id AND status='succeeded' ORDER BY id DESC LIMIT 1)
		     AND o.producer_command_json=sc.command_json ORDER BY o.id DESC LIMIT 1),
		  (SELECT schedule_run_id FROM schedule_deploy_obligations o
		   WHERE o.schedule_id=sc.id
		     AND o.deployment_id=(SELECT id FROM deployments WHERE app_id=sc.app_id AND status='succeeded' ORDER BY id DESC LIMIT 1)
		     AND o.producer_command_json=sc.command_json ORDER BY o.id DESC LIMIT 1),
		  (SELECT last_error FROM schedule_deploy_obligations o
		   WHERE o.schedule_id=sc.id
		     AND o.deployment_id=(SELECT id FROM deployments WHERE app_id=sc.app_id AND status='succeeded' ORDER BY id DESC LIMIT 1)
		     AND o.producer_command_json=sc.command_json ORDER BY o.id DESC LIMIT 1),
		  sa.status, sa.phase, sa.created_at, sa.updated_at, sa.due_at, sa.target_generation,
		  CASE WHEN sa.last_error <> '' THEN sa.last_error ELSE sa.defer_reason END
		FROM app_schedules sc JOIN apps a ON a.id = sc.app_id
		LEFT JOIN schedule_activations sa ON sa.id = (
			SELECT id FROM schedule_activations
			WHERE schedule_id = sc.id
			ORDER BY CASE WHEN status IN ('running', 'repairing') THEN 0 ELSE 1 END, id DESC
			LIMIT 1
		)
		`+where+`
		ORDER BY a.slug, sc.name`, args...)
	if err != nil {
		return nil, fmt.Errorf("schedule freshness: %w", err)
	}
	defer rows.Close()
	var out []ScheduleFreshness
	for rows.Next() {
		var fr ScheduleFreshness
		var enabled int // SQLite stores BOOLEAN as INTEGER; database/sql has no int->bool scan
		var tz sql.NullString
		var currentDeploymentID sql.NullInt64
		var currentVersion, currentDigest sql.NullString
		var lastRunID sql.NullInt64
		var lastRunAt sql.NullTime
		var lastStatus sql.NullString
		var lastSuccess sql.NullTime
		var producerDeploymentID sql.NullInt64
		var producerVersion, producerDigest sql.NullString
		var activeRunID sql.NullInt64
		var activeRunAt sql.NullTime
		var activeRunDigest sql.NullString
		var deploySatisfied int
		var producerRepairRequired int
		var producerFingerprint sql.NullString
		var producerPublishedAt sql.NullTime
		var obligationID, obligationRunID sql.NullInt64
		var convergenceStatus, convergenceError sql.NullString
		var activationStatus, activationPhase, activationError sql.NullString
		var activationCreated, activationUpdated, activationDue sql.NullTime
		var activationGeneration sql.NullInt64
		if err := rows.Scan(&fr.ScheduleID, &fr.Slug, &fr.Name, &enabled, &fr.CronExpr, &tz,
			&fr.CreatedAt, &fr.TimeoutSeconds, &fr.DeployTrigger, &currentDeploymentID, &currentVersion, &currentDigest,
			&lastRunID, &lastRunAt, &lastStatus, &lastSuccess, &producerDeploymentID, &producerVersion, &producerDigest,
			&activeRunID, &activeRunAt, &activeRunDigest, &deploySatisfied, &producerRepairRequired,
			&producerFingerprint, &producerPublishedAt, &obligationID, &convergenceStatus, &obligationRunID, &convergenceError,
			&activationStatus, &activationPhase, &activationCreated,
			&activationUpdated, &activationDue, &activationGeneration, &activationError); err != nil {
			return nil, err
		}
		fr.Enabled = enabled != 0
		fr.DeployTriggerSatisfied = deploySatisfied != 0
		fr.ProducerRepairRequired = producerRepairRequired != 0
		fr.ProducerFingerprint = producerFingerprint.String
		if producerPublishedAt.Valid {
			v := producerPublishedAt.Time
			fr.ProducerPublishedAt = &v
		}
		if obligationID.Valid {
			v := obligationID.Int64
			fr.ConvergenceObligationID = &v
		}
		fr.ConvergenceStatus = convergenceStatus.String
		if obligationRunID.Valid {
			v := obligationRunID.Int64
			fr.ConvergenceRunID = &v
		}
		fr.ConvergenceError = convergenceError.String
		if currentDeploymentID.Valid {
			v := currentDeploymentID.Int64
			fr.CurrentDeploymentID = &v
		}
		fr.CurrentAppVersion = currentVersion.String
		fr.CurrentContentDigest = currentDigest.String
		if tz.Valid && tz.String != "" {
			v := tz.String
			fr.Timezone = &v
		}
		if lastRunID.Valid {
			v := lastRunID.Int64
			fr.LastRunID = &v
		}
		if lastRunAt.Valid {
			v := lastRunAt.Time
			fr.LastRunAt = &v
		}
		if lastStatus.Valid {
			fr.LastRunStatus = lastStatus.String
		}
		if lastSuccess.Valid {
			v := lastSuccess.Time
			fr.LastSuccessAt = &v
		}
		if producerDeploymentID.Valid {
			v := producerDeploymentID.Int64
			fr.ProducerDeploymentID = &v
		}
		fr.ProducerAppVersion = producerVersion.String
		fr.ProducerContentDigest = producerDigest.String
		if activeRunID.Valid {
			v := activeRunID.Int64
			fr.ActiveRunID = &v
		}
		if activeRunAt.Valid {
			v := activeRunAt.Time
			fr.ActiveRunAt = &v
		}
		fr.ActiveRunContentDigest = activeRunDigest.String
		fr.ActivationStatus = activationStatus.String
		fr.ActivationPhase = activationPhase.String
		fr.ActivationError = activationError.String
		if activationCreated.Valid {
			v := activationCreated.Time
			fr.ActivationCreatedAt = &v
		}
		if activationUpdated.Valid {
			v := activationUpdated.Time
			fr.ActivationUpdatedAt = &v
		}
		if activationDue.Valid {
			v := activationDue.Time
			fr.ActivationDueAt = &v
		}
		if activationGeneration.Valid {
			v := activationGeneration.Int64
			fr.ActivationGeneration = &v
		}
		out = append(out, fr)
	}
	return out, rows.Err()
}

// --- helpers ---

type rowScanner interface {
	Scan(dest ...any) error
}

func scanSchedule(s rowScanner) (*Schedule, error) {
	var sched Schedule
	var enabled int
	var tz sql.NullString
	err := s.Scan(&sched.ID, &sched.AppID, &sched.Name, &sched.CronExpr, &sched.CommandJSON,
		&enabled, &sched.TimeoutSeconds, &sched.OverlapPolicy, &sched.MissedPolicy,
		&sched.DeployTrigger, &tz, &sched.OnSuccess, &sched.MinRollIntervalSeconds, &sched.RollFallback,
		&sched.MaxDeferAgeSeconds, &sched.CreatedAt, &sched.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	sched.Enabled = enabled != 0
	if tz.Valid {
		sched.Timezone = &tz.String
	}
	return &sched, nil
}

func scanScheduleRun(s rowScanner) (*ScheduleRun, error) {
	var r ScheduleRun
	var uid, deploymentID, deployObligationID, targetGeneration, activationID sql.NullInt64
	var publishesData int
	var fin sql.NullTime
	var ex sql.NullInt64
	err := s.Scan(&r.ID, &r.ScheduleID, &r.Status, &r.Trigger, &uid,
		&r.StartedAt, &fin, &ex, &r.LogPath, &r.OnSuccess, &r.MinRollIntervalSeconds,
		&r.RollFallback, &r.MaxDeferAgeSeconds, &deploymentID, &r.AppVersion, &r.ContentDigest,
		&r.ProducerFingerprint, &r.ProducerCommandJSON, &publishesData, &deployObligationID,
		&targetGeneration, &activationID, &r.ActivationStatus, &r.ActivationPhase, &r.ActivationError)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if uid.Valid {
		v := uid.Int64
		r.TriggeredByUserID = &v
	}
	r.PublishesData = publishesData != 0
	if deploymentID.Valid {
		v := deploymentID.Int64
		r.DeploymentID = &v
	}
	if deployObligationID.Valid {
		v := deployObligationID.Int64
		r.DeployObligationID = &v
	}
	if fin.Valid {
		v := fin.Time
		r.FinishedAt = &v
	}
	if ex.Valid {
		v := int(ex.Int64)
		r.ExitCode = &v
	}
	if targetGeneration.Valid {
		v := targetGeneration.Int64
		r.TargetGeneration = &v
	}
	if activationID.Valid {
		v := activationID.Int64
		r.ActivationID = &v
	}
	return &r, nil
}

func normalizeScheduleAction(action string) string {
	if strings.TrimSpace(action) == "" {
		return "none"
	}
	return strings.TrimSpace(action)
}

func normalizeRollFallback(fallback string) string {
	if strings.TrimSpace(fallback) == "" {
		return "defer"
	}
	return strings.TrimSpace(fallback)
}

func normalizeDeployTrigger(trigger string) string {
	trigger = strings.TrimSpace(trigger)
	if trigger == "" {
		return "never"
	}
	return trigger
}
