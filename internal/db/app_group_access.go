package db

import "fmt"

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
