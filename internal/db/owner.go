package db

import (
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// ownerRole is the constant primary key for the single cp_owner row.
const ownerRole = "control-plane"

// ttlModifier renders a Go duration as a SQLite datetime modifier
// ("+N seconds"), flooring at 1 second so a non-positive TTL still yields a
// valid (immediately-near-expiry) lease rather than malformed SQL.
func ttlModifier(ttl time.Duration) string {
	secs := int(ttl / time.Second)
	if secs < 1 {
		secs = 1
	}
	return fmt.Sprintf("+%d seconds", secs)
}

// OwnerInfo is the current lease holder, for tests and observability.
type OwnerInfo struct {
	InstanceID string
	Epoch      int64
	ExpiresAt  string
}

// AcquireOwner attempts to take the control-plane lease for instanceID. It
// succeeds when no instance holds it or the current lease has expired, bumping
// the fencing epoch. Returns acquired=false (epoch 0) when another instance
// holds a live lease.
func (s *Store) AcquireOwner(instanceID string, ttl time.Duration) (bool, int64, error) {
	res, err := s.db.Exec(`
		INSERT INTO cp_owner (role, instance_id, epoch, acquired_at, heartbeat_at, expires_at)
		VALUES (?, ?, 1, datetime('now'), datetime('now'), datetime('now', ?))
		ON CONFLICT(role) DO UPDATE SET
			instance_id  = excluded.instance_id,
			epoch        = cp_owner.epoch + 1,
			acquired_at  = datetime('now'),
			heartbeat_at = datetime('now'),
			expires_at   = excluded.expires_at
		WHERE cp_owner.instance_id IS NULL OR cp_owner.expires_at <= datetime('now')`,
		ownerRole, instanceID, ttlModifier(ttl))
	if err != nil {
		return false, 0, fmt.Errorf("acquire owner: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return false, 0, nil // held by a live lease
	}
	var epoch int64
	err = s.db.QueryRow(`SELECT epoch FROM cp_owner WHERE role = ? AND instance_id = ?`,
		ownerRole, instanceID).Scan(&epoch)
	if errors.Is(err, sql.ErrNoRows) {
		return false, 0, nil // lost in a race between acquire and read (only with ~0 TTL)
	}
	if err != nil {
		return false, 0, fmt.Errorf("acquire owner read epoch: %w", err)
	}
	return true, epoch, nil
}

// RenewOwner extends the lease iff instanceID still holds it at epoch. ok=false
// means ownership was lost (a different instance, or a newer epoch, holds it).
func (s *Store) RenewOwner(instanceID string, epoch int64, ttl time.Duration) (bool, error) {
	res, err := s.db.Exec(`
		UPDATE cp_owner SET heartbeat_at = datetime('now'), expires_at = datetime('now', ?)
		WHERE role = ? AND instance_id = ? AND epoch = ?`,
		ttlModifier(ttl), ownerRole, instanceID, epoch)
	if err != nil {
		return false, fmt.Errorf("renew owner: %w", err)
	}
	n, _ := res.RowsAffected()
	return n == 1, nil
}

// ReleaseOwner relinquishes the lease iff instanceID holds it at epoch, marking
// it immediately expired so a waiter can acquire. A no-op (no error) when the
// lease was already lost - fencing prevents clobbering a newer owner.
func (s *Store) ReleaseOwner(instanceID string, epoch int64) error {
	// Keep the row (instance_id=NULL) rather than deleting it: the next AcquireOwner
	// takes the ON CONFLICT DO UPDATE path and bumps epoch, preserving fencing-token continuity.
	_, err := s.db.Exec(`
		UPDATE cp_owner SET instance_id = NULL, expires_at = datetime('now')
		WHERE role = ? AND instance_id = ? AND epoch = ?`,
		ownerRole, instanceID, epoch)
	if err != nil {
		return fmt.Errorf("release owner: %w", err)
	}
	return nil
}

// GetOwner returns the current lease row, or ErrNotFound if no row exists yet.
func (s *Store) GetOwner() (*OwnerInfo, error) {
	var o OwnerInfo
	var instance sql.NullString
	err := s.db.QueryRow(`SELECT instance_id, epoch, expires_at FROM cp_owner WHERE role = ?`, ownerRole).
		Scan(&instance, &o.Epoch, &o.ExpiresAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	o.InstanceID = instance.String
	return &o, nil
}
