package db

import (
	"context"
	"database/sql"
	"encoding/hex"
	"errors"
	"strings"
)

func validElasticLaunchBinding(nodeID, digest string) bool {
	if strings.TrimSpace(nodeID) != nodeID || nodeID == "" || len(nodeID) > 128 || len(digest) != 64 || strings.ToLower(digest) != digest {
		return false
	}
	_, err := hex.DecodeString(digest)
	return err == nil
}

// BindElasticLaunch durably pins a reservation to one worker and one immutable
// request before any remote launch. Retries cannot move a launch whose outcome
// is unknown to another worker. Binding and launch intent commit atomically.
func (s *Store) BindElasticLaunch(ctx context.Context, owner ElasticOwner, id, nodeID, requestDigest string) error {
	if !validElasticLaunchBinding(nodeID, requestDigest) {
		return ErrElasticConflict
	}
	tx, err := s.beginElasticWrite(ctx, owner)
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck
	r, err := scanElasticReservation(tx.QueryRowContext(ctx, `SELECT `+elasticReservationColumns+` FROM elastic_reservations WHERE id = ?`, id))
	if err != nil {
		return err
	}
	if r.Owner != owner {
		return ErrElasticFenced
	}
	if err := validateElasticDesiredLaunch(ctx, tx, r); err != nil {
		return err
	}
	if r.State != "reserved" && r.State != "launching" && r.State != "ready" {
		return ErrElasticConflict
	}
	var boundNode, boundDigest string
	err = tx.QueryRowContext(ctx, `SELECT node_id, request_digest FROM elastic_launch_bindings WHERE reservation_id = ?`, id).Scan(&boundNode, &boundDigest)
	if err == nil {
		if boundNode != nodeID || boundDigest != requestDigest {
			return ErrElasticConflict
		}
	} else if errors.Is(err, sql.ErrNoRows) {
		// A previously unbound launch may already have executed through a legacy
		// caller. Only a pristine reservation can establish a new identity.
		if r.State != "reserved" {
			return ErrElasticConflict
		}
		if _, err = tx.ExecContext(ctx, `INSERT INTO elastic_launch_bindings (reservation_id, node_id, request_digest) VALUES (?, ?, ?)`, id, nodeID, requestDigest); err != nil {
			return err
		}
	} else {
		return err
	}
	if r.State == "reserved" {
		if _, err := tx.ExecContext(ctx, `UPDATE elastic_reservations SET state = 'launching' WHERE id = ?`, id); err != nil {
			return err
		}
	}
	return s.commitElasticWrite(ctx, tx, owner)
}

// AuthorizeElasticCommand checks the live controller lease and the immutable
// launch binding together. It grants no capacity and never confirms an exit.
// Workers must still serialize commands and retain durable stop tombstones;
// authorization is not proof that a remote operation executed successfully.
func (s *Store) AuthorizeElasticCommand(ctx context.Context, owner ElasticOwner, id, nodeID, requestDigest, action string) error {
	if !validElasticLaunchBinding(nodeID, requestDigest) || (action != "prepare" && action != "ack" && action != "stop") {
		return ErrElasticConflict
	}
	tx, err := s.beginElasticWrite(ctx, owner)
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck
	r, err := scanElasticReservation(tx.QueryRowContext(ctx, `SELECT `+elasticReservationColumns+` FROM elastic_reservations WHERE id = ?`, id))
	if err != nil {
		return err
	}
	if r.Owner != owner {
		return ErrElasticFenced
	}
	if action == "stop" {
		if r.State != "stopping" && r.State != "stopped" {
			return ErrElasticConflict
		}
	} else if r.State != "launching" && r.State != "ready" {
		return ErrElasticConflict
	}
	if action != "stop" {
		if err := validateElasticDesiredLaunch(ctx, tx, r); err != nil {
			return err
		}
	}
	var boundNode, boundDigest string
	if err := tx.QueryRowContext(ctx, `SELECT node_id, request_digest FROM elastic_launch_bindings WHERE reservation_id = ?`, id).Scan(&boundNode, &boundDigest); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrElasticConflict
		}
		return err
	}
	if boundNode != nodeID || boundDigest != requestDigest {
		return ErrElasticConflict
	}
	return s.commitElasticWrite(ctx, tx, owner)
}

func validateElasticDesiredLaunch(ctx context.Context, tx writeTx, r *ElasticReservation) error {
	if _, err := tx.ExecContext(ctx, `UPDATE apps SET id = id WHERE id = ?`, r.AppID); err != nil {
		return err
	}
	var allowed bool
	err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM apps WHERE id = ? AND active_deployment_id = ? AND status IN ('running', 'degraded') AND worker_isolation = 'per_session' AND worker_max_workers > 0)`, r.AppID, r.DeploymentID).Scan(&allowed)
	if err != nil {
		return err
	}
	if !allowed {
		return ErrElasticConflict
	}
	return nil
}

func (s *Store) GetElasticReservation(ctx context.Context, id string) (*ElasticReservation, error) {
	return scanElasticReservation(s.db.QueryRowContext(ctx, `SELECT `+elasticReservationColumns+` FROM elastic_reservations WHERE id = ?`, id))
}
