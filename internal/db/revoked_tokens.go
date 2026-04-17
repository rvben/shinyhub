package db

import (
	"database/sql"
	"errors"
	"time"
)

// RevokeToken records a JWT as revoked. After this call IsTokenRevoked returns
// true for the given jti until expiresAt passes and the row is pruned.
//
// Rows whose expires_at is already in the past are pruned opportunistically on
// every insert — a revoked token is useless once its signed expiry passes, so
// carrying it forward only bloats the index.
func (s *Store) RevokeToken(jti string, userID int64, expiresAt time.Time) error {
	now := time.Now().Unix()
	if _, err := s.db.Exec(`DELETE FROM revoked_tokens WHERE expires_at < ?`, now); err != nil {
		return err
	}
	_, err := s.db.Exec(
		`INSERT INTO revoked_tokens (jti, user_id, expires_at) VALUES (?, ?, ?)
		 ON CONFLICT(jti) DO NOTHING`,
		jti, userID, expiresAt.Unix(),
	)
	return err
}

// IsTokenRevoked reports whether the given jti is on the revocation list and
// still within its signed expiry window. Expired entries are ignored so the
// lookup stays cheap without requiring a separate cleanup job.
func (s *Store) IsTokenRevoked(jti string) (bool, error) {
	if jti == "" {
		return false, nil
	}
	var expiresAt int64
	err := s.db.QueryRow(
		`SELECT expires_at FROM revoked_tokens WHERE jti = ?`, jti,
	).Scan(&expiresAt)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return false, nil
		}
		return false, err
	}
	if expiresAt < time.Now().Unix() {
		return false, nil
	}
	return true, nil
}
