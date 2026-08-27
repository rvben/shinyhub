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
	ExternalLogs string     `json:"-"`
	Status       string     `json:"status"`
	StartedAt    time.Time  `json:"started_at"`
	FinishedAt   *time.Time `json:"finished_at,omitempty"`
	OOMKilled    bool       `json:"oom_killed"`
	ExitCode     *int       `json:"exit_code,omitempty"`
	Signal       string     `json:"exit_signal,omitempty"`
	ExitReason   string     `json:"exit_reason,omitempty"`
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

// MarkAppLogRunRunning attaches the runtime provider and any provider-owned log
// handoff after launch succeeds. externalLogs is an opaque JSON envelope owned
// by the process layer; keeping it on the immutable run preserves access after
// the replica exits or its pool slot is reused.
func (s *Store) MarkAppLogRunRunning(runID, provider, externalLogs string) error {
	defer s.timed("MarkAppLogRunRunning")()
	res, err := s.db.Exec(`
		UPDATE app_log_runs SET provider = ?, external_logs = ?, status = 'running'
		WHERE run_id = ?`, provider, externalLogs, runID)
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
	return s.FinishAppLogRunWithExit(runID, status, finishedAt, oomKilled, nil, "", "")
}

// FinishAppLogRunWithExit closes a run and retains the complete terminal
// verdict. The older FinishAppLogRun entry point remains for intentional-stop
// callers that have no unexpected-exit diagnostic.
func (s *Store) FinishAppLogRunWithExit(runID, status string, finishedAt time.Time, oomKilled bool, exitCode *int, signal, reason string) error {
	defer s.timed("FinishAppLogRun")()
	res, err := s.db.Exec(`
		UPDATE app_log_runs
		SET status = ?, finished_at = ?, oom_killed = ?, exit_code = ?,
		    exit_signal = ?, exit_reason = ?
		WHERE run_id = ?`, status, finishedAt.UnixMilli(), boolToInt(oomKilled),
		exitCode, signal, reason, runID)
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
		       provider, external_logs, status, started_at, finished_at, oom_killed,
		       exit_code, exit_signal, exit_reason
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

// ListUnfinishedAppLogRuns returns every execution that still belongs to a
// live process. Recovery uses this to reconnect adopted processes to their
// immutable run records, so a later stop or crash can close the right row.
func (s *Store) ListUnfinishedAppLogRuns(appID int64) ([]*AppLogRun, error) {
	defer s.timed("ListUnfinishedAppLogRuns")()
	rows, err := s.db.Query(`
		SELECT run_id, app_id, replica_index, deployment_id, app_version, tier,
		       provider, external_logs, status, started_at, finished_at, oom_killed,
		       exit_code, exit_signal, exit_reason
		FROM app_log_runs
		WHERE app_id = ? AND finished_at IS NULL
		ORDER BY started_at DESC, run_id DESC`, appID)
	if err != nil {
		return nil, fmt.Errorf("list unfinished app log runs: %w", err)
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
		       provider, external_logs, status, started_at, finished_at, oom_killed,
		       exit_code, exit_signal, exit_reason
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

// PruneAppLogRuns keeps the newest keep runs independently for every app and
// replica slot. Running rows are never removed, even if unusual timestamps put
// one beyond the retention rank. app_log_chunks are deleted by the FK cascade.
func (s *Store) PruneAppLogRuns(keep int) (int64, error) {
	defer s.timed("PruneAppLogRuns")()
	if keep <= 0 {
		return 0, nil
	}
	res, err := s.db.Exec(`
		WITH ranked AS (
			SELECT run_id, finished_at,
			       ROW_NUMBER() OVER (
			           PARTITION BY app_id, replica_index
			           ORDER BY started_at DESC, run_id DESC
			       ) AS retention_rank
			FROM app_log_runs
		)
		DELETE FROM app_log_runs
		WHERE run_id IN (
			SELECT run_id FROM ranked
			WHERE retention_rank > ? AND finished_at IS NOT NULL
		)`, keep)
	if err != nil {
		return 0, fmt.Errorf("prune app log runs: %w", err)
	}
	return res.RowsAffected()
}

// ListAppLogRunIDs returns the complete retained identity set for reconciling
// node-local immutable files after database pruning.
func (s *Store) ListAppLogRunIDs() (map[string]struct{}, error) {
	defer s.timed("ListAppLogRunIDs")()
	rows, err := s.db.Query(`SELECT run_id FROM app_log_runs`)
	if err != nil {
		return nil, fmt.Errorf("list app log run IDs: %w", err)
	}
	defer rows.Close()
	out := make(map[string]struct{})
	for rows.Next() {
		var runID string
		if err := rows.Scan(&runID); err != nil {
			return nil, fmt.Errorf("scan app log run ID: %w", err)
		}
		out[runID] = struct{}{}
	}
	return out, rows.Err()
}

type logRunScanner interface{ Scan(...any) error }

func scanAppLogRun(row logRunScanner) (*AppLogRun, error) {
	var run AppLogRun
	var started int64
	var finished *int64
	var oom int
	if err := row.Scan(&run.RunID, &run.AppID, &run.ReplicaIndex,
		&run.DeploymentID, &run.AppVersion, &run.Tier, &run.Provider,
		&run.ExternalLogs, &run.Status, &started, &finished, &oom,
		&run.ExitCode, &run.Signal, &run.ExitReason); err != nil {
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
