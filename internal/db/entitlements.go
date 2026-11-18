package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/rvben/shinyhub/internal/entitlementpolicy"
)

// MaxAppEntitlements bounds the signed claim without silently truncating grants.
const MaxAppEntitlements = entitlementpolicy.MaxDefinitions
const entitlementMutationLockKey int64 = 0x5348454e54 // "SHENT"

var ErrInvalidEntitlement = errors.New("invalid entitlement")
var ErrEntitlementLimit = errors.New("an app may define at most 64 entitlements")
var ErrEntitlementInUse = errors.New("revoke all grants before deleting an entitlement")

type AppEntitlement struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	ManagedBy   string `json:"managed_by,omitempty"`
}

// DefineAppEntitlementParams distinguishes an omitted description from an
// explicit empty string. Omission preserves the existing description.
type DefineAppEntitlementParams struct {
	Name        string
	Description *string
}

// EntitlementGrant retains each source even when several sources grant the same
// entitlement. UserID is stable; Username and Group are for inspection.
type EntitlementGrant struct {
	Entitlement string `json:"entitlement"`
	Source      string `json:"source"`
	UserID      int64  `json:"user_id,omitempty"`
	Username    string `json:"username,omitempty"`
	Group       string `json:"group,omitempty"`
	ManagedBy   string `json:"managed_by,omitempty"`
}

type EntitlementPrincipal struct {
	UserID int64
	Group  string
}

func (p EntitlementPrincipal) valid() bool {
	if p.UserID > 0 {
		return p.Group == ""
	}
	return p.UserID == 0 && entitlementpolicy.ValidGroup(p.Group)
}

func (s *Store) ListAppEntitlements(appID int64) ([]AppEntitlement, error) {
	rows, err := s.db.Query(`SELECT name, description, managed_by FROM app_entitlements WHERE app_id = ? ORDER BY name`, appID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []AppEntitlement{}
	for rows.Next() {
		var e AppEntitlement
		if err := rows.Scan(&e.Name, &e.Description, &e.ManagedBy); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// ListAppEntitlementGrants lists all assignments, or only the sources effective
// for userID when nonzero. Group names use the existing IdP snapshot verbatim.
func (s *Store) ListAppEntitlementGrants(appID, userID int64) ([]EntitlementGrant, error) {
	query := `SELECT e.entitlement, 'user', e.user_id, u.username, '', 'manual'
		FROM app_user_entitlements e JOIN users u ON u.id = e.user_id WHERE e.app_id = ?`
	args := []any{appID}
	if userID != 0 {
		query += ` AND e.user_id = ?`
		args = append(args, userID)
	}
	query += ` UNION ALL SELECT e.entitlement, 'group', 0, '', e.group_name, e.managed_by
		FROM app_group_entitlements e WHERE e.app_id = ?`
	args = append(args, appID)
	if userID != 0 {
		query += ` AND EXISTS (SELECT 1 FROM user_groups ug WHERE ug.user_id = ? AND ug.group_name = e.group_name)`
		args = append(args, userID)
	}
	query += ` ORDER BY 1, 2, 3, 5`
	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []EntitlementGrant{}
	for rows.Next() {
		var g EntitlementGrant
		if err := rows.Scan(&g.Entitlement, &g.Source, &g.UserID, &g.Username, &g.Group, &g.ManagedBy); err != nil {
			return nil, err
		}
		out = append(out, g)
	}
	return out, rows.Err()
}

// AppEntitlementsForUser reads the effective union in one statement, uncached.
// Admission and platform roles never consult these tables.
func (s *Store) AppEntitlementsForUser(appID, userID int64) ([]string, error) {
	rows, err := s.db.Query(`SELECT entitlement FROM app_user_entitlements WHERE app_id = ? AND user_id = ?
		UNION SELECT e.entitlement FROM app_group_entitlements e
		JOIN user_groups ug ON ug.group_name = e.group_name WHERE e.app_id = ? AND ug.user_id = ? ORDER BY 1`,
		appID, userID, appID, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		out = append(out, name)
	}
	return out, rows.Err()
}

// mutateEntitlement commits the change and its audit event together. Unlike
// best-effort operational auditing, a grant must not succeed without its audit.
type entitlementEvent struct {
	action string
	detail map[string]any
}

func (s *Store) mutateEntitlement(appID int64, audit AuditEventParams, change func(writeTx) (map[string]any, error)) error {
	return s.mutateEntitlements(appID, audit, func(tx writeTx, _ string) ([]entitlementEvent, error) {
		detail, err := change(tx)
		if detail == nil || err != nil {
			return nil, err
		}
		return []entitlementEvent{{audit.Action, detail}}, nil
	})
}

func (s *Store) mutateEntitlements(appID int64, audit AuditEventParams, change func(writeTx, string) ([]entitlementEvent, error)) error {
	if audit.UserID == nil || *audit.UserID <= 0 {
		return errors.New("entitlement mutation requires an audit actor")
	}
	tx, err := s.d.beginWrite(context.Background(), s.rawDB(), entitlementMutationLockKey)
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck
	var slug, owner string
	query := `SELECT slug, COALESCE(managed_by, '') FROM apps WHERE id = ?`
	if s.IsPostgres() {
		query += " FOR UPDATE"
	}
	if err := tx.QueryRow(query, appID).Scan(&slug, &owner); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		return err
	}
	events, err := change(tx, owner)
	if err != nil {
		return err
	}
	if len(events) == 0 {
		return tx.Commit()
	}
	var actorUsername string
	if err := tx.QueryRow(`SELECT username FROM users WHERE id = ?`, *audit.UserID).Scan(&actorUsername); err != nil {
		return fmt.Errorf("resolve entitlement audit actor: %w", err)
	}
	records := make([]AuditEventParams, 0, len(events))
	for _, event := range events {
		event.detail["actor_user_id"], event.detail["actor_username"] = *audit.UserID, actorUsername
		event.detail["app_id"] = appID
		record := audit
		record.Action, record.ResourceType, record.ResourceID = event.action, "app", slug
		record.Detail = AuditDetail(event.detail)
		if err := insertAuditEvent(tx.Exec, record); err != nil {
			s.reportAuditWriteFailure(event.action, err)
			return fmt.Errorf("record entitlement audit: %w", err)
		}
		records = append(records, record)
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	for _, record := range records {
		s.emitAuditLog(record, nil)
	}
	return nil
}

func (s *Store) DefineAppEntitlement(appID int64, e DefineAppEntitlementParams, audit AuditEventParams) error {
	if !entitlementpolicy.ValidName(e.Name) || (e.Description != nil && !entitlementpolicy.ValidDescription(*e.Description)) {
		return ErrInvalidEntitlement
	}
	audit.Action = "entitlement.define"
	return s.mutateEntitlement(appID, audit, func(tx writeTx) (map[string]any, error) {
		var old, owner string
		err := tx.QueryRow(`SELECT description, managed_by FROM app_entitlements WHERE app_id = ? AND name = ?`, appID, e.Name).Scan(&old, &owner)
		exists := err == nil
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return nil, err
		}
		description := old
		if e.Description != nil {
			description = *e.Description
		}
		if exists && old == description {
			return nil, nil
		}
		if exists && owner != entitlementpolicy.Manual {
			return nil, managedEntitlementError(owner)
		}
		if !exists {
			var count int
			if err := tx.QueryRow(`SELECT COUNT(*) FROM app_entitlements WHERE app_id = ?`, appID).Scan(&count); err != nil {
				return nil, err
			}
			if count >= MaxAppEntitlements {
				return nil, ErrEntitlementLimit
			}
		}
		_, err = tx.Exec(`INSERT INTO app_entitlements (app_id, name, description) VALUES (?, ?, ?)
			ON CONFLICT (app_id, name) DO UPDATE SET description = excluded.description`, appID, e.Name, description)
		return map[string]any{"entitlement": e.Name, "description": description, "previous_description": old, "created": !exists}, err
	})
}

func (s *Store) DeleteAppEntitlement(appID int64, name string, audit AuditEventParams) error {
	audit.Action = "entitlement.delete"
	return s.mutateEntitlement(appID, audit, func(tx writeTx) (map[string]any, error) {
		var owner string
		err := tx.QueryRow(`SELECT managed_by FROM app_entitlements WHERE app_id = ? AND name = ?`, appID, name).Scan(&owner)
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		if err != nil {
			return nil, err
		}
		if owner != entitlementpolicy.Manual {
			return nil, managedEntitlementError(owner)
		}
		var used bool
		if err := tx.QueryRow(`SELECT EXISTS(SELECT 1 FROM app_user_entitlements WHERE app_id = ? AND entitlement = ?
			UNION ALL SELECT 1 FROM app_group_entitlements WHERE app_id = ? AND entitlement = ?)`, appID, name, appID, name).Scan(&used); err != nil {
			return nil, err
		}
		if used {
			return nil, ErrEntitlementInUse
		}
		_, err = tx.Exec(`DELETE FROM app_entitlements WHERE app_id = ? AND name = ?`, appID, name)
		return map[string]any{"entitlement": name}, err
	})
}

// SetAppEntitlementGrant changes one source. Revocation never denies another
// source: deleting a user grant leaves any matching group grants effective.
func (s *Store) SetAppEntitlementGrant(appID int64, name string, principal EntitlementPrincipal, grant bool, audit AuditEventParams) error {
	if !entitlementpolicy.ValidName(name) || !principal.valid() {
		return ErrInvalidEntitlement
	}
	audit.Action = "entitlement.revoke"
	if grant {
		audit.Action = "entitlement.grant"
	}
	return s.mutateEntitlement(appID, audit, func(tx writeTx) (map[string]any, error) {
		var exists bool
		if err := tx.QueryRow(`SELECT EXISTS(SELECT 1 FROM app_entitlements WHERE app_id = ? AND name = ?)`, appID, name).Scan(&exists); err != nil {
			return nil, err
		}
		if !exists {
			return nil, ErrNotFound
		}
		table, column, value := "app_group_entitlements", "group_name", any(principal.Group)
		detail := map[string]any{"entitlement": name, "source": "group", "group": principal.Group}
		if principal.UserID > 0 {
			var username string
			if err := tx.QueryRow(`SELECT username FROM users WHERE id = ?`, principal.UserID).Scan(&username); err != nil {
				if errors.Is(err, sql.ErrNoRows) {
					return nil, ErrNotFound
				}
				return nil, err
			}
			table, column, value = "app_user_entitlements", "user_id", principal.UserID
			detail = map[string]any{"entitlement": name, "source": "user", "user_id": principal.UserID, "username": username}
		}
		query := `DELETE FROM ` + table + ` WHERE app_id = ? AND entitlement = ? AND ` + column + ` = ?`
		if !grant && principal.UserID == 0 {
			var owner string
			err := tx.QueryRow(`SELECT managed_by FROM app_group_entitlements WHERE app_id = ? AND entitlement = ? AND group_name = ?`, appID, name, principal.Group).Scan(&owner)
			if err != nil && !errors.Is(err, sql.ErrNoRows) {
				return nil, err
			}
			if err == nil && owner != entitlementpolicy.Manual {
				return nil, managedEntitlementError(owner)
			}
		}
		if grant {
			query = `INSERT INTO ` + table + ` (app_id, entitlement, ` + column + `) VALUES (?, ?, ?) ON CONFLICT DO NOTHING`
		}
		res, err := tx.Exec(query, appID, name, value)
		if err != nil {
			return nil, err
		}
		n, err := res.RowsAffected()
		if err != nil {
			return nil, err
		}
		if n == 0 {
			return nil, nil
		}
		return detail, nil
	})
}
