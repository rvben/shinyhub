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
	ID             int64
	AppID          int64
	Name           string
	CronExpr       string
	CommandJSON    string
	Enabled        bool
	TimeoutSeconds int
	OverlapPolicy  string
	MissedPolicy   string
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
	if def == nil {
		def = time.UTC
	}
	if tz != nil && *tz != "" {
		if loc, err := time.LoadLocation(*tz); err == nil {
			return loc
		}
	}
	return def
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
	AppID          int64
	Name           string
	CronExpr       string
	CommandJSON    string
	Enabled        bool
	TimeoutSeconds int
	OverlapPolicy  string
	MissedPolicy   string
	Timezone       *string
}

type UpdateScheduleParams struct {
	Name           *string
	CronExpr       *string
	CommandJSON    *string
	Enabled        *bool
	TimeoutSeconds *int
	OverlapPolicy  *string
	MissedPolicy   *string
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
			(app_id, name, cron_expr, command_json, enabled, timeout_seconds, overlap_policy, missed_policy, timezone)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
		RETURNING id`,
		p.AppID, p.Name, p.CronExpr, p.CommandJSON, boolToInt(p.Enabled), p.TimeoutSeconds, p.OverlapPolicy, p.MissedPolicy, tz,
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
		       overlap_policy, missed_policy, timezone, created_at, updated_at
		FROM app_schedules WHERE id = ?`, id)
	return scanSchedule(row)
}

// GetScheduleByName returns the schedule with the given name for the given app,
// or ErrNotFound when no such schedule exists.
func (s *Store) GetScheduleByName(appID int64, name string) (*Schedule, error) {
	row := s.db.QueryRow(`
		SELECT id, app_id, name, cron_expr, command_json, enabled, timeout_seconds,
		       overlap_policy, missed_policy, timezone, created_at, updated_at
		FROM app_schedules WHERE app_id = ? AND name = ?`, appID, name)
	return scanSchedule(row)
}

func (s *Store) ListSchedulesByApp(appID int64) ([]*Schedule, error) {
	rows, err := s.db.Query(`
		SELECT id, app_id, name, cron_expr, command_json, enabled, timeout_seconds,
		       overlap_policy, missed_policy, timezone, created_at, updated_at
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
		       overlap_policy, missed_policy, timezone, created_at, updated_at
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
	res, err := s.db.Exec(`DELETE FROM app_schedules WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("delete schedule: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
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
}

type InsertScheduleRunParams struct {
	ScheduleID        int64
	Status            string
	Trigger           string
	TriggeredByUserID *int64
	StartedAt         time.Time
	LogPath           string
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
		INSERT INTO schedule_runs (schedule_id, status, trigger, triggered_by_user_id, started_at, log_path)
		VALUES (?, ?, ?, ?, ?, ?)
		RETURNING id`,
		p.ScheduleID, p.Status, p.Trigger, uid, p.StartedAt, p.LogPath,
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

func (s *Store) ListScheduleRuns(scheduleID int64, limit, offset int) ([]*ScheduleRun, error) {
	rows, err := s.db.Query(`
		SELECT id, schedule_id, status, trigger, triggered_by_user_id, started_at,
		       finished_at, exit_code, log_path
		FROM schedule_runs WHERE schedule_id = ?
		ORDER BY started_at DESC LIMIT ? OFFSET ?`, scheduleID, limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*ScheduleRun{}
	for rows.Next() {
		var r ScheduleRun
		var uid sql.NullInt64
		var fin sql.NullTime
		var ex sql.NullInt64
		if err := rows.Scan(&r.ID, &r.ScheduleID, &r.Status, &r.Trigger, &uid,
			&r.StartedAt, &fin, &ex, &r.LogPath); err != nil {
			return nil, err
		}
		if uid.Valid {
			v := uid.Int64
			r.TriggeredByUserID = &v
		}
		if fin.Valid {
			v := fin.Time
			r.FinishedAt = &v
		}
		if ex.Valid {
			v := int(ex.Int64)
			r.ExitCode = &v
		}
		out = append(out, &r)
	}
	return out, rows.Err()
}

func (s *Store) GetScheduleRun(runID int64) (*ScheduleRun, error) {
	row := s.db.QueryRow(`
		SELECT id, schedule_id, status, trigger, triggered_by_user_id, started_at,
		       finished_at, exit_code, log_path
		FROM schedule_runs WHERE id = ?`, runID)
	var r ScheduleRun
	var uid sql.NullInt64
	var fin sql.NullTime
	var ex sql.NullInt64
	err := row.Scan(&r.ID, &r.ScheduleID, &r.Status, &r.Trigger, &uid,
		&r.StartedAt, &fin, &ex, &r.LogPath)
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
	if fin.Valid {
		v := fin.Time
		r.FinishedAt = &v
	}
	if ex.Valid {
		v := int(ex.Int64)
		r.ExitCode = &v
	}
	return &r, nil
}

// MarkRunningSchedulesInterrupted flips any rows still in 'running' state into
// 'interrupted'. Called at startup since we never resume in-flight runs.
func (s *Store) MarkRunningSchedulesInterrupted() (int64, error) {
	res, err := s.db.Exec(`
		UPDATE schedule_runs SET status = 'interrupted', finished_at = CURRENT_TIMESTAMP
		WHERE status = 'running'`)
	if err != nil {
		return 0, fmt.Errorf("mark interrupted: %w", err)
	}
	return res.RowsAffected()
}

// LastSuccessfulRun returns the most recent succeeded run for a schedule, used
// by missed-run catch-up. Returns ErrNotFound if there's never been one.
func (s *Store) LastSuccessfulRun(scheduleID int64) (*ScheduleRun, error) {
	row := s.db.QueryRow(`
		SELECT id, schedule_id, status, trigger, triggered_by_user_id, started_at,
		       finished_at, exit_code, log_path
		FROM schedule_runs WHERE schedule_id = ? AND status = 'succeeded'
		ORDER BY started_at DESC LIMIT 1`, scheduleID)
	var r ScheduleRun
	var uid sql.NullInt64
	var fin sql.NullTime
	var ex sql.NullInt64
	err := row.Scan(&r.ID, &r.ScheduleID, &r.Status, &r.Trigger, &uid,
		&r.StartedAt, &fin, &ex, &r.LogPath)
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
	if fin.Valid {
		v := fin.Time
		r.FinishedAt = &v
	}
	if ex.Valid {
		v := int(ex.Int64)
		r.ExitCode = &v
	}
	return &r, nil
}

// SchedulesNeedingFirstFireRetry returns the ids of enabled schedules whose
// run_on_register first-fire was interrupted by a service restart and has never
// succeeded, so the startup reconcile can re-fire it.
//
// A schedule qualifies when:
//   - it is enabled, and
//   - it has no successful run on record, and
//   - its most recent run with trigger='register' (the first-fire trigger) is
//     'interrupted'.
//
// This scopes recovery to first-fires only: a missed cron run re-fires on its
// next tick, but a first-fire's next tick can be hours away. Restricting to a
// LATEST register run that is 'interrupted' preserves the other policies - an
// operator 'cancelled' first-fire stays terminal, a 'failed' first-fire heals
// on the next deploy, and a schedule that ever succeeded is never re-fired.
//
// The gate is the run history, not the current manifest's run_on_register flag
// (which is a deploy-time instruction, never persisted). So if an operator
// removes run_on_register while a first-fire is interrupted and never
// succeeded, a restart still completes that one warm. This is intentional:
// finishing an in-progress warm an operator already requested is harmless and
// idempotent, and it avoids persisting deploy-time intent into the schedule row.
func (s *Store) SchedulesNeedingFirstFireRetry() ([]int64, error) {
	rows, err := s.db.Query(`
		SELECT sc.id
		FROM app_schedules sc
		WHERE sc.enabled = 1
		  AND NOT EXISTS (
		      SELECT 1 FROM schedule_runs r
		      WHERE r.schedule_id = sc.id AND r.status = 'succeeded'
		  )
		  AND (
		      SELECT r.status FROM schedule_runs r
		      WHERE r.schedule_id = sc.id AND r.trigger = 'register'
		      ORDER BY r.started_at DESC, r.id DESC
		      LIMIT 1
		  ) = 'interrupted'`)
	if err != nil {
		return nil, fmt.Errorf("schedules needing first-fire retry: %w", err)
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

type UpsertScheduleByNameParams struct {
	AppID          int64
	Name           string
	CronExpr       string
	CommandJSON    string
	Enabled        bool
	TimeoutSeconds int
	OverlapPolicy  string
	MissedPolicy   string
	Timezone       *string
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
  (app_id, name, cron_expr, command_json, enabled, timeout_seconds, overlap_policy, missed_policy, timezone)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(app_id, name) DO NOTHING
RETURNING id`,
		p.AppID, p.Name, p.CronExpr, p.CommandJSON,
		boolToInt(p.Enabled), p.TimeoutSeconds, p.OverlapPolicy, p.MissedPolicy, tz,
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
       overlap_policy = ?, missed_policy = ?, timezone = ?, updated_at = CURRENT_TIMESTAMP
 WHERE app_id = ? AND name = ?
RETURNING id`,
		p.CronExpr, p.CommandJSON, boolToInt(p.Enabled),
		p.TimeoutSeconds, p.OverlapPolicy, p.MissedPolicy, tz,
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
	Slug           string
	Name           string
	Enabled        bool
	CronExpr       string
	Timezone       *string
	CreatedAt      time.Time
	TimeoutSeconds int
	LastRunAt      *time.Time // started_at of the most recent run, nil if never run
	LastRunStatus  string     // status of that run, "" if never run
	LastSuccessAt  *time.Time // finished_at of the most recent succeeded run, nil if never
}

// EffectiveLocation resolves this schedule's timezone against the server
// default, mirroring Schedule.EffectiveLocation.
func (f *ScheduleFreshness) EffectiveLocation(def *time.Location) *time.Location {
	return resolveLocation(f.Timezone, def)
}

// ScheduleFreshness returns one row per schedule across all apps, with the last
// run and last success resolved via correlated subqueries (SQLite has no
// LATERAL; same shape as SchedulesNeedingFirstFireRetry). last success uses
// finished_at so a slow-but-recently-finished job reads as fresh.
func (s *Store) ScheduleFreshness() ([]ScheduleFreshness, error) {
	rows, err := s.db.Query(`
		SELECT a.slug, sc.name, sc.enabled, sc.cron_expr, sc.timezone, sc.created_at, sc.timeout_seconds,
		  (SELECT started_at  FROM schedule_runs WHERE schedule_id=sc.id ORDER BY started_at DESC LIMIT 1),
		  (SELECT status      FROM schedule_runs WHERE schedule_id=sc.id ORDER BY started_at DESC LIMIT 1),
		  (SELECT finished_at FROM schedule_runs WHERE schedule_id=sc.id AND status='succeeded' ORDER BY started_at DESC LIMIT 1)
		FROM app_schedules sc JOIN apps a ON a.id = sc.app_id
		ORDER BY a.slug, sc.name`)
	if err != nil {
		return nil, fmt.Errorf("schedule freshness: %w", err)
	}
	defer rows.Close()
	var out []ScheduleFreshness
	for rows.Next() {
		var fr ScheduleFreshness
		var enabled int // SQLite stores BOOLEAN as INTEGER; database/sql has no int->bool scan
		var tz sql.NullString
		var lastRunAt sql.NullTime
		var lastStatus sql.NullString
		var lastSuccess sql.NullTime
		if err := rows.Scan(&fr.Slug, &fr.Name, &enabled, &fr.CronExpr, &tz,
			&fr.CreatedAt, &fr.TimeoutSeconds, &lastRunAt, &lastStatus, &lastSuccess); err != nil {
			return nil, err
		}
		fr.Enabled = enabled != 0
		if tz.Valid && tz.String != "" {
			v := tz.String
			fr.Timezone = &v
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
		&tz, &sched.CreatedAt, &sched.UpdatedAt)
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
