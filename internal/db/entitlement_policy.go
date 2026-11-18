package db

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	policy "github.com/rvben/shinyhub/internal/entitlementpolicy"
)

var ErrEntitlementOwnership = policy.ErrOwnership
var ErrEntitlementPolicyValidation = errors.New("invalid entitlement policy")
var ErrEntitlementStale = errors.New("entitlement policy changed since preview; run fleet plan again")

func managedEntitlementError(owner string) error {
	return fmt.Errorf("%w: managed by %s; update the fleet policy or run apps entitlements relinquish", ErrEntitlementOwnership, owner)
}

func readEntitlementPolicy(tx writeTx, appID int64) (policy.State, error) {
	var state policy.State
	rows, err := tx.Query(`SELECT name, description, managed_by,
		(SELECT COUNT(*) FROM app_user_entitlements u WHERE u.app_id = e.app_id AND u.entitlement = e.name)
		FROM app_entitlements e WHERE app_id = ? ORDER BY name`, appID)
	if err != nil {
		return state, err
	}
	for rows.Next() {
		var item policy.StoredDefinition
		if err := rows.Scan(&item.Name, &item.Description, &item.Owner, &item.DirectCount); err != nil {
			rows.Close()
			return state, err
		}
		state.Definitions = append(state.Definitions, item)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return state, err
	}
	rows, err = tx.Query(`SELECT entitlement, group_name, managed_by FROM app_group_entitlements
		WHERE app_id = ? ORDER BY entitlement, group_name`, appID)
	if err != nil {
		return state, err
	}
	defer rows.Close()
	for rows.Next() {
		var item policy.StoredGroup
		if err := rows.Scan(&item.Name, &item.Group, &item.Owner); err != nil {
			return state, err
		}
		state.Groups = append(state.Groups, item)
	}
	return state, rows.Err()
}

func entitlementPolicyPreview(appID int64, owner string, desired policy.Policy, state policy.State) (policy.Preview, error) {
	plan, err := state.Preview(desired, owner)
	if errors.Is(err, ErrEntitlementOwnership) {
		return policy.Preview{}, err
	}
	if err != nil {
		return policy.Preview{}, fmt.Errorf("%w: %v", ErrEntitlementPolicyValidation, err)
	}
	// All values have concrete JSON types. The token also binds the request to
	// the app identity and desired state, including the adoption declarations.
	data, err := json.Marshal(struct {
		AppID   int64
		Owner   string
		Desired policy.Policy
		State   policy.State
	}{appID, owner, desired, state})
	if err != nil {
		return policy.Preview{}, err
	}
	return policy.Preview{Plan: plan, AppID: appID, Token: fmt.Sprintf("%x", sha256.Sum256(data))}, nil
}

func (s *Store) PreviewEntitlementPolicy(ctx context.Context, appID int64, owner string, desired policy.Policy) (policy.Preview, error) {
	if !policy.ValidOwner(owner) || (desired != nil && desired.Validate() != nil) {
		return policy.Preview{}, ErrInvalidEntitlement
	}
	tx, err := s.beginReadSnapshot(ctx)
	if err != nil {
		return policy.Preview{}, err
	}
	defer tx.Rollback() //nolint:errcheck
	var liveOwner string
	err = tx.QueryRow(`SELECT COALESCE(managed_by, '') FROM apps WHERE id = ?`, appID).Scan(&liveOwner)
	if errors.Is(err, sql.ErrNoRows) {
		return policy.Preview{}, ErrNotFound
	}
	if err != nil {
		return policy.Preview{}, err
	}
	state, err := readEntitlementPolicy(tx, appID)
	if err != nil {
		return policy.Preview{}, err
	}
	if liveOwner != owner && policy.ValidOwner(liveOwner) && state.HasOwner(liveOwner) {
		return policy.Preview{}, managedEntitlementError(liveOwner)
	}
	if desired == nil {
		return policy.Preview{AppID: appID, Retained: state.HasOwner(owner)}, nil
	}
	return entitlementPolicyPreview(appID, owner, desired, state)
}

func (s *Store) ApplyEntitlementPolicy(appID int64, owner, token string, desired policy.Policy, audit AuditEventParams) error {
	if !policy.ValidOwner(owner) || token == "" || desired == nil || desired.Validate() != nil {
		return ErrInvalidEntitlement
	}
	return s.mutateEntitlements(appID, audit, func(tx writeTx, liveOwner string) ([]entitlementEvent, error) {
		if liveOwner != owner {
			return nil, ErrEntitlementStale
		}
		state, err := readEntitlementPolicy(tx, appID)
		if err != nil {
			return nil, err
		}
		preview, err := entitlementPolicyPreview(appID, owner, desired, state)
		if err != nil {
			return nil, err
		}
		if preview.Token != token {
			return nil, ErrEntitlementStale
		}
		return executeEntitlementChanges(tx, appID, preview.Changes, owner, "manifest:"+owner[len("fleet:"):])
	})
}

func (s *Store) RelinquishEntitlementPolicy(appID int64, owner string, audit AuditEventParams) error {
	if !policy.ValidOwner(owner) {
		return ErrInvalidEntitlement
	}
	return s.mutateEntitlements(appID, audit, func(tx writeTx, _ string) ([]entitlementEvent, error) {
		state, err := readEntitlementPolicy(tx, appID)
		if err != nil {
			return nil, err
		}
		var changes []policy.Change
		for _, def := range state.Definitions {
			if def.Owner == owner {
				changes = append(changes, policy.Change{Action: policy.Relinquish, Name: def.Name, Owner: policy.Manual})
			}
		}
		for _, grant := range state.Groups {
			if grant.Owner == owner {
				changes = append(changes, policy.Change{Action: policy.Relinquish, Name: grant.Name, Group: grant.Group, Owner: policy.Manual})
			}
		}
		return executeEntitlementChanges(tx, appID, changes, owner, "api")
	})
}

func executeEntitlementChanges(tx writeTx, appID int64, changes []policy.Change, owner, origin string) ([]entitlementEvent, error) {
	events := make([]entitlementEvent, 0, len(changes))
	for _, change := range changes {
		var err error
		switch change.Action {
		case policy.Define:
			_, err = tx.Exec(`INSERT INTO app_entitlements (app_id, name, description, managed_by) VALUES (?, ?, ?, ?)
				ON CONFLICT (app_id, name) DO UPDATE SET description = excluded.description`,
				appID, change.Name, change.Description, owner)
		case policy.Delete:
			_, err = tx.Exec(`DELETE FROM app_entitlements WHERE app_id = ? AND name = ?`, appID, change.Name)
		case policy.Grant:
			_, err = tx.Exec(`INSERT INTO app_group_entitlements (app_id, entitlement, group_name, managed_by)
				VALUES (?, ?, ?, ?)`, appID, change.Name, change.Group, owner)
		case policy.Revoke:
			_, err = tx.Exec(`DELETE FROM app_group_entitlements WHERE app_id = ? AND entitlement = ? AND group_name = ?`,
				appID, change.Name, change.Group)
		case policy.Adopt, policy.Relinquish:
			if change.Group != "" {
				_, err = tx.Exec(`UPDATE app_group_entitlements SET managed_by = ?
					WHERE app_id = ? AND entitlement = ? AND group_name = ?`, change.Owner, appID, change.Name, change.Group)
			} else if change.Action == policy.Adopt {
				_, err = tx.Exec(`UPDATE app_entitlements SET managed_by = ?, description = ? WHERE app_id = ? AND name = ?`,
					change.Owner, change.Description, appID, change.Name)
			} else {
				_, err = tx.Exec(`UPDATE app_entitlements SET managed_by = ? WHERE app_id = ? AND name = ?`,
					change.Owner, appID, change.Name)
			}
		default:
			return nil, fmt.Errorf("unknown entitlement change %q", change.Action)
		}
		if err != nil {
			return nil, err
		}
		detail := map[string]any{"entitlement": change.Name, "managed_by": change.Owner, "fleet": owner, "origin": origin}
		if change.Group != "" {
			detail["group"], detail["source"] = change.Group, "group"
		} else if change.Action == policy.Define || change.Action == policy.Adopt {
			detail["description"], detail["previous_description"] = change.Description, change.Previous
		}
		events = append(events, entitlementEvent{"entitlement." + change.Action, detail})
	}
	return events, nil
}

// ManagedEntitlementPolicyForApps reports whether the current fleet owns any
// entitlement policy on each requested app. The app list uses one page read.
func (s *Store) ManagedEntitlementPolicyForApps(ids []int64) (map[int64]bool, error) {
	result := make(map[int64]bool)
	if len(ids) == 0 {
		return result, nil
	}
	args := make([]any, len(ids))
	for i, id := range ids {
		args[i] = id
	}
	rows, err := s.db.Query(`SELECT a.id FROM apps a WHERE a.id IN (`+
		strings.TrimSuffix(strings.Repeat("?,", len(ids)), ",")+`)
		AND (EXISTS (SELECT 1 FROM app_entitlements e WHERE e.app_id = a.id AND e.managed_by = a.managed_by)
		  OR EXISTS (SELECT 1 FROM app_group_entitlements g WHERE g.app_id = a.id AND g.managed_by = a.managed_by))
		AND a.managed_by LIKE 'fleet:%'`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		result[id] = true
	}
	return result, rows.Err()
}

// SetAppFleetOwner prevents takeover of a policy concurrently established by
// the previous fleet. Generic unmanagement preserves the policy rows for
// explicit relinquishment through the entitlement API.
func (s *Store) SetAppFleetOwner(appID int64, newOwner string) error {
	tx, err := s.d.beginWrite(context.Background(), s.rawDB(), entitlementMutationLockKey)
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck
	var current string
	query := `SELECT COALESCE(managed_by, '') FROM apps WHERE id = ?`
	if s.IsPostgres() {
		query += " FOR UPDATE"
	}
	if err := tx.QueryRow(query, appID).Scan(&current); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		return err
	}
	if current != newOwner && policy.ValidOwner(current) {
		var hasPolicy bool
		err := tx.QueryRow(`SELECT EXISTS(SELECT 1 FROM app_entitlements WHERE app_id = ? AND managed_by = ?
			UNION ALL SELECT 1 FROM app_group_entitlements WHERE app_id = ? AND managed_by = ?)`,
			appID, current, appID, current).Scan(&hasPolicy)
		if err != nil {
			return err
		}
		if hasPolicy {
			return managedEntitlementError(current)
		}
	}
	if _, err := tx.Exec(`UPDATE apps SET managed_by = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ?`, newOwner, appID); err != nil {
		return err
	}
	return tx.Commit()
}
