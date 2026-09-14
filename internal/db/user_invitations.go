package db

import (
	"database/sql"
	"errors"
	"time"
)

// Invitations store only the digest of the bearer secret. Consuming the link
// and creating the local account are one transaction on both databases.
type UserInvitation struct {
	ID        string    `json:"id"`
	Username  string    `json:"username"`
	Role      string    `json:"role"`
	CreatedBy int64     `json:"created_by"`
	CreatedAt time.Time `json:"created_at"`
	ExpiresAt time.Time `json:"expires_at"`
}

func (s *Store) CreateUserInvitation(inv UserInvitation, tokenHash string) error {
	if IsReservedUsername(inv.Username) {
		return ErrReservedUsername
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.Exec(`DELETE FROM user_invitations WHERE expires_at <= ?`, time.Now().UTC()); err != nil {
		return err
	}
	var count int
	if err = tx.QueryRow(`SELECT COUNT(*) FROM users WHERE username = ?`, inv.Username).Scan(&count); err != nil {
		return err
	}
	if count != 0 {
		return ErrUsernameExists
	}
	_, err = tx.Exec(`INSERT INTO user_invitations (id, token_hash, username, role, created_by, created_at, expires_at) VALUES (?, ?, ?, ?, ?, ?, ?)`, inv.ID, tokenHash, inv.Username, inv.Role, inv.CreatedBy, inv.CreatedAt, inv.ExpiresAt)
	if err != nil {
		if s.d.isUniqueViolation(err) {
			return ErrUsernameExists
		}
		return err
	}
	return tx.Commit()
}

func (s *Store) ListUserInvitations() ([]UserInvitation, error) {
	rows, err := s.db.Query(`SELECT id, username, role, created_by, created_at, expires_at FROM user_invitations ORDER BY created_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []UserInvitation{}
	for rows.Next() {
		var inv UserInvitation
		if err := rows.Scan(&inv.ID, &inv.Username, &inv.Role, &inv.CreatedBy, &inv.CreatedAt, &inv.ExpiresAt); err != nil {
			return nil, err
		}
		result = append(result, inv)
	}
	return result, rows.Err()
}

func (s *Store) GetUserInvitation(tokenHash string) (*UserInvitation, error) {
	var inv UserInvitation
	err := s.db.QueryRow(`SELECT id, username, role, created_by, created_at, expires_at FROM user_invitations WHERE token_hash = ? AND expires_at > ? AND EXISTS (SELECT 1 FROM users WHERE users.id = user_invitations.created_by AND users.role = 'admin' AND users.principal_type = 'human')`, tokenHash, time.Now().UTC()).Scan(&inv.ID, &inv.Username, &inv.Role, &inv.CreatedBy, &inv.CreatedAt, &inv.ExpiresAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &inv, nil
}

func (s *Store) RevokeUserInvitation(id string) (bool, error) {
	result, err := s.db.Exec(`DELETE FROM user_invitations WHERE id = ?`, id)
	if err != nil {
		return false, err
	}
	n, err := result.RowsAffected()
	return n == 1, err
}

func (s *Store) AcceptUserInvitation(tokenHash, passwordHash string) (*User, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	var username, role string
	err = tx.QueryRow(`DELETE FROM user_invitations WHERE token_hash = ? AND expires_at > ? AND EXISTS (SELECT 1 FROM users WHERE users.id = user_invitations.created_by AND users.role = 'admin' AND users.principal_type = 'human') RETURNING username, role`, tokenHash, time.Now().UTC()).Scan(&username, &role)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if IsReservedUsername(username) {
		return nil, ErrReservedUsername
	}
	var id int64
	err = tx.QueryRow(`INSERT INTO users (username,password_hash,role,manual_role,role_source) VALUES (?,?,?,?,'manual') RETURNING id`, username, passwordHash, role, role).Scan(&id)
	if err != nil {
		if s.d.isUniqueViolation(err) {
			return nil, ErrUsernameExists
		}
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return s.GetUserByID(id)
}

// ReplaceUserInvitation rotates the row identity as well as the secret. Two
// replacements of the same old invitation cannot both succeed, and acceptance
// races against this update atomically on both supported databases.
func (s *Store) ReplaceUserInvitation(oldID string, replacement UserInvitation, tokenHash string) (*UserInvitation, error) {
	var inv UserInvitation
	err := s.db.QueryRow(`UPDATE user_invitations SET id = ?, token_hash = ?, created_by = ?, created_at = ?, expires_at = ? WHERE id = ? AND NOT EXISTS (SELECT 1 FROM users WHERE users.username = user_invitations.username) RETURNING id, username, role, created_by, created_at, expires_at`, replacement.ID, tokenHash, replacement.CreatedBy, replacement.CreatedAt, replacement.ExpiresAt, oldID).Scan(&inv.ID, &inv.Username, &inv.Role, &inv.CreatedBy, &inv.CreatedAt, &inv.ExpiresAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &inv, nil
}
