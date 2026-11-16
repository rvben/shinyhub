package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

var ErrOIDCLinkConflict = errors.New("OIDC identity link conflicts with existing account policy")

// BindOIDCProvider prevents a later issuer/client change from resolving an
// unrelated subject through a legacy (oidc, sub) link. The first upgraded
// configuration establishes the binding; operators must verify it beforehand.
func (s *Store) BindOIDCProvider(issuer, clientID string) error {
	if issuer == "" || clientID == "" {
		return fmt.Errorf("OIDC authority is required")
	}
	_, err := s.db.Exec(`INSERT INTO oidc_provider_binding (id, issuer, client_id) VALUES (1, ?, ?) ON CONFLICT (id) DO NOTHING`, issuer, clientID)
	if err != nil {
		return err
	}
	return s.CheckOIDCProvider(issuer, clientID)
}

// CheckOIDCProvider validates an existing binding without establishing one.
func (s *Store) CheckOIDCProvider(issuer, clientID string) error {
	var i, c string
	if err := s.db.QueryRow(`SELECT issuer, client_id FROM oidc_provider_binding WHERE id = 1`).Scan(&i, &c); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		return err
	}
	if i != issuer || c != clientID {
		return fmt.Errorf("OIDC authority changed: existing accounts are bound to a different issuer/client; explicit identity migration is required")
	}
	return nil
}

// LinkOIDCUser never merges users or transfers credentials. It preserves the
// original row and refuses local, managed, service and already-linked accounts.
// dryRun runs the same checks without writing. Repeating the same link is safe.
// expectedID pins an admin-selected row even if its username is concurrently reused.
func (s *Store) LinkOIDCUser(username, sub string, dryRun bool, expectedID ...int64) (*User, bool, error) {
	id := int64(0)
	if len(expectedID) > 0 {
		id = expectedID[0]
	}
	return s.linkOIDCUser(username, sub, dryRun, id, false)
}

// LinkOIDCUserAutomatically permits only unprivileged, unambiguous corporate handles.
func (s *Store) LinkOIDCUserAutomatically(username, sub string) (*User, bool, error) {
	return s.linkOIDCUser(username, sub, false, 0, true)
}

func (s *Store) linkOIDCUser(username, sub string, dryRun bool, expectedID int64, automatic bool) (*User, bool, error) {
	if username == "" || sub == "" || len(sub) > 1024 {
		return nil, false, ErrOIDCLinkConflict
	}
	ctx := context.Background()
	tx, err := s.d.beginWrite(ctx, s.rawDB(), roleMutationLockKey)
	if err != nil {
		return nil, false, err
	}
	defer tx.Rollback()
	var id int64
	var hash, principal, managed, role, manual string
	query := `SELECT id,username,password_hash,principal_type,COALESCE(managed_by,''),role,COALESCE(manual_role,'') FROM users WHERE username = ?`
	var target any = username
	if automatic {
		query = `SELECT id,username,password_hash,principal_type,COALESCE(managed_by,''),role,COALESCE(manual_role,'') FROM users WHERE lower(username)=lower(?) LIMIT 2`
	}
	if expectedID != 0 {
		query = `SELECT id,username,password_hash,principal_type,COALESCE(managed_by,''),role,COALESCE(manual_role,'') FROM users WHERE id = ?`
		target = expectedID
	}
	rows, err := tx.QueryContext(ctx, query, target)
	if err != nil {
		return nil, false, err
	}
	countMatches := 0
	for rows.Next() {
		countMatches++
		if err := rows.Scan(&id, &username, &hash, &principal, &managed, &role, &manual); err != nil {
			rows.Close()
			return nil, false, err
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, false, err
	}
	if countMatches == 0 {
		return nil, false, ErrNotFound
	}
	if countMatches > 1 || (automatic && (hash != systemUserPasswordHash || role == "admin" || manual != "")) {
		return nil, false, ErrOIDCLinkConflict
	}
	if (hash != "" && hash != systemUserPasswordHash) || principal == "service_account" || managed != "" || IsReservedUsername(username) {
		return nil, false, ErrOIDCLinkConflict
	}
	var linkedID int64
	err = tx.QueryRowContext(ctx, `SELECT user_id FROM oauth_accounts WHERE provider = 'oidc' AND provider_id = ?`, sub).Scan(&linkedID)
	if err == nil {
		if linkedID != id {
			return nil, false, ErrOIDCLinkConflict
		}
		tx.Rollback()
		u, e := s.GetUserByID(id)
		return u, false, e
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return nil, false, err
	}
	var count int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM oauth_accounts WHERE user_id = ?`, id).Scan(&count); err != nil {
		return nil, false, err
	}
	if count != 0 {
		return nil, false, ErrOIDCLinkConflict
	}
	if dryRun {
		tx.Rollback()
		u, e := s.GetUserByID(id)
		return u, false, e
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO oauth_accounts (user_id,provider,provider_id) VALUES (?, 'oidc', ?)`, id, sub); err != nil {
		if s.d.isUniqueViolation(err) {
			return nil, false, fmt.Errorf("%w: link could not be saved", ErrOIDCLinkConflict)
		}
		return nil, false, fmt.Errorf("save OIDC link: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return nil, false, err
	}
	u, e := s.GetUserByID(id)
	return u, true, e
}

// OIDCSession stores only an encrypted ID token, for optional single logout.
type OIDCSession struct {
	JTI                  string
	UserID               int64
	Issuer, SID, IDToken string
	ExpiresAt            time.Time
}

func (s *Store) SaveOIDCSession(v OIDCSession) error {
	if err := s.PruneOIDCSessions(); err != nil {
		return err
	}
	_, err := s.db.Exec(`INSERT INTO oidc_sessions (jti,user_id,issuer,sid,id_token,expires_at) VALUES (?,?,?,?,?,?)`, v.JTI, v.UserID, v.Issuer, v.SID, v.IDToken, v.ExpiresAt.Unix())
	return err
}
func (s *Store) GetOIDCSession(jti string) (*OIDCSession, error) {
	var v OIDCSession
	var expires int64
	err := s.db.QueryRow(`SELECT jti,user_id,issuer,sid,id_token,expires_at FROM oidc_sessions WHERE jti = ? AND expires_at > ?`, jti, time.Now().Unix()).Scan(&v.JTI, &v.UserID, &v.Issuer, &v.SID, &v.IDToken, &expires)
	v.ExpiresAt = time.Unix(expires, 0)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return &v, err
}
func (s *Store) RevokeOIDCSessions(issuer, sid string, retainUntil ...time.Time) error {
	minimum := int64(0)
	if len(retainUntil) > 0 {
		minimum = retainUntil[0].Unix()
	}
	if issuer == "" || sid == "" {
		return ErrOIDCLinkConflict
	}
	var found bool
	if err := s.db.QueryRow(`SELECT EXISTS(SELECT 1 FROM oidc_sessions WHERE issuer = ? AND sid = ? AND expires_at > ?)`, issuer, sid, time.Now().Unix()).Scan(&found); err != nil {
		return err
	}
	if !found {
		return nil
	}
	_, err := s.db.Exec(`INSERT INTO revoked_tokens (jti,user_id,expires_at) SELECT jti,user_id,CASE WHEN expires_at < ? THEN ? ELSE expires_at END FROM oidc_sessions WHERE issuer = ? AND sid = ? AND expires_at > ? ON CONFLICT (jti) DO UPDATE SET expires_at = CASE WHEN excluded.expires_at > revoked_tokens.expires_at THEN excluded.expires_at ELSE revoked_tokens.expires_at END`, minimum, minimum, issuer, sid, time.Now().Unix())
	if err != nil {
		return err
	}
	// Only remove hints whose JTI was actually revoked, including during concurrent login.
	_, err = s.db.Exec(`DELETE FROM oidc_sessions WHERE issuer = ? AND sid = ? AND jti IN (SELECT jti FROM revoked_tokens)`, issuer, sid)
	return err
}

func (s *Store) DeleteOIDCSession(jti string) error {
	_, err := s.db.Exec(`DELETE FROM oidc_sessions WHERE jti = ?`, jti)
	return err
}

// RebindEmptyOIDCProvider is an explicit offline recovery operation. It refuses
// authority changes once any OIDC identity or live OIDC session depends on it.
func (s *Store) RebindEmptyOIDCProvider(issuer, clientID string) error {
	if issuer == "" || clientID == "" {
		return fmt.Errorf("OIDC issuer and client ID are required")
	}
	ctx := context.Background()
	tx, err := s.d.beginWrite(ctx, s.rawDB(), roleMutationLockKey)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var count int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM oauth_accounts WHERE provider = 'oidc'`).Scan(&count); err != nil {
		return err
	}
	if count != 0 {
		return fmt.Errorf("cannot rebind OIDC while identities exist; plan an explicit identity migration")
	}
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM oidc_sessions WHERE expires_at > ?`, time.Now().Unix()).Scan(&count); err != nil {
		return err
	}
	if count != 0 {
		return fmt.Errorf("cannot rebind OIDC while sessions exist")
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO oidc_provider_binding (id,issuer,client_id) VALUES (1,?,?) ON CONFLICT(id) DO UPDATE SET issuer=excluded.issuer,client_id=excluded.client_id`, issuer, clientID); err != nil {
		return err
	}
	return tx.Commit()
}

// PruneOIDCSessions deletes expired encrypted hints even when SSO is disabled.
func (s *Store) PruneOIDCSessions() error {
	_, err := s.db.Exec(`DELETE FROM oidc_sessions WHERE expires_at <= ?`, time.Now().Unix())
	return err
}

// ExtendOIDCSession keeps front-channel correlation for a renewed browser family
// if the operator has increased the maximum age since its original login.
func (s *Store) ExtendOIDCSession(jti string, deadline time.Time) error {
	// Avoid a SQLite writer lock for ordinary renewals and non-OIDC sessions.
	session, err := s.GetOIDCSession(jti)
	if errors.Is(err, ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	if !deadline.After(session.ExpiresAt) {
		return nil
	}
	_, err = s.db.Exec(`UPDATE oidc_sessions SET expires_at = ? WHERE jti = ? AND expires_at < ?`, deadline.Unix(), jti, deadline.Unix())
	return err
}
