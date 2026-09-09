package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/google/uuid"
)

var (
	ErrElasticCapacity = errors.New("elastic worker capacity exhausted")
	ErrElasticFenced   = errors.New("elastic reservation owner is stale or expired")
	ErrElasticConflict = errors.New("elastic reservation requires reconciliation")
)

// ElasticOwner identifies a control-plane lease, not an authenticated user.
type ElasticOwner struct {
	Instance string
	Epoch    int64
}

// ElasticReservation is an internal admission record for a single browser
// client. ClientKey must be an app/principal-bound opaque digest, never a raw
// session cookie. IDs are worker incarnations; slots never repeat within an app.
// Expiry of a controller lease does NOT free this capacity.
type ElasticReservation struct {
	ID                  string
	AppID, DeploymentID int64
	ClientKey           string
	Slot                int
	Owner               ElasticOwner
	State               string
}

const elasticReservationColumns = "id, app_id, deployment_id, client_key, slot, owner_instance, owner_epoch, state"

func scanElasticReservation(row interface{ Scan(...any) error }) (*ElasticReservation, error) {
	var r ElasticReservation
	err := row.Scan(&r.ID, &r.AppID, &r.DeploymentID, &r.ClientKey, &r.Slot, &r.Owner.Instance, &r.Owner.Epoch, &r.State)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return &r, err
}

// beginElasticWrite locks the lease row as well as checking it. Takeover cannot
// interleave with admission or a state transition. PostgreSQL's transaction
// timestamp can precede a lock wait, so use its live database clock here.
func (s *Store) beginElasticWrite(ctx context.Context, owner ElasticOwner) (writeTx, error) {
	if owner.Instance == "" || owner.Epoch <= 0 {
		return nil, ErrElasticFenced
	}
	tx, err := s.d.beginWrite(ctx, s.rawDB(), 0)
	if err != nil {
		return nil, err
	}
	if err := s.checkElasticOwner(ctx, tx, owner); err != nil {
		_ = tx.Rollback()
		return nil, err
	}
	return tx, nil
}

func (s *Store) checkElasticOwner(ctx context.Context, tx writeTx, owner ElasticOwner) error {
	now := s.d.now()
	if s.IsPostgres() {
		now = "clock_timestamp()"
	}
	res, err := tx.ExecContext(ctx, `UPDATE cp_owner SET epoch = epoch WHERE role = ? AND instance_id = ? AND epoch = ? AND expires_at > `+now, ownerRole, owner.Instance, owner.Epoch)
	if err == nil {
		var n int64
		n, err = res.RowsAffected()
		if err == nil && n != 1 {
			err = ErrElasticFenced
		}
	}
	return err
}

// Recheck after app-row waits and immediately before committing the grant.
func (s *Store) commitElasticWrite(ctx context.Context, tx writeTx, owner ElasticOwner) error {
	if err := s.checkElasticOwner(ctx, tx, owner); err != nil {
		return err
	}
	return tx.Commit()
}

// ReserveElasticSession is the storage primitive for the future clustered
// per-session coordinator. It is deliberately not wired into public routing
// until remote launch acknowledgement and worker fencing are implemented.
// The database app policy, rather than a caller-supplied limit, controls capacity.
func (s *Store) ReserveElasticSession(ctx context.Context, owner ElasticOwner, appID, deploymentID int64, clientKey string) (*ElasticReservation, error) {
	if appID <= 0 || deploymentID <= 0 || len(clientKey) == 0 || len(clientKey) > 128 {
		return nil, fmt.Errorf("invalid elastic reservation identity")
	}
	tx, err := s.beginElasticWrite(ctx, owner)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback() //nolint:errcheck
	// Coordinate with ordinary app mutations as well as other admissions.
	if _, err := tx.ExecContext(ctx, `UPDATE apps SET id = id WHERE id = ?`, appID); err != nil {
		return nil, err
	}
	var mode, status string
	var maxWorkers int
	var active sql.NullInt64
	if err := tx.QueryRowContext(ctx, `SELECT worker_isolation, status, worker_max_workers, active_deployment_id FROM apps WHERE id = ?`, appID).Scan(&mode, &status, &maxWorkers, &active); err != nil {
		return nil, err
	}
	if mode != "per_session" || (status != "running" && status != "degraded") || maxWorkers < 1 || !active.Valid || active.Int64 != deploymentID {
		return nil, ErrElasticConflict
	}
	existing, err := scanElasticReservation(tx.QueryRowContext(ctx, `SELECT `+elasticReservationColumns+` FROM elastic_reservations WHERE app_id = ? AND client_key = ? AND state <> 'stopped'`, appID, clientKey))
	if err == nil {
		if existing.DeploymentID != deploymentID || existing.Owner != owner || existing.State == "stopping" {
			return nil, ErrElasticConflict
		}
		return existing, s.commitElasticWrite(ctx, tx, owner)
	}
	if !errors.Is(err, ErrNotFound) {
		return nil, err
	}
	var count, next int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM elastic_reservations WHERE app_id = ? AND state <> 'stopped'`, appID).Scan(&count); err != nil {
		return nil, err
	}
	if count >= maxWorkers {
		return nil, ErrElasticCapacity
	}
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(slot), -1) + 1 FROM elastic_reservations WHERE app_id = ?`, appID).Scan(&next); err != nil {
		return nil, err
	}
	r := &ElasticReservation{ID: uuid.NewString(), AppID: appID, DeploymentID: deploymentID, ClientKey: clientKey, Slot: next, Owner: owner, State: "reserved"}
	_, err = tx.ExecContext(ctx, `INSERT INTO elastic_reservations (`+elasticReservationColumns+`) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`, r.ID, r.AppID, r.DeploymentID, r.ClientKey, r.Slot, owner.Instance, owner.Epoch, r.State)
	if err != nil {
		return nil, err
	}
	return r, s.commitElasticWrite(ctx, tx, owner)
}

// AdvanceElasticReservation records launch progress under the original owner.
// A retry of an already-completed transition succeeds; backwards/skipped steps
// and updates from stale owners do not.
func (s *Store) AdvanceElasticReservation(ctx context.Context, owner ElasticOwner, id, state string) error {
	previous := map[string]string{"launching": "reserved", "ready": "launching"}[state]
	if previous == "" {
		return ErrElasticConflict
	}
	return s.transitionElasticReservation(ctx, owner, id, previous, state, false)
}

// StopElasticReservation claims reconciliation for the current owner, including
// after takeover. It retains capacity until ConfirmElasticReservationStopped.
func (s *Store) StopElasticReservation(ctx context.Context, owner ElasticOwner, id string) error {
	return s.transitionElasticReservation(ctx, owner, id, "", "stopping", true)
}

// ConfirmElasticReservationStopped may be called ONLY after the worker protocol
// proves this exact incarnation has stopped (or never launched). A timeout or
// unreachable worker is not confirmation. The future coordinator owns that proof.
func (s *Store) ConfirmElasticReservationStopped(ctx context.Context, owner ElasticOwner, id string) error {
	return s.transitionElasticReservation(ctx, owner, id, "stopping", "stopped", false)
}

func (s *Store) transitionElasticReservation(ctx context.Context, owner ElasticOwner, id, previous, next string, takeover bool) error {
	tx, err := s.beginElasticWrite(ctx, owner)
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck
	r, err := scanElasticReservation(tx.QueryRowContext(ctx, `SELECT `+elasticReservationColumns+` FROM elastic_reservations WHERE id = ?`, id))
	if err != nil {
		return err
	}
	if !takeover && r.Owner != owner {
		return ErrElasticFenced
	}
	if r.State == next && r.Owner == owner {
		return s.commitElasticWrite(ctx, tx, owner)
	}
	if takeover && next == "stopping" && r.State == "stopped" && r.Owner == owner {
		return s.commitElasticWrite(ctx, tx, owner) // retry of a fully confirmed stop
	}
	if r.State == "stopped" || (!takeover && r.State != previous) {
		return ErrElasticConflict
	}
	_, err = tx.ExecContext(ctx, `UPDATE elastic_reservations SET state = ?, owner_instance = ?, owner_epoch = ? WHERE id = ?`, next, owner.Instance, owner.Epoch, id)
	if err != nil {
		return err
	}
	return s.commitElasticWrite(ctx, tx, owner)
}

// ListElasticReservations includes terminal rows to retain non-reusable slots.
// No background expiry or deletion is safe before runtime reconciliation exists.
func (s *Store) ListElasticReservations(ctx context.Context, appID int64) ([]ElasticReservation, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+elasticReservationColumns+` FROM elastic_reservations WHERE app_id = ? ORDER BY slot`, appID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []ElasticReservation{}
	for rows.Next() {
		r, err := scanElasticReservation(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, *r)
	}
	return result, rows.Err()
}
