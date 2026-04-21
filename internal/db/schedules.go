package db

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

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
	CreatedAt      time.Time
	UpdatedAt      time.Time
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
}

type UpdateScheduleParams struct {
	Name           *string
	CronExpr       *string
	CommandJSON    *string
	Enabled        *bool
	TimeoutSeconds *int
	OverlapPolicy  *string
	MissedPolicy   *string
}

func (s *Store) CreateSchedule(p CreateScheduleParams) (int64, error) {
	res, err := s.db.Exec(`
		INSERT INTO app_schedules
			(app_id, name, cron_expr, command_json, enabled, timeout_seconds, overlap_policy, missed_policy)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		p.AppID, p.Name, p.CronExpr, p.CommandJSON, boolToInt(p.Enabled), p.TimeoutSeconds, p.OverlapPolicy, p.MissedPolicy,
	)
	if err != nil {
		return 0, fmt.Errorf("create schedule: %w", err)
	}
	return res.LastInsertId()
}

func (s *Store) GetSchedule(id int64) (*Schedule, error) {
	row := s.db.QueryRow(`
		SELECT id, app_id, name, cron_expr, command_json, enabled, timeout_seconds,
		       overlap_policy, missed_policy, created_at, updated_at
		FROM app_schedules WHERE id = ?`, id)
	return scanSchedule(row)
}

func (s *Store) ListSchedulesByApp(appID int64) ([]*Schedule, error) {
	rows, err := s.db.Query(`
		SELECT id, app_id, name, cron_expr, command_json, enabled, timeout_seconds,
		       overlap_policy, missed_policy, created_at, updated_at
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
		       overlap_policy, missed_policy, created_at, updated_at
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
	ID                int64
	ScheduleID        int64
	Status            string
	Trigger           string
	TriggeredByUserID *int64
	StartedAt         time.Time
	FinishedAt        *time.Time
	ExitCode          int
	LogPath           string
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
	RunID      int64
	Status     string
	ExitCode   int
	FinishedAt time.Time
}

func (s *Store) InsertScheduleRun(p InsertScheduleRunParams) (int64, error) {
	var uid sql.NullInt64
	if p.TriggeredByUserID != nil {
		uid = sql.NullInt64{Int64: *p.TriggeredByUserID, Valid: true}
	}
	res, err := s.db.Exec(`
		INSERT INTO schedule_runs (schedule_id, status, trigger, triggered_by_user_id, started_at, log_path)
		VALUES (?, ?, ?, ?, ?, ?)`,
		p.ScheduleID, p.Status, p.Trigger, uid, p.StartedAt, p.LogPath,
	)
	if err != nil {
		return 0, fmt.Errorf("insert schedule run: %w", err)
	}
	return res.LastInsertId()
}

func (s *Store) FinishScheduleRun(p FinishScheduleRunParams) error {
	res, err := s.db.Exec(`
		UPDATE schedule_runs SET status = ?, exit_code = ?, finished_at = ? WHERE id = ?`,
		p.Status, p.ExitCode, p.FinishedAt, p.RunID,
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

func (s *Store) ListScheduleRuns(scheduleID int64, limit, offset int) ([]*ScheduleRun, error) {
	rows, err := s.db.Query(`
		SELECT id, schedule_id, status, trigger, triggered_by_user_id, started_at,
		       finished_at, COALESCE(exit_code, 0), log_path
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
		if err := rows.Scan(&r.ID, &r.ScheduleID, &r.Status, &r.Trigger, &uid,
			&r.StartedAt, &fin, &r.ExitCode, &r.LogPath); err != nil {
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
		out = append(out, &r)
	}
	return out, rows.Err()
}

func (s *Store) GetScheduleRun(runID int64) (*ScheduleRun, error) {
	row := s.db.QueryRow(`
		SELECT id, schedule_id, status, trigger, triggered_by_user_id, started_at,
		       finished_at, COALESCE(exit_code, 0), log_path
		FROM schedule_runs WHERE id = ?`, runID)
	var r ScheduleRun
	var uid sql.NullInt64
	var fin sql.NullTime
	err := row.Scan(&r.ID, &r.ScheduleID, &r.Status, &r.Trigger, &uid,
		&r.StartedAt, &fin, &r.ExitCode, &r.LogPath)
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
		       finished_at, COALESCE(exit_code, 0), log_path
		FROM schedule_runs WHERE schedule_id = ? AND status = 'succeeded'
		ORDER BY started_at DESC LIMIT 1`, scheduleID)
	var r ScheduleRun
	var uid sql.NullInt64
	var fin sql.NullTime
	err := row.Scan(&r.ID, &r.ScheduleID, &r.Status, &r.Trigger, &uid,
		&r.StartedAt, &fin, &r.ExitCode, &r.LogPath)
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
	return &r, nil
}

// --- app_shared_data ---

type SharedDataMount struct {
	ID          int64
	AppID       int64
	SourceAppID int64
	SourceSlug  string // joined from apps.slug at query time
	CreatedAt   time.Time
}

func (s *Store) GrantSharedData(consumerAppID, sourceAppID int64) error {
	if consumerAppID == sourceAppID {
		return fmt.Errorf("cannot mount data from self")
	}
	_, err := s.db.Exec(`
		INSERT INTO app_shared_data (app_id, source_app_id) VALUES (?, ?)`,
		consumerAppID, sourceAppID,
	)
	if err != nil {
		return fmt.Errorf("grant shared data: %w", err)
	}
	return nil
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

// --- helpers ---

type rowScanner interface {
	Scan(dest ...any) error
}

func scanSchedule(s rowScanner) (*Schedule, error) {
	var sched Schedule
	var enabled int
	err := s.Scan(&sched.ID, &sched.AppID, &sched.Name, &sched.CronExpr, &sched.CommandJSON,
		&enabled, &sched.TimeoutSeconds, &sched.OverlapPolicy, &sched.MissedPolicy,
		&sched.CreatedAt, &sched.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	sched.Enabled = enabled != 0
	return &sched, nil
}

