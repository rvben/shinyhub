package db

import (
	"database/sql"
	"encoding/json"
	"errors"
	"slices"
	"time"
)

type TokenRevocation struct {
	JTI       string
	UserID    int64
	ExpiresAt time.Time
}
type BrowserLogoutCode struct {
	Hash     string
	UserIDs  []int64
	NextPath string
}

func revokeBrowserTokens(tx *boundTx, tokens []TokenRevocation) (int, error) {
	count := 0
	for _, token := range tokens {
		res, err := tx.Exec(`INSERT INTO revoked_tokens (jti,user_id,expires_at)
   SELECT ?, id, ? FROM users WHERE id = ?
   ON CONFLICT(jti) DO UPDATE SET expires_at = CASE WHEN revoked_tokens.expires_at < excluded.expires_at THEN excluded.expires_at ELSE revoked_tokens.expires_at END`, token.JTI, token.ExpiresAt.Unix(), token.UserID)
		if err != nil {
			return 0, err
		}
		n, err := res.RowsAffected()
		if err != nil {
			return 0, err
		}
		count += int(n)
	}
	return count, nil
}

// RevokeBrowserTokens commits all credentials and the optional app handoff together.
func (s *Store) RevokeBrowserTokens(tokens []TokenRevocation, code *BrowserLogoutCode) (int, error) {
	if len(tokens) == 0 && code == nil {
		return 0, nil
	}
	tx, err := s.db.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	if _, err = tx.Exec(`DELETE FROM revoked_tokens WHERE expires_at < ?`, time.Now().Unix()); err != nil {
		return 0, err
	}
	count, err := revokeBrowserTokens(tx, tokens)
	if err != nil {
		return 0, err
	}
	if code != nil {
		if _, err = tx.Exec(`DELETE FROM browser_logout_codes WHERE created_at < ` + s.d.nowMinusSeconds(120)); err != nil {
			return 0, err
		}
		ids, err := json.Marshal(code.UserIDs)
		if err != nil {
			return 0, err
		}
		if _, err = tx.Exec(`INSERT INTO browser_logout_codes(code_hash,kind,user_ids,next_path) VALUES(?,'app',?,?)`, code.Hash, string(ids), code.NextPath); err != nil {
			return 0, err
		}
	}
	if err = tx.Commit(); err != nil {
		return 0, err
	}
	return count, nil
}

func (s *Store) BrowserLogoutCodePending(hash string) (bool, error) {
	var exists int
	err := s.db.QueryRow(`SELECT CASE WHEN EXISTS(SELECT 1 FROM browser_logout_codes WHERE code_hash=? AND kind='app' AND created_at >= `+s.d.nowMinusSeconds(120)+`) THEN 1 ELSE 0 END`, hash).Scan(&exists)
	return exists != 0, err
}

// FinishAppBrowserLogout is atomic, so a failed cleanup leaves its code retryable.
// Only tokens belonging to the authenticated logout's recorded scope are revoked.
func (s *Store) FinishAppBrowserLogout(hash, completion string, tokens []TokenRevocation) ([]int64, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	var raw, nextPath string
	err = tx.QueryRow(`DELETE FROM browser_logout_codes WHERE code_hash=? AND kind='app' AND created_at >= `+s.d.nowMinusSeconds(120)+` RETURNING user_ids,next_path`, hash).Scan(&raw, &nextPath)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	var ids []int64
	if err = json.Unmarshal([]byte(raw), &ids); err != nil {
		return nil, err
	}
	var scoped []TokenRevocation
	for _, token := range tokens {
		if slices.Contains(ids, token.UserID) {
			scoped = append(scoped, token)
		}
	}
	if _, err = revokeBrowserTokens(tx, scoped); err != nil {
		return nil, err
	}
	if _, err = tx.Exec(`DELETE FROM browser_logout_codes WHERE created_at < ` + s.d.nowMinusSeconds(120)); err != nil {
		return nil, err
	}
	if _, err = tx.Exec(`INSERT INTO browser_logout_codes(code_hash,kind,user_ids,parent_hash,next_path) VALUES(?,'complete',?,?,?)`, completion, raw, hash, nextPath); err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return ids, nil
}

// ConsumeBrowserLogoutCompletion returns only the handoff's server-recorded
// local return path, never a destination supplied by the bridge request.
func (s *Store) ConsumeBrowserLogoutCompletion(hash, parent string) (string, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	if _, err = tx.Exec(`DELETE FROM browser_logout_codes WHERE created_at < ` + s.d.nowMinusSeconds(120)); err != nil {
		return "", err
	}
	var next string
	err = tx.QueryRow(`DELETE FROM browser_logout_codes WHERE code_hash=? AND parent_hash=? AND kind='complete' AND created_at >= `+s.d.nowMinusSeconds(120)+` RETURNING next_path`, hash, parent).Scan(&next)
	if errors.Is(err, sql.ErrNoRows) {
		if commitErr := tx.Commit(); commitErr != nil {
			return "", commitErr
		}
		return "", ErrNotFound
	}
	if err != nil {
		return "", err
	}
	if err = tx.Commit(); err != nil {
		return "", err
	}
	return next, nil
}
