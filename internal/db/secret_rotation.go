package db

import (
	"database/sql"
	"errors"
	"fmt"
)

// RotateSecretsTx re-encrypts every at-rest secret in a single transaction: each
// app_env_vars row whose is_secret is true, and the worker CA private key if a
// CA has been provisioned. reencryptEnv and reencryptCA receive the stored
// ciphertext and must return it re-encrypted under the new key; any error rolls
// the whole rotation back, so the store is never left half old / half new (a
// state that would be unrecoverable, since the old key is gone after the
// operator switches auth.secret). Crypto lives in the caller's callbacks; this
// method only moves bytes atomically. Returns the number of env secrets rotated
// and whether a worker CA was rotated.
func (s *Store) RotateSecretsTx(reencryptEnv, reencryptCA func([]byte) ([]byte, error)) (envRotated int, caRotated bool, err error) {
	tx, err := s.db.Begin()
	if err != nil {
		return 0, false, fmt.Errorf("begin: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck

	// Read every secret env var first (the cursor is closed before any UPDATE so
	// the single-connection SQLite driver does not deadlock on a write mid-scan).
	// is_secret is filtered in Go to stay dialect-agnostic about bool storage.
	type envRow struct {
		appID int64
		key   string
		val   []byte
	}
	rows, err := tx.Query(`SELECT app_id, key, value, is_secret FROM app_env_vars`)
	if err != nil {
		return 0, false, fmt.Errorf("list env vars: %w", err)
	}
	var secretsToRotate []envRow
	for rows.Next() {
		var r envRow
		// is_secret is an integer 0/1 column on both dialects; scan it as an int
		// (as the other accessors do) rather than a bool, which pgx rejects.
		var isSecret int
		if err := rows.Scan(&r.appID, &r.key, &r.val, &isSecret); err != nil {
			rows.Close()
			return 0, false, fmt.Errorf("scan env var: %w", err)
		}
		if isSecret != 0 {
			secretsToRotate = append(secretsToRotate, r)
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return 0, false, err
	}
	rows.Close()

	for _, r := range secretsToRotate {
		nv, rerr := reencryptEnv(r.val)
		if rerr != nil {
			return 0, false, fmt.Errorf("re-encrypt env %d/%s: %w", r.appID, r.key, rerr)
		}
		if _, err := tx.Exec(
			`UPDATE app_env_vars SET value = ? WHERE app_id = ? AND key = ?`,
			nv, r.appID, r.key,
		); err != nil {
			return 0, false, fmt.Errorf("update env %d/%s: %w", r.appID, r.key, err)
		}
	}

	// Worker CA private key (single row), if present.
	var caEnc []byte
	caErr := tx.QueryRow(`SELECT key_pem_enc FROM cp_worker_ca WHERE role = ?`, workerCARole).Scan(&caEnc)
	switch {
	case errors.Is(caErr, sql.ErrNoRows):
		// No CA provisioned; nothing to rotate.
	case caErr != nil:
		return 0, false, fmt.Errorf("read worker ca: %w", caErr)
	default:
		nca, rerr := reencryptCA(caEnc)
		if rerr != nil {
			return 0, false, fmt.Errorf("re-encrypt worker ca: %w", rerr)
		}
		if _, err := tx.Exec(
			`UPDATE cp_worker_ca SET key_pem_enc = ? WHERE role = ?`,
			nca, workerCARole,
		); err != nil {
			return 0, false, fmt.Errorf("update worker ca: %w", err)
		}
		caRotated = true
	}

	if err := tx.Commit(); err != nil {
		return 0, false, fmt.Errorf("commit: %w", err)
	}
	return len(secretsToRotate), caRotated, nil
}
