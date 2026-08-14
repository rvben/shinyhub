package db

import (
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// AppLogRun is one immutable execution of a replica slot. Mutable fields are
// limited to lifecycle facts learned after launch (provider, terminal status,
// finish time, and OOM verdict); identity and release metadata never change.
type AppLogRun struct {
	RunID        string     `json:"run_id"`
	AppID        int64      `json:"app_id"`
	ReplicaIndex int        `json:"replica"`
	DeploymentID *int64     `json:"deployment_id,omitempty"`
	AppVersion   string     `json:"app_version"`
	Tier         string     `json:"tier"`
	Provider     string     `json:"provider"`
	Status       string     `json:"status"`
	StartedAt    time.Time  `json:"started_at"`
	FinishedAt   *time.Time `json:"finished_at,omitempty"`
	OOMKilled    bool       `json:"oom_killed"`
}

type CreateAppLogRunParams struct {
	RunID        string
	AppID        int64
	ReplicaIndex int
	DeploymentID *int64
	AppVersion   string
	Tier         string
	Status       string
	StartedAt    time.Time
}

// CreateAppLogRun records a run before the runtime is launched. This ordering
// makes a start attempt discoverable even when the runtime fails immediately.
func (s *Store) CreateAppLogRun(p CreateAppLogRunParams) error {
	defer s.timed("CreateAppLogRun")()
	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("create app log run begin: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck
	// A hard control-plane stop can prevent the exit callback from closing the
	// prior record. A new start is definitive evidence that the old execution no
	// longer owns this slot, so close it atomically before inserting its successor.
	if _, err := tx.Exec(`
		UPDATE app_log_runs
		SET status = 'interrupted', finished_at = ?
		WHERE app_id = ? AND replica_index = ? AND finished_at IS NULL`,
		p.StartedAt.UnixMilli(), p.AppID, p.ReplicaIndex); err != nil {
		return fmt.Errorf("supersede app log run: %w", err)
	}
	_, err = tx.Exec(`
		INSERT INTO app_log_runs
			(run_id, app_id, replica_index, deployment_id, app_version, tier,
			 provider, status, started_at, finished_at, oom_killed)
		VALUES (?, ?, ?, ?, ?, ?, '', ?, ?, NULL, 0)`,
		p.RunID, p.AppID, p.ReplicaIndex, p.DeploymentID, p.AppVersion, p.Tier,
		p.Status, p.StartedAt.UnixMilli(),
	)
	if err != nil {
		return fmt.Errorf("create app log run: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("create app log run commit: %w", err)
	}
	return nil
}

// MarkAppLogRunRunning attaches the runtime provider after launch succeeds.
func (s *Store) MarkAppLogRunRunning(runID, provider string) error {
	defer s.timed("MarkAppLogRunRunning")()
	res, err := s.db.Exec(`
		UPDATE app_log_runs SET provider = ?, status = 'running'
		WHERE run_id = ?`, provider, runID)
	if err != nil {
		return fmt.Errorf("mark app log run running: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// FinishAppLogRun closes a run with its terminal status. Calls are idempotent
// so shutdown/recovery reconciliation can safely repeat them.
func (s *Store) FinishAppLogRun(runID, status string, finishedAt time.Time, oomKilled bool) error {
	defer s.timed("FinishAppLogRun")()
	res, err := s.db.Exec(`
		UPDATE app_log_runs
		SET status = ?, finished_at = ?, oom_killed = ?
		WHERE run_id = ?`, status, finishedAt.UnixMilli(), boolToInt(oomKilled), runID)
	if err != nil {
		return fmt.Errorf("finish app log run: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// ListAppLogRuns returns newest-first execution history, bounded for UI/API use.
func (s *Store) ListAppLogRuns(appID int64, limit int) ([]*AppLogRun, error) {
	defer s.timed("ListAppLogRuns")()
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	rows, err := s.db.Query(`
		SELECT run_id, app_id, replica_index, deployment_id, app_version, tier,
		       provider, status, started_at, finished_at, oom_killed
		FROM app_log_runs
		WHERE app_id = ?
		ORDER BY started_at DESC, run_id DESC
		LIMIT ?`, appID, limit)
	if err != nil {
		return nil, fmt.Errorf("list app log runs: %w", err)
	}
	defer rows.Close()
	out := []*AppLogRun{}
	for rows.Next() {
		run, err := scanAppLogRun(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, run)
	}
	return out, rows.Err()
}

// GetAppLogRun scopes lookup to the parent app so a run ID can never be used
// to read another app's file through the logs endpoint.
func (s *Store) GetAppLogRun(appID int64, runID string) (*AppLogRun, error) {
	defer s.timed("GetAppLogRun")()
	row := s.db.QueryRow(`
		SELECT run_id, app_id, replica_index, deployment_id, app_version, tier,
		       provider, status, started_at, finished_at, oom_killed
		FROM app_log_runs WHERE app_id = ? AND run_id = ?`, appID, runID)
	run, err := scanAppLogRun(row)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("get app log run: %w", err)
	}
	return run, nil
}

type logRunScanner interface{ Scan(...any) error }

func scanAppLogRun(row logRunScanner) (*AppLogRun, error) {
	var run AppLogRun
	var started int64
	var finished *int64
	var oom int
	if err := row.Scan(&run.RunID, &run.AppID, &run.ReplicaIndex,
		&run.DeploymentID, &run.AppVersion, &run.Tier, &run.Provider,
		&run.Status, &started, &finished, &oom); err != nil {
		return nil, err
	}
	run.StartedAt = time.UnixMilli(started)
	if finished != nil {
		t := time.UnixMilli(*finished)
		run.FinishedAt = &t
	}
	run.OOMKilled = oom != 0
	return &run, nil
}
