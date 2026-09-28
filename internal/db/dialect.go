package db

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
)

// Execer is the subset of database/sql used by query methods and test helpers.
// The concrete value behind it rebinds `?` placeholders to the active dialect,
// so callers write SQLite-style `?` regardless of backend.
type Execer interface {
	Exec(query string, args ...any) (sql.Result, error)
	Query(query string, args ...any) (*sql.Rows, error)
	QueryRow(query string, args ...any) *sql.Row
}

// dialect supplies the few things that differ between SQLite and Postgres.
type dialect interface {
	// rebind rewrites `?` placeholders for the backend (identity on SQLite).
	rebind(query string) string
	// now renders the DB-clock "current timestamp" expression.
	now() string
	// nowPlusSeconds renders a DB-clock expression n seconds in the future.
	nowPlusSeconds(n int) string
	// nowMinusSeconds renders a DB-clock expression n seconds in the past.
	nowMinusSeconds(n int) string
	// nowText renders the DB-clock current time as an ISO8601 text string, for
	// columns stored as text on both backends (e.g. workers.last_heartbeat).
	// Both SQLite and Postgres produce "YYYY-MM-DD HH:MM:SS" format.
	nowText() string
	// nowEpoch renders the DB-clock current time as Unix epoch seconds (integer),
	// for columns stored as bigint epoch (e.g. replicas.updated_at).
	nowEpoch() string
	// nowEpochMillis renders the DB-clock current time as Unix epoch milliseconds
	// (integer). Used by the shared rate limiter so every instance buckets a
	// window from one time source (the database) rather than its own clock,
	// which clock skew between HA replicas could otherwise split.
	nowEpochMillis() string
	// beginWrite starts an eagerly-serialized write transaction. lockKey, when
	// non-zero, serializes a read-then-write invariant across transactions.
	// The returned writeTx (defined in bound.go) is satisfied by both the
	// Postgres boundTx and the SQLite dedicated-connection boundConn.
	beginWrite(ctx context.Context, db *sql.DB, lockKey int64) (writeTx, error)
	// isUniqueViolation reports a unique/primary-key constraint violation.
	isUniqueViolation(err error) bool
	// noLimit returns the sentinel integer to use as a LIMIT value meaning
	// "return all rows". SQLite uses -1; Postgres uses a large positive int
	// because Postgres rejects negative LIMIT values.
	noLimit() int
	// utcTimestampArg renders an instant as a bound argument comparable against
	// a DATETIME/timestamptz column holding UTC. SQLite stores those columns as
	// "YYYY-MM-DD HH:MM:SS" text and compares them lexicographically, so the
	// bound must match that shape exactly and carry no zone suffix. Postgres
	// parses the literal, and does so in the session's time zone unless the
	// literal says otherwise, so it gets an explicit UTC offset: without it a
	// server running in Europe/Amsterdam would silently shift every bound by an
	// hour or two.
	utcTimestampArg(t time.Time) string
	// slugAllowlistClause renders the "apps.slug is in the allowlist" predicate
	// against the single bound argument slugAllowlistArg produces. Unlike an
	// "IN (?,?,...)" fragment, it takes exactly one placeholder no matter how
	// many slugs the allowlist holds, so a large allowlist never approaches the
	// backend's per-statement bind-variable limit. Combine with AND/parentheses
	// as needed; callers must not call this for an empty allowlist (which means
	// "no apps") and should short-circuit before reaching SQL instead.
	slugAllowlistClause() string
	// slugAllowlistArg encodes a non-empty slug list into the single bound
	// argument slugAllowlistClause expects.
	slugAllowlistArg(slugs []string) (any, error)
}

type sqliteDialect struct{}

func (sqliteDialect) rebind(q string) string { return q }
func (sqliteDialect) now() string            { return "datetime('now')" }
func (sqliteDialect) nowPlusSeconds(n int) string {
	return "datetime('now', '+" + strconv.Itoa(n) + " seconds')"
}
func (sqliteDialect) nowMinusSeconds(n int) string {
	return "datetime('now', '-" + strconv.Itoa(n) + " seconds')"
}
func (sqliteDialect) nowText() string  { return "datetime('now')" }
func (sqliteDialect) nowEpoch() string { return "strftime('%s','now')" }
func (sqliteDialect) nowEpochMillis() string {
	return "(CAST(strftime('%s','now') AS INTEGER) * 1000)"
}

// beginWrite on SQLite takes the write lock up front with BEGIN IMMEDIATE on a
// dedicated connection, dodging the deferred-upgrade SQLITE_BUSY deadlock. The
// lockKey is irrelevant: a single writer is already serialized. The returned
// boundConn wraps the dedicated connection so query methods keep using `?`.
func (sqliteDialect) beginWrite(ctx context.Context, db *sql.DB, _ int64) (writeTx, error) {
	return beginImmediateSQLite(ctx, db, sqliteDialect{})
}

func (sqliteDialect) isUniqueViolation(err error) bool {
	return err != nil && (strings.Contains(err.Error(), "UNIQUE constraint failed") ||
		strings.Contains(err.Error(), "constraint failed: UNIQUE"))
}

// SQLite treats -1 as "no limit" in a LIMIT clause.
func (sqliteDialect) noLimit() int { return -1 }

func (sqliteDialect) utcTimestampArg(t time.Time) string {
	return t.UTC().Format("2006-01-02 15:04:05")
}

// json_each is a SQLite table-valued function that expands a JSON array bound
// as a single text argument into one row per element, so the allowlist stays
// a single placeholder regardless of length.
func (sqliteDialect) slugAllowlistClause() string {
	return "apps.slug IN (SELECT value FROM json_each(?))"
}

func (sqliteDialect) slugAllowlistArg(slugs []string) (any, error) {
	b, err := json.Marshal(slugs)
	if err != nil {
		return nil, fmt.Errorf("encode slug allowlist: %w", err)
	}
	return string(b), nil
}

type pgDialect struct{}

func (pgDialect) rebind(q string) string { return rebindQuery(q) }
func (pgDialect) now() string            { return "now()" }
func (pgDialect) nowPlusSeconds(n int) string {
	return "now() + make_interval(secs => " + strconv.Itoa(n) + ")"
}
func (pgDialect) nowMinusSeconds(n int) string {
	return "now() - make_interval(secs => " + strconv.Itoa(n) + ")"
}
func (pgDialect) nowText() string {
	return "to_char(now() at time zone 'UTC', 'YYYY-MM-DD HH24:MI:SS')"
}
func (pgDialect) nowEpoch() string       { return "extract(epoch from now())::bigint" }
func (pgDialect) nowEpochMillis() string { return "(extract(epoch from now()) * 1000)::bigint" }

// beginWrite on Postgres uses a normal transaction. MVCC plus the existing
// ON CONFLICT/unique handling cover single-row contention. For a read-then-write
// cross-row invariant the caller passes a non-zero lockKey and we take a
// transaction-scoped advisory lock so opposing transactions serialize here.
func (d pgDialect) beginWrite(ctx context.Context, db *sql.DB, lockKey int64) (writeTx, error) {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	if lockKey != 0 {
		if _, err := tx.ExecContext(ctx, "SELECT pg_advisory_xact_lock($1)", lockKey); err != nil {
			_ = tx.Rollback()
			return nil, err
		}
	}
	return &boundTx{tx: tx, d: d}, nil
}

func (pgDialect) isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return pgErr.Code == "23505" // unique_violation
	}
	return false
}

// Postgres rejects negative LIMIT values; use a large positive integer instead.
// 2^31-1 rows is the practical upper bound for any paged listing.
func (pgDialect) noLimit() int { return 1<<31 - 1 }

func (pgDialect) utcTimestampArg(t time.Time) string {
	return t.UTC().Format("2006-01-02 15:04:05+00:00")
}

// ANY over a bound array is Postgres's native membership test; the pgx stdlib
// driver encodes a []string argument as a Postgres array directly, so no
// pq.Array-style wrapping is needed.
func (pgDialect) slugAllowlistClause() string {
	return "apps.slug = ANY(?)"
}

func (pgDialect) slugAllowlistArg(slugs []string) (any, error) {
	return slugs, nil
}
