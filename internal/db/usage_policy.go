package db

import (
	"database/sql"
	"errors"
	"fmt"
)

// UsagePolicyState is the durable hub-wide usage privacy generation. The
// pseudonym master is encrypted by the caller; the database never receives its
// plaintext form.
type UsagePolicyState struct {
	IdentityMode    string
	Generation      int64
	PseudonymKeyEnc []byte
}

func (s *Store) EnsureUsagePolicy(mode string, encryptedKey []byte) (UsagePolicyState, error) {
	_, err := s.db.Exec(`INSERT INTO usage_policy
		(singleton_id, identity_mode, generation, pseudonym_key_enc)
		VALUES (1, ?, 1, ?) ON CONFLICT (singleton_id) DO NOTHING`, mode, encryptedKey)
	if err != nil {
		return UsagePolicyState{}, fmt.Errorf("initialize usage policy: %w", err)
	}
	return s.UsagePolicy()
}

func (s *Store) UsagePolicy() (UsagePolicyState, error) {
	var p UsagePolicyState
	err := s.db.QueryRow(`SELECT identity_mode, generation, pseudonym_key_enc
		FROM usage_policy WHERE singleton_id = 1`).Scan(&p.IdentityMode, &p.Generation, &p.PseudonymKeyEnc)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return p, ErrNotFound
		}
		return p, fmt.Errorf("load usage policy: %w", err)
	}
	return p, nil
}

func (s *Store) SetUsagePolicyMode(mode string) (int64, error) {
	if _, err := s.db.Exec(`UPDATE usage_policy SET identity_mode = ?, generation = generation + 1,
		updated_at = CURRENT_TIMESTAMP WHERE singleton_id = 1`, mode); err != nil {
		return 0, fmt.Errorf("update usage policy: %w", err)
	}
	p, err := s.UsagePolicy()
	return p.Generation, err
}

// AdvanceUsagePolicyGeneration assigns a new generation to an app-level
// policy transition without changing the hub-wide identity ceiling.
func (s *Store) AdvanceUsagePolicyGeneration() (int64, error) {
	if _, err := s.db.Exec(`UPDATE usage_policy SET generation = generation + 1,
		updated_at = CURRENT_TIMESTAMP WHERE singleton_id = 1`); err != nil {
		return 0, fmt.Errorf("advance usage policy generation: %w", err)
	}
	p, err := s.UsagePolicy()
	return p.Generation, err
}

func (s *Store) UsageAppIdentityOverride(slug string) (string, error) {
	var mode sql.NullString
	if err := s.db.QueryRow(`SELECT usage_identity_mode FROM apps WHERE slug = ?`, slug).Scan(&mode); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", ErrNotFound
		}
		return "", fmt.Errorf("load app usage policy: %w", err)
	}
	return mode.String, nil
}

type UsageIdentityRow struct {
	SessionID string
	AppSlug   string
	UserID    int64
}

// UsageAppPolicy is the durable per-app override used to seed the in-memory
// policy snapshot before the proxy starts accepting connections.
type UsageAppPolicy struct {
	AppID    int64
	Slug     string
	Override string
}

// UsageAppPolicySnapshot is the authoritative policy for one app at the time
// a connection is accepted. Reading the hub mode, generation, and app
// override in one query keeps multiple control-plane instances coherent and
// prevents a deleted-and-recreated slug from inheriting a stale cache entry.
type UsageAppPolicySnapshot struct {
	AppID      int64
	HubMode    string
	Generation int64
	Override   string
}

func (s *Store) UsagePolicyForApp(slug string) (UsageAppPolicySnapshot, error) {
	var snapshot UsageAppPolicySnapshot
	err := s.db.QueryRow(`SELECT a.id, up.identity_mode, up.generation,
		COALESCE(a.usage_identity_mode, '')
		FROM apps a CROSS JOIN usage_policy up
		WHERE a.slug = ? AND up.singleton_id = 1`, slug).Scan(
		&snapshot.AppID, &snapshot.HubMode, &snapshot.Generation, &snapshot.Override,
	)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return snapshot, ErrNotFound
		}
		return snapshot, fmt.Errorf("load effective app usage policy: %w", err)
	}
	return snapshot, nil
}

func (s *Store) ListUsageAppPolicies() ([]UsageAppPolicy, error) {
	rows, err := s.db.Query(`SELECT id, slug, COALESCE(usage_identity_mode, '') FROM apps ORDER BY id`)
	if err != nil {
		return nil, fmt.Errorf("list app usage policies: %w", err)
	}
	defer rows.Close()
	var out []UsageAppPolicy
	for rows.Next() {
		var app UsageAppPolicy
		if err := rows.Scan(&app.AppID, &app.Slug, &app.Override); err != nil {
			return nil, err
		}
		out = append(out, app)
	}
	return out, rows.Err()
}

// ListUsageIdentityRows returns a bounded reconciliation batch. Rows already
// compliant with the target are excluded by the caller's target mode.
func (s *Store) ListUsageIdentityRows(appID *int64, limit int) ([]UsageIdentityRow, error) {
	if limit <= 0 {
		limit = 500
	}
	query := `SELECT us.id, a.slug, us.user_id FROM usage_sessions us
		JOIN apps a ON a.id = us.app_id WHERE us.user_id IS NOT NULL`
	args := []any{}
	if appID != nil {
		query += ` AND us.app_id = ?`
		args = append(args, *appID)
	}
	query += ` ORDER BY us.id LIMIT ?`
	args = append(args, limit)
	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("list usage identities: %w", err)
	}
	defer rows.Close()
	var out []UsageIdentityRow
	for rows.Next() {
		var row UsageIdentityRow
		if err := rows.Scan(&row.SessionID, &row.AppSlug, &row.UserID); err != nil {
			return nil, err
		}
		out = append(out, row)
	}
	return out, rows.Err()
}

func (s *Store) PseudonymizeUsageSession(id, viewerKey string, generation int64) error {
	_, err := s.db.Exec(`UPDATE usage_sessions SET user_id = NULL, viewer_key = ?,
		identity_mode = 'pseudonymous', policy_generation = ? WHERE id = ?`, viewerKey, generation, id)
	if err != nil {
		return fmt.Errorf("pseudonymize usage session: %w", err)
	}
	return nil
}

// UnattributeUsageSessions irreversibly removes all durable user-derived
// identifiers, while principal_kind preserves the audience classification.
func (s *Store) UnattributeUsageSessions(appID *int64, generation int64) (int64, error) {
	query := `UPDATE usage_sessions SET user_id = NULL, viewer_key = NULL,
		identity_mode = 'unattributed', policy_generation = ?
		WHERE (user_id IS NOT NULL OR viewer_key IS NOT NULL OR identity_mode <> 'unattributed')`
	args := []any{generation}
	if appID != nil {
		query += ` AND app_id = ?`
		args = append(args, *appID)
	}
	res, err := s.db.Exec(query, args...)
	if err != nil {
		return 0, fmt.Errorf("unattribute usage sessions: %w", err)
	}
	return res.RowsAffected()
}

func (s *Store) SetUsagePseudonymKeyEncrypted(encrypted []byte) error {
	_, err := s.db.Exec(`UPDATE usage_policy SET pseudonym_key_enc = ?, updated_at = CURRENT_TIMESTAMP
		WHERE singleton_id = 1`, encrypted)
	return err
}
