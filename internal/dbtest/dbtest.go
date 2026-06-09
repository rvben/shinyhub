// Package dbtest provides an env-aware store constructor for tests. When
// SHINYHUB_TEST_POSTGRES_DSN is set, New returns a Postgres-backed store in an
// isolated per-test database; otherwise it returns an in-memory SQLite store.
package dbtest

import (
	"database/sql"
	"fmt"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/rvben/shinyhub/internal/db"
)

const dsnEnv = "SHINYHUB_TEST_POSTGRES_DSN"

var counter atomic.Int64

// New returns a migrated store. SQLite (:memory:) by default; an isolated
// Postgres database when SHINYHUB_TEST_POSTGRES_DSN is set. The store (and any
// Postgres database it created) is closed/dropped on test cleanup.
func New(t *testing.T) *db.Store {
	t.Helper()
	adminDSN := os.Getenv(dsnEnv)
	if adminDSN == "" {
		return newSQLite(t)
	}
	store, _ := newPostgres(t, adminDSN)
	return store
}

func newSQLite(t *testing.T) *db.Store {
	t.Helper()
	store, err := db.Open(":memory:")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := store.Migrate(); err != nil {
		t.Fatalf("migrate sqlite: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func newPostgres(t *testing.T, adminDSN string) (*db.Store, string) {
	t.Helper()
	// Create a uniquely-named database on the admin connection, then open it.
	admin, err := sql.Open("pgx", adminDSN)
	if err != nil {
		t.Fatalf("open admin postgres: %v", err)
	}
	dbName := fmt.Sprintf("shtest_%d_%d", time.Now().UnixNano(), counter.Add(1))
	// dbName is composed of digits + a fixed prefix, so it is a safe identifier.
	if _, err := admin.Exec(`CREATE DATABASE ` + dbName); err != nil {
		_ = admin.Close()
		t.Fatalf("create test database: %v", err)
	}
	// Register the drop cleanup immediately after CREATE DATABASE so that any
	// subsequent failure (Open, Migrate, or inside the test) still drops the
	// database. t.Cleanup runs LIFO, so this runs LAST (after the store is
	// closed by the cleanup registered below).
	t.Cleanup(func() {
		// Terminate stragglers, then drop. Best-effort.
		_, _ = admin.Exec(`SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE datname = $1`, dbName)
		_, _ = admin.Exec(`DROP DATABASE IF EXISTS ` + dbName)
		_ = admin.Close()
	})

	testDSN := swapDatabase(adminDSN, dbName)
	store, err := db.Open(testDSN)
	if err != nil {
		t.Fatalf("open test postgres: %v", err)
	}
	// Register store.Close before the drop cleanup (LIFO order ensures the
	// store is closed before its database is dropped).
	t.Cleanup(func() { _ = store.Close() })

	if err := store.Migrate(); err != nil {
		t.Fatalf("migrate postgres: %v", err)
	}
	return store, testDSN
}

// swapDatabase replaces the path component (database name) of a postgres DSN.
func swapDatabase(dsn, name string) string {
	// postgres://user:pass@host:port/dbname?query  -> swap dbname
	q := ""
	if i := strings.IndexByte(dsn, '?'); i >= 0 {
		q = dsn[i:]
		dsn = dsn[:i]
	}
	if i := strings.LastIndexByte(dsn, '/'); i >= 0 {
		dsn = dsn[:i+1] + name
	}
	return dsn + q
}

// NewPostgres returns a migrated, isolated Postgres store AND its DSN, so a test
// can hand the same database to child processes it spawns. It SKIPS the test
// when SHINYHUB_TEST_POSTGRES_DSN is unset - there is no SQLite fallback,
// because a two-process shared-lease test is meaningless on per-process
// in-memory SQLite.
func NewPostgres(t *testing.T) (*db.Store, string) {
	t.Helper()
	adminDSN := os.Getenv(dsnEnv)
	if adminDSN == "" {
		t.Skip("SHINYHUB_TEST_POSTGRES_DSN not set; skipping Postgres-only test")
	}
	return newPostgres(t, adminDSN)
}

// RequirePostgres skips the test unless a Postgres DSN is configured. Use for
// Postgres-specific assertions (advisory locks, type behavior).
func RequirePostgres(t *testing.T) {
	t.Helper()
	if os.Getenv(dsnEnv) == "" {
		t.Skipf("%s not set; skipping Postgres-specific test", dsnEnv)
	}
}

// SkipIfPostgres skips a SQLite-only test (pragmas, VACUUM INTO, legacy adoption)
// when running against Postgres.
func SkipIfPostgres(t *testing.T) {
	t.Helper()
	if os.Getenv(dsnEnv) != "" {
		t.Skip("SQLite-only test; skipping under Postgres")
	}
}
