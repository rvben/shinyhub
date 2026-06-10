package db

import (
	"context"
	"fmt"
)

// AppGroupRule is a per-app group access rule.
type AppGroupRule struct {
	Group  string
	Role   string
	Source string
}

// GrantAppGroupAccess upserts a group rule (role) on slug. role must be a valid
// member role (validated by callers); source is "manual" for UI/API/CLI grants.
func (s *Store) GrantAppGroupAccess(slug, group, role, source string) error {
	_, err := s.db.Exec(
		`INSERT INTO app_group_access (app_slug, group_name, role, source) VALUES (?, ?, ?, ?)
		 ON CONFLICT (app_slug, group_name) DO UPDATE SET role = excluded.role, source = excluded.source`,
		slug, group, role, source)
	if err != nil {
		return fmt.Errorf("grant app group access: %w", err)
	}
	return nil
}

// RevokeAppGroupAccess removes a group rule. Returns ErrNotFound when absent.
func (s *Store) RevokeAppGroupAccess(slug, group string) error {
	result, err := s.db.Exec(
		`DELETE FROM app_group_access WHERE app_slug = ? AND group_name = ?`, slug, group)
	if err != nil {
		return fmt.Errorf("revoke app group access: %w", err)
	}
	n, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("revoke app group access rows: %w", err)
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// ListAppGroupAccess returns all group rules for slug, ordered by group name.
func (s *Store) ListAppGroupAccess(slug string) ([]AppGroupRule, error) {
	rows, err := s.db.Query(
		`SELECT group_name, role, source FROM app_group_access WHERE app_slug = ? ORDER BY group_name`, slug)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []AppGroupRule{}
	for rows.Next() {
		var r AppGroupRule
		if err := rows.Scan(&r.Group, &r.Role, &r.Source); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// GroupRoleForUserOnApp returns the highest-rank role granted to userID on slug
// via any of the user's groups (joined through user_groups). ok=false when no
// group rule matches. A non-nil error indicates a DB failure (callers should
// surface it rather than treat it as "no access").
func (s *Store) GroupRoleForUserOnApp(slug string, userID int64) (string, bool, error) {
	rows, err := s.db.Query(`
		SELECT aga.role
		FROM app_group_access aga
		JOIN user_groups ug ON ug.group_name = aga.group_name
		WHERE aga.app_slug = ? AND ug.user_id = ?`, slug, userID)
	if err != nil {
		return "", false, err
	}
	defer rows.Close()
	best := ""
	for rows.Next() {
		var role string
		if err := rows.Scan(&role); err != nil {
			return "", false, err
		}
		best = HigherMemberRole(best, role)
	}
	if err := rows.Err(); err != nil {
		return "", false, err
	}
	return best, best != "", nil
}

// HigherMemberRole returns the higher-rank of two member roles ("manager" >
// "viewer" > ""). Unknown roles rank as "".
func HigherMemberRole(a, b string) string {
	if memberRank(b) > memberRank(a) {
		return b
	}
	return a
}

func memberRank(role string) int {
	switch role {
	case "viewer":
		return 1
	case "manager":
		return 2
	default:
		return 0
	}
}

// ReconcileAppGroupAccessFromManifest replaces the app's source='manifest' group
// rules with exactly the given desired rules, in one transaction. Rules whose
// group already has a source='manual' row are NOT applied (manual is
// authoritative) and are returned in skipped. source='manual' rows are never
// deleted or modified. Passing nil/empty rules removes all manifest rows.
func (s *Store) ReconcileAppGroupAccessFromManifest(slug string, rules []AppGroupRule) (skipped []string, err error) {
	ctx := context.Background()
	tx, err := s.d.beginWrite(ctx, s.rawDB(), 0)
	if err != nil {
		return nil, fmt.Errorf("reconcile manifest group access: begin: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()

	// Collect groups that have a manual row - these are authoritative and must
	// not be touched.
	manual := map[string]struct{}{}
	mrows, err := tx.QueryContext(ctx,
		`SELECT group_name FROM app_group_access WHERE app_slug = ? AND source = 'manual'`, slug)
	if err != nil {
		return nil, fmt.Errorf("reconcile manifest group access: read manual: %w", err)
	}
	for mrows.Next() {
		var g string
		if err := mrows.Scan(&g); err != nil {
			mrows.Close()
			return nil, err
		}
		manual[g] = struct{}{}
	}
	mrows.Close()
	if err := mrows.Err(); err != nil {
		return nil, err
	}

	// Remove all existing manifest rows; manual rows are untouched by the WHERE.
	if _, err := tx.ExecContext(ctx,
		`DELETE FROM app_group_access WHERE app_slug = ? AND source = 'manifest'`, slug); err != nil {
		return nil, fmt.Errorf("reconcile manifest group access: delete: %w", err)
	}

	for _, rule := range rules {
		if _, isManual := manual[rule.Group]; isManual {
			skipped = append(skipped, rule.Group)
			continue
		}
		// The WHERE guard ensures a manifest reconcile cannot overwrite a manual
		// row even if a concurrent GrantAppGroupAccess inserts one between the
		// map read above and this statement. When the existing row has
		// source='manual', DO UPDATE is a no-op and the manual row is preserved.
		// This closes a Postgres READ-COMMITTED TOCTOU; on SQLite the serialised
		// writer prevents the race, but the guard is correct on both dialects.
		//
		// The guard must qualify the column as app_group_access.source: in an
		// ON CONFLICT DO UPDATE ... WHERE clause both the target row and the
		// excluded pseudo-row are in scope, so Postgres rejects a bare `source`
		// as ambiguous (42702). The guard reads the EXISTING row's source.
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO app_group_access (app_slug, group_name, role, source) VALUES (?, ?, ?, 'manifest')
			 ON CONFLICT (app_slug, group_name) DO UPDATE SET role = excluded.role, source = 'manifest'
			 WHERE app_group_access.source = 'manifest'`,
			slug, rule.Group, rule.Role); err != nil {
			return nil, fmt.Errorf("reconcile manifest group access: upsert: %w", err)
		}
	}

	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("reconcile manifest group access: commit: %w", err)
	}
	committed = true
	return skipped, nil
}
