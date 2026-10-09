package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

type ServingWorkerIsolationReader interface {
	GetServingDeployment(int64) (*Deployment, error)
	GetDeploymentWorkerIsolation(int64) (string, error)
}

// ResolveServingWorkerIsolation prefers the durable serving generation policy.
// Desired app settings may already describe a replacement that has not started.
func ResolveServingWorkerIsolation(reader ServingWorkerIsolationReader, app *App, defaultIsolation string) (string, error) {
	deployment, err := reader.GetServingDeployment(app.ID)
	if err != nil && !errors.Is(err, ErrNotFound) {
		return "", err
	}
	isolation := ""
	if err == nil && deployment != nil && deployment.Status == DeploymentSucceeded {
		isolation, err = reader.GetDeploymentWorkerIsolation(deployment.ID)
		if err != nil {
			return "", err
		}
	}
	if isolation == "" {
		isolation = app.WorkerIsolation
	}
	if isolation == "" {
		isolation = defaultIsolation
	}
	if isolation == "" {
		isolation = "multiplex"
	}
	if !validDeploymentWorkerIsolation(isolation) {
		return "", fmt.Errorf("invalid serving worker isolation %q", isolation)
	}
	return isolation, nil
}

func (s *Store) ServingWorkerIsolation(app *App, defaultIsolation string) (string, error) {
	return ResolveServingWorkerIsolation(s, app, defaultIsolation)
}

func validDeploymentWorkerIsolation(isolation string) bool {
	return isolation == "multiplex" || isolation == "grouped" || isolation == "per_session"
}

func (s *Store) GetDeploymentWorkerIsolation(id int64) (string, error) {
	var isolation string
	err := s.db.QueryRow("SELECT worker_isolation FROM deployments WHERE id=?", id).Scan(&isolation)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNotFound
	}
	return isolation, err
}

// RecordDeploymentWorkerIsolation initializes a legacy snapshot or confirms it.
// A live generation's policy can never silently change with desired settings.
func (s *Store) RecordDeploymentWorkerIsolation(appID, id int64, isolation string) error {
	return s.writeDeploymentWorkerIsolation(appID, id, isolation, false)
}

// ReplaceStoppedDeploymentWorkerIsolation requires the caller to confirm stop.
// Durable identities must be removed before a structural restart can proceed.
func (s *Store) ReplaceStoppedDeploymentWorkerIsolation(appID, id int64, isolation string) error {
	return s.writeDeploymentWorkerIsolation(appID, id, isolation, true)
}

func (s *Store) writeDeploymentWorkerIsolation(appID, id int64, isolation string, replace bool) error {
	if !validDeploymentWorkerIsolation(isolation) {
		return fmt.Errorf("invalid deployment worker isolation %q", isolation)
	}
	tx, err := s.d.beginWrite(context.Background(), s.rawDB(), appID)
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck
	var owner int64
	var previous string
	if err := tx.QueryRow("SELECT app_id,worker_isolation FROM deployments WHERE id=?", id).Scan(&owner, &previous); err != nil {
		return err
	}
	if owner != appID {
		return fmt.Errorf("deployment %d belongs to another app", id)
	}
	if replace {
		var rows int
		if err := tx.QueryRow("SELECT COUNT(*) FROM deployment_replicas WHERE deployment_id=?", id).Scan(&rows); err != nil {
			return err
		}
		if rows != 0 {
			return fmt.Errorf("deployment %d still has recorded workers", id)
		}
		if err := tx.QueryRow("SELECT COUNT(*) FROM replicas WHERE app_id=? AND pid IS NOT NULL", appID).Scan(&rows); err != nil {
			return err
		}
		if rows != 0 {
			return fmt.Errorf("app %d still has recorded fixed processes", appID)
		}
		if isolation == "grouped" || isolation == "per_session" {
			if _, err := tx.Exec("DELETE FROM replicas WHERE app_id=? AND pid IS NULL", appID); err != nil {
				return err
			}
		}
	} else if previous != "" && previous != isolation {
		return fmt.Errorf("deployment %d isolation is %s, not %s", id, previous, isolation)
	}
	if _, err := tx.Exec("UPDATE deployments SET worker_isolation=? WHERE id=?", isolation, id); err != nil {
		return err
	}
	return tx.Commit()
}
