package db

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// CLIConnectRequestTTL bounds how long a registered `shinyhub connect`
// request stays approvable, matching the oauth_states nonce convention.
const CLIConnectRequestTTL = 600 * time.Second

// ErrCLIUserCodeExists is returned when a freshly generated user_code
// collides with another still-pending request. cli_connect_requests.user_code
// carries a UNIQUE index for exactly this reason: ConsumeCLIConnectRequest
// resolves a request by code alone, so an unenforced collision would let its
// DELETE ... RETURNING match two rows, approving one CLI as the wrong browser
// session while silently discarding the other's request. Callers should
// generate a new code and retry (see registerCLIConnectRequest in
// internal/api/auth.go).
var ErrCLIUserCodeExists = errors.New("cli connect user code already in use")

// CreateCLIConnectRequest registers a CLI's token hash and the human-typeable
// user_code the CLI is displaying, before any browser page is involved. The
// browser side later resolves a request only by the code a person typed in,
// never by anything the CLI put in a URL.
func (s *Store) CreateCLIConnectRequest(tokenHash, userCode, name string) error {
	// Keep the table bounded even when a terminal is closed before approval.
	// Cleanup is best-effort; registration itself still fails closed.
	s.db.Exec(`DELETE FROM cli_connect_requests WHERE created_at < ` + s.d.nowMinusSeconds(int(CLIConnectRequestTTL.Seconds()))) //nolint:errcheck
	_, err := s.db.Exec(
		`INSERT INTO cli_connect_requests (token_hash, user_code, name) VALUES (?, ?, ?)`,
		tokenHash, userCode, name,
	)
	if err != nil {
		// The table has two unique columns (token_hash is the primary key,
		// user_code carries its own unique index), so a generic
		// isUniqueViolation is not enough to know which one fired. Both
		// drivers name the offending column or index in the error text
		// (SQLite: "UNIQUE constraint failed: cli_connect_requests.user_code";
		// Postgres: the index name idx_cli_connect_requests_user_code_unique,
		// which we chose specifically so it is greppable here), so a substring
		// check is sufficient without a new dialect method.
		if s.d.isUniqueViolation(err) && strings.Contains(err.Error(), "user_code") {
			return ErrCLIUserCodeExists
		}
		return fmt.Errorf("create cli connect request: %w", err)
	}
	return nil
}

// CLIConnectRequestStatus reports what a waiting CLI should learn about its
// own token_hash, without ever revealing the user_code back to it (the code
// only ever needs to flow the other way: CLI screen to browser keyboard):
//
//   - "approved": an API key was minted for this hash (the browser side
//     resolved a matching pending request by user_code and consumed it).
//   - "pending": a registration exists and has not expired.
//   - "expired": a registration exists but is past CLIConnectRequestTTL. The
//     row itself is left for the existing lazy-cleanup convention rather
//     than deleted here, so a concurrent status poll and consume attempt see
//     a consistent view.
//   - "not_found": no registration exists for this hash at all. This is also
//     what an old CLI binary sees forever, since it never calls the register
//     endpoint before polling; it surfaces as a clear "unknown status" error
//     on the CLI side rather than any approval succeeding.
func (s *Store) CLIConnectRequestStatus(tokenHash string) (string, error) {
	approved, err := s.APIKeyHashExists(tokenHash)
	if err != nil {
		return "", err
	}
	if approved {
		return "approved", nil
	}

	var pendingCount int
	if err := s.db.QueryRow(
		`SELECT COUNT(*) FROM cli_connect_requests WHERE token_hash = ? AND created_at >= `+s.d.nowMinusSeconds(int(CLIConnectRequestTTL.Seconds())),
		tokenHash,
	).Scan(&pendingCount); err != nil {
		return "", fmt.Errorf("check pending cli connect request: %w", err)
	}
	if pendingCount > 0 {
		return "pending", nil
	}

	var totalCount int
	if err := s.db.QueryRow(
		`SELECT COUNT(*) FROM cli_connect_requests WHERE token_hash = ?`,
		tokenHash,
	).Scan(&totalCount); err != nil {
		return "", fmt.Errorf("check cli connect request: %w", err)
	}
	if totalCount > 0 {
		return "expired", nil
	}
	return "not_found", nil
}

// ConsumeCLIConnectRequest atomically validates and deletes a pending request
// by the code a signed-in person typed into the browser, returning the
// token_hash and name the CLI registered. A wrong code, an already-consumed
// code, and an expired code all fail identically with ErrNotFound: which one
// occurred is not a distinction worth leaking to the approving browser.
func (s *Store) ConsumeCLIConnectRequest(userCode string) (tokenHash, name string, err error) {
	row := s.db.QueryRow(
		`DELETE FROM cli_connect_requests
		 WHERE user_code = ? AND created_at >= `+s.d.nowMinusSeconds(int(CLIConnectRequestTTL.Seconds()))+`
		 RETURNING token_hash, name`,
		userCode,
	)
	if scanErr := row.Scan(&tokenHash, &name); scanErr != nil {
		if errors.Is(scanErr, sql.ErrNoRows) {
			return "", "", ErrNotFound
		}
		return "", "", fmt.Errorf("consume cli connect request: %w", scanErr)
	}
	return tokenHash, name, nil
}
