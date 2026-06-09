package db

import (
	"context"
	"errors"
	"fmt"

	"github.com/rvben/shinyhub/internal/auth"
)

// ErrLastAdmin is returned by manual role mutations that would leave the system
// with zero admins. Automatic reconciliation never returns this; it keeps the
// last admin instead.
var ErrLastAdmin = errors.New("operation would remove the last admin")

// roleMutationLockKey serializes all effective-role writes on Postgres (advisory
// xact lock). SQLite serializes via BEGIN IMMEDIATE and ignores the key.
const roleMutationLockKey int64 = 0x53484B524F4C45 // "SHKROLE"

// GetUserGroups returns the user's snapshotted IdP group names.
func (s *Store) GetUserGroups(userID int64) ([]string, error) {
	rows, err := s.db.Query(`SELECT group_name FROM user_groups WHERE user_id = ? ORDER BY group_name`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var g string
		if err := rows.Scan(&g); err != nil {
			return nil, err
		}
		out = append(out, g)
	}
	return out, rows.Err()
}

// ReplaceUserGroups replaces the user's group snapshot wholesale, atomically.
func (s *Store) ReplaceUserGroups(userID int64, groups []string) error {
	ctx := context.Background()
	tx, err := s.d.beginWrite(ctx, s.rawDB(), roleMutationLockKey)
	if err != nil {
		return fmt.Errorf("replace user groups: begin: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()
	if err := replaceGroupsTx(ctx, tx, userID, groups); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("replace user groups: commit: %w", err)
	}
	committed = true
	return nil
}

func replaceGroupsTx(ctx context.Context, tx writeTx, userID int64, groups []string) error {
	if _, err := tx.ExecContext(ctx, `DELETE FROM user_groups WHERE user_id = ?`, userID); err != nil {
		return fmt.Errorf("clear user groups: %w", err)
	}
	for _, g := range groups {
		if g == "" {
			continue
		}
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO user_groups (user_id, group_name) VALUES (?, ?) ON CONFLICT (user_id, group_name) DO NOTHING`,
			userID, g); err != nil {
			return fmt.Errorf("insert user group: %w", err)
		}
	}
	return nil
}

func countAdminsExceptTx(ctx context.Context, tx writeTx, exceptID int64) (int, error) {
	var n int
	err := tx.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM users WHERE role = 'admin' AND id <> ?`, exceptID).Scan(&n)
	return n, err
}

func readUserRoleTx(ctx context.Context, tx writeTx, userID int64) (role string, manual *string, err error) {
	err = tx.QueryRowContext(ctx, `SELECT role, manual_role FROM users WHERE id = ?`, userID).Scan(&role, &manual)
	return role, manual, err
}

// applyEffectiveRoleTx writes the effective role + source, enforcing the
// last-admin guard. When hard is true (manual mutations) a demotion of the last
// admin returns ErrLastAdmin; when hard is false (auto reconcile) it keeps admin.
func applyEffectiveRoleTx(ctx context.Context, tx writeTx, userID int64, current, newRole, source string, hard bool) error {
	if current == "admin" && newRole != "admin" {
		others, err := countAdminsExceptTx(ctx, tx, userID)
		if err != nil {
			return err
		}
		if others == 0 {
			if hard {
				return ErrLastAdmin
			}
			return nil // auto path: keep the last admin
		}
	}
	_, err := tx.ExecContext(ctx, `UPDATE users SET role = ?, role_source = ? WHERE id = ?`, newRole, source, userID)
	return err
}

func groupsTx(ctx context.Context, tx writeTx, userID int64) ([]string, error) {
	rows, err := tx.QueryContext(ctx, `SELECT group_name FROM user_groups WHERE user_id = ?`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var g string
		if err := rows.Scan(&g); err != nil {
			return nil, err
		}
		out = append(out, g)
	}
	return out, rows.Err()
}

// ReconcileUserFromGroups replaces the user's group snapshot and recomputes the
// effective role authoritatively: manual_role if set, else the highest-rank
// group-derived role, else defaultRole. Never demotes the last admin (keeps it).
func (s *Store) ReconcileUserFromGroups(userID int64, groups []string, mappings []auth.GroupRoleMapping, defaultRole string) error {
	ctx := context.Background()
	tx, err := s.d.beginWrite(ctx, s.rawDB(), roleMutationLockKey)
	if err != nil {
		return fmt.Errorf("reconcile: begin: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()
	if err := replaceGroupsTx(ctx, tx, userID, groups); err != nil {
		return err
	}
	current, manual, err := readUserRoleTx(ctx, tx, userID)
	if err != nil {
		return fmt.Errorf("reconcile: read role: %w", err)
	}
	var newRole, source string
	if manual != nil && *manual != "" {
		newRole, source = *manual, "manual"
	} else if r, matched := auth.ResolveGlobalRole(groups, mappings, defaultRole); matched {
		newRole, source = r, "sso"
	} else {
		newRole, source = defaultRole, "default"
	}
	if err := applyEffectiveRoleTx(ctx, tx, userID, current, newRole, source, false); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("reconcile: commit: %w", err)
	}
	committed = true
	return nil
}

// SetManualRole sets a break-glass manual override (and the effective role).
// Returns ErrLastAdmin if it would demote the last admin.
func (s *Store) SetManualRole(userID int64, role string) error {
	ctx := context.Background()
	tx, err := s.d.beginWrite(ctx, s.rawDB(), roleMutationLockKey)
	if err != nil {
		return fmt.Errorf("set manual role: begin: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()
	current, _, err := readUserRoleTx(ctx, tx, userID)
	if err != nil {
		return fmt.Errorf("set manual role: read: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE users SET manual_role = ? WHERE id = ?`, role, userID); err != nil {
		return err
	}
	if err := applyEffectiveRoleTx(ctx, tx, userID, current, role, "manual", true); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("set manual role: commit: %w", err)
	}
	committed = true
	return nil
}

// ClearManualRole removes the manual override and recomputes the effective role
// from the current group snapshot (else defaultRole). Returns ErrLastAdmin if
// that would demote the last admin.
func (s *Store) ClearManualRole(userID int64, mappings []auth.GroupRoleMapping, defaultRole string) error {
	ctx := context.Background()
	tx, err := s.d.beginWrite(ctx, s.rawDB(), roleMutationLockKey)
	if err != nil {
		return fmt.Errorf("clear manual role: begin: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()
	current, _, err := readUserRoleTx(ctx, tx, userID)
	if err != nil {
		return fmt.Errorf("clear manual role: read: %w", err)
	}
	groups, err := groupsTx(ctx, tx, userID)
	if err != nil {
		return err
	}
	newRole, source := defaultRole, "default"
	if r, matched := auth.ResolveGlobalRole(groups, mappings, defaultRole); matched {
		newRole, source = r, "sso"
	}
	// applyEffectiveRoleTx runs before the NULL write so a rejected clear leaves
	// manual_role intact (the tx rolls back regardless, but this ordering is clearest).
	if err := applyEffectiveRoleTx(ctx, tx, userID, current, newRole, source, true); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE users SET manual_role = NULL WHERE id = ?`, userID); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("clear manual role: commit: %w", err)
	}
	committed = true
	return nil
}
