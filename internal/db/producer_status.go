package db

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"
)

// Deployment barriers and failed writes invalidate existing consumers. A healthy
// running publisher only fences new consumer boots, which must wait to load a
// complete publication.
const appCompatibilityFailuresSQL = `EXISTS (
 SELECT 1 FROM deployments failed WHERE failed.app_id = apps.id AND ` + deploymentRepairConditionSQL + `
) OR EXISTS (
 SELECT 1 FROM schedule_data_uncertainty u JOIN app_schedules sc ON sc.id = u.schedule_id
 WHERE sc.app_id = apps.id
)`

// All durable compatibility blockers must clear before consumers may return.
const appCompatibilityBlockersSQL = appCompatibilityFailuresSQL + ` OR EXISTS (
 SELECT 1 FROM schedule_runs r JOIN app_schedules sc ON sc.id = r.schedule_id
 WHERE sc.app_id = apps.id AND r.publishes_data = 1 AND r.status = 'running'
)`

type producerStatusChange struct {
	slug, from, to, reason string
	runID                  int64
}

func (c *producerStatusChange) log() {
	if c != nil {
		slog.Info("app_status_changed", "slug", c.slug, "from", c.from, "to", c.to, "reason", c.reason, "schedule_run_id", c.runID)
	}
}

// reconcileProducerStatusTx keeps the repair marker and transition audit atomic
// with completion. Only states displaced by a producer are restored; unrelated
// failures and explicit lifecycle operations must not be revived.
func reconcileProducerStatusTx(ctx context.Context, tx writeTx, appID, runID int64, failureReason string) (*producerStatusChange, error) {
	// Lock the app row on both databases, including against operator mutations.
	if _, err := tx.ExecContext(ctx, `UPDATE apps SET producer_recovery_status = producer_recovery_status WHERE id = ?`, appID); err != nil {
		return nil, err
	}
	var slug, status, previous string
	var blocked int
	if err := tx.QueryRowContext(ctx, `SELECT slug, status, producer_recovery_status,
 CASE WHEN (`+appCompatibilityBlockersSQL+`) THEN 1 ELSE 0 END FROM apps WHERE id = ?`, appID).Scan(&slug, &status, &previous, &blocked); err != nil {
		return nil, err
	}
	if status == "deleting" {
		return nil, nil
	}
	target, reason := status, failureReason
	if failureReason != "" && blocked != 0 {
		switch status {
		case "running", "hibernated", "waking", "starting", "degraded":
			previous = status
			if status == "waking" || status == "starting" {
				previous = "running"
			}
			target = "failed"
		default:
			// Startup retains the original fail-closed behavior for other
			// states without inventing lifecycle intent for later recovery.
			if runID == 0 && status != "stopped" && status != "failed" {
				previous = ""
				target = "failed"
			}
		}
	} else if blocked == 0 && status == "failed" && previous != "" {
		target, previous, reason = previous, "", "producer_repair_succeeded"
	}
	// The failure callback stops these consumers. Leave their placement and
	// PID identity intact until a confirmed stop, but remove stale serving
	// status durably so the watcher can replace desired-running slots on repair.
	if target == "failed" && blocked != 0 {
		if _, err := tx.ExecContext(ctx, `UPDATE replicas SET status = 'stopped', updated_at = ?
   WHERE app_id = ? AND status IN ('running', 'draining', 'starting', 'booting', 'resuming', 'suspended')`, time.Now().UTC().Unix(), appID); err != nil {
			return nil, err
		}
	}
	diagnostic := failureReason
	if target != "failed" {
		diagnostic = ""
	} else if previous != "" && blocked != 0 {
		// A later failure or partial repair can change the outstanding blocker
		// without changing app status. Derive the diagnostic from durable state,
		// including uncertainty whose run history has already been pruned.
		var err error
		diagnostic, err = producerQuarantineDiagnosticTx(ctx, tx, appID)
		if err != nil {
			return nil, err
		}
	}
	if target == status {
		if target == "failed" && previous != "" && blocked != 0 {
			_, err := tx.ExecContext(ctx, `UPDATE apps SET last_error = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ? AND last_error <> ?`, diagnostic, appID, diagnostic)
			return nil, err
		}
		return nil, nil
	}
	if _, err := tx.ExecContext(ctx, `UPDATE apps SET status = ?, operator_stopped = 0, producer_recovery_status = ?, last_error = ?, crashed_at = 0, updated_at = CURRENT_TIMESTAMP WHERE id = ?`, target, previous, diagnostic, appID); err != nil {
		return nil, err
	}
	detail, err := json.Marshal(map[string]any{"from": status, "to": target, "reason": reason, "schedule_run_id": runID})
	if err != nil {
		return nil, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO audit_events (action, resource_type, resource_id, detail, ip_address) VALUES ('app_status_changed', 'app', ?, ?, '')`, slug, string(detail)); err != nil {
		return nil, fmt.Errorf("audit producer status: %w", err)
	}
	return &producerStatusChange{slug: slug, from: status, to: target, reason: reason, runID: runID}, nil
}

func producerQuarantineDiagnosticTx(ctx context.Context, tx writeTx, appID int64) (string, error) {
	var runID int64
	var name, status string
	err := tx.QueryRowContext(ctx, `SELECT u.schedule_run_id, sc.name, u.status
 FROM schedule_data_uncertainty u JOIN app_schedules sc ON sc.id = u.schedule_id
 WHERE sc.app_id = ? ORDER BY u.data_write_sequence DESC, u.schedule_id DESC LIMIT 1`, appID).Scan(&runID, &name, &status)
	if err == nil {
		return fmt.Sprintf("producer run %d (%s) ended %s after attempting a data write; rerun the producer successfully to repair compatibility", runID, name, status), nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return "", fmt.Errorf("read producer uncertainty diagnostic: %w", err)
	}
	err = tx.QueryRowContext(ctx, `SELECT r.id, sc.name FROM schedule_runs r
 JOIN app_schedules sc ON sc.id = r.schedule_id WHERE sc.app_id = ?
 AND r.publishes_data = 1 AND r.status = 'running' ORDER BY r.id DESC LIMIT 1`, appID).Scan(&runID, &name)
	if err == nil {
		return fmt.Sprintf("producer run %d (%s) is still running; compatibility cannot be confirmed while this producer is active", runID, name), nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return "", fmt.Errorf("read running producer diagnostic: %w", err)
	}
	return "compatibility remains quarantined by a data-affecting deployment; complete a corrective deployment to repair compatibility", nil
}

// ListProducerQuarantinedApps includes failed and operator-stopped apps in cleanup: their
// consumers may have survived the hub before the failure callback could stop them.
// Recovery intent is not a quarantine predicate: deployment failures and older
// producer failures may have no marker while retaining a durable data fence.
func (s *Store) ListProducerQuarantinedApps() ([]*App, error) {
	rows, err := s.db.Query(`SELECT ` + appColumns + deploymentSummarySQL + ` FROM apps
 WHERE status IN ('failed', 'stopped') AND (producer_recovery_status <> '' OR (` + appCompatibilityBlockersSQL + `))`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var apps []*App
	for rows.Next() {
		app, err := scanApp(rows)
		if err != nil {
			return nil, err
		}
		apps = append(apps, app)
	}
	return apps, rows.Err()
}
