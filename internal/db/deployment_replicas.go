package db

import (
	"database/sql"
	"fmt"
	"time"
)

// DeploymentReplica is one runtime identity scoped to an exact deployment
// generation. It exists alongside the legacy active-replica projection while
// a candidate or previous generation is staged or draining.
type DeploymentReplica struct {
	AppID        int64
	DeploymentID int64
	Index        int
	PID          *int
	Port         *int
	Status       string
	Provider     string
	Tier         string
	EndpointURL  string
	WorkerID     string
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

type UpsertDeploymentReplicaParams struct {
	AppID        int64
	DeploymentID int64
	Index        int
	PID          *int
	Port         *int
	Status       string
	Provider     string
	Tier         string
	EndpointURL  string
	WorkerID     string
}

func (s *Store) UpsertDeploymentReplica(p UpsertDeploymentReplicaParams) error {
	if p.AppID <= 0 || p.DeploymentID <= 0 || p.Index < 0 {
		return fmt.Errorf("upsert deployment replica: invalid identity")
	}
	var deploymentAppID int64
	if err := s.db.QueryRow(`SELECT app_id FROM deployments WHERE id = ?`, p.DeploymentID).Scan(&deploymentAppID); err != nil {
		return fmt.Errorf("upsert deployment replica: load deployment owner: %w", err)
	}
	if deploymentAppID != p.AppID {
		return fmt.Errorf("upsert deployment replica: deployment %d belongs to app %d, not app %d", p.DeploymentID, deploymentAppID, p.AppID)
	}
	_, err := s.db.Exec(`
		INSERT INTO deployment_replicas
			(app_id, deployment_id, idx, pid, port, status, provider, tier, endpoint_url, worker_id)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (deployment_id, idx) DO UPDATE SET
			pid = excluded.pid,
			port = excluded.port,
			status = excluded.status,
			provider = excluded.provider,
			tier = excluded.tier,
			endpoint_url = excluded.endpoint_url,
			worker_id = excluded.worker_id,
			updated_at = CURRENT_TIMESTAMP`,
		p.AppID, p.DeploymentID, p.Index, p.PID, p.Port, p.Status,
		p.Provider, p.Tier, p.EndpointURL, p.WorkerID)
	if err != nil {
		return fmt.Errorf("upsert deployment replica: %w", err)
	}
	return nil
}

func (s *Store) ListDeploymentReplicas(appID int64) ([]*DeploymentReplica, error) {
	rows, err := s.db.Query(`
		SELECT app_id, deployment_id, idx, pid, port, status, provider, tier,
		       endpoint_url, worker_id, created_at, updated_at
		FROM deployment_replicas
		WHERE app_id = ?
		ORDER BY deployment_id, idx`, appID)
	if err != nil {
		return nil, fmt.Errorf("list deployment replicas: %w", err)
	}
	defer rows.Close()
	var out []*DeploymentReplica
	for rows.Next() {
		var (
			r    DeploymentReplica
			pid  sql.NullInt64
			port sql.NullInt64
		)
		if err := rows.Scan(&r.AppID, &r.DeploymentID, &r.Index, &pid, &port,
			&r.Status, &r.Provider, &r.Tier, &r.EndpointURL, &r.WorkerID,
			&r.CreatedAt, &r.UpdatedAt); err != nil {
			return nil, fmt.Errorf("list deployment replicas: %w", err)
		}
		if pid.Valid {
			v := int(pid.Int64)
			r.PID = &v
		}
		if port.Valid {
			v := int(port.Int64)
			r.Port = &v
		}
		out = append(out, &r)
	}
	return out, rows.Err()
}

func (s *Store) DeleteDeploymentReplicas(deploymentID int64) error {
	if _, err := s.db.Exec(`DELETE FROM deployment_replicas WHERE deployment_id = ?`, deploymentID); err != nil {
		return fmt.Errorf("delete deployment replicas: %w", err)
	}
	return nil
}

// RepairDeploymentGenerationLedger closes the mixed-version startup window.
// An older owner admitted before the startup mutation fence may finish after
// migrations 073/074 ran, leaving a succeeded row with the legacy empty token
// and without advancing apps.active_deployment_id. The new owner calls this
// only after predecessor handlers have drained behind the exclusive fence.
func (s *Store) RepairDeploymentGenerationLedger() error {
	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("repair deployment generation ledger: begin: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck
	rows, err := tx.Query(`SELECT id FROM deployments WHERE activation_token = '' ORDER BY id`)
	if err != nil {
		return fmt.Errorf("repair deployment generation ledger: list empty tokens: %w", err)
	}
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return fmt.Errorf("repair deployment generation ledger: scan deployment: %w", err)
		}
		ids = append(ids, id)
	}
	if err := rows.Close(); err != nil {
		return fmt.Errorf("repair deployment generation ledger: close deployment rows: %w", err)
	}
	for _, id := range ids {
		token, err := newActivationToken()
		if err != nil {
			return err
		}
		if _, err := tx.Exec(`UPDATE deployments SET activation_token = ? WHERE id = ? AND activation_token = ''`, token, id); err != nil {
			return fmt.Errorf("repair deployment generation ledger: token for deployment %d: %w", id, err)
		}
	}
	if _, err := tx.Exec(`
		UPDATE apps
		SET active_deployment_id = (
			SELECT d.id FROM deployments d
			WHERE d.app_id = apps.id AND d.status = ?
			ORDER BY d.id DESC LIMIT 1
		), updated_at = CURRENT_TIMESTAMP`, DeploymentSucceeded); err != nil {
		return fmt.Errorf("repair deployment generation ledger: recompute active pointers: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("repair deployment generation ledger: commit: %w", err)
	}
	return nil
}
