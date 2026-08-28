// Package dbtest provides an env-aware store constructor for tests. When
// SHINYHUB_TEST_POSTGRES_DSN is set, New returns a Postgres-backed store in an
// isolated per-test database; otherwise it returns an in-memory SQLite store.
//
// Both backends clone a template that is migrated once per test binary instead
// of replaying every migration per test. Under the race detector a migration
// replay through modernc's transpiled SQLite costs seconds per fixture, and
// packages build hundreds of fixtures, so the clone is what keeps the race jobs
// short. Tests that assert migration behaviour open their own stores and
// migrate them for real.
package dbtest

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"strings"
	"sync"
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
func New(t testing.TB) *db.Store {
	t.Helper()
	adminDSN := os.Getenv(dsnEnv)
	if adminDSN == "" {
		return newSQLite(t)
	}
	store, _ := newPostgres(t, adminDSN)
	return store
}

// sqliteTemplate holds the serialized image of a freshly migrated in-memory
// database, built once per test binary. Every SQLite fixture deserializes its
// own copy, so tests never share state through it.
var sqliteTemplate struct {
	once  sync.Once
	image []byte
	err   error
}

func sqliteTemplateImage() ([]byte, error) {
	sqliteTemplate.once.Do(func() {
		sqliteTemplate.image, sqliteTemplate.err = buildSQLiteTemplate()
	})
	return sqliteTemplate.image, sqliteTemplate.err
}

func buildSQLiteTemplate() ([]byte, error) {
	store, err := db.Open(":memory:")
	if err != nil {
		return nil, fmt.Errorf("open: %w", err)
	}
	defer store.Close()
	if err := store.Migrate(); err != nil {
		return nil, fmt.Errorf("migrate: %w", err)
	}
	image, err := store.SerializeSQLite(context.Background())
	if err != nil {
		return nil, err
	}
	return image, nil
}

func newSQLite(t testing.TB) *db.Store {
	t.Helper()
	image, err := sqliteTemplateImage()
	if err != nil {
		t.Fatalf("build sqlite template: %v", err)
	}
	store, err := db.Open(":memory:")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if err := store.DeserializeSQLite(context.Background(), image); err != nil {
		t.Fatalf("load sqlite template: %v", err)
	}
	return store
}

// postgresTemplateLockKey serializes template creation across every test
// process sharing one Postgres server (go test runs packages in parallel).
// Distinct from the key Migrate itself takes.
const postgresTemplateLockKey int64 = 7223151986

// postgresTemplate memoizes, per test binary, the name of the migrated template
// database that per-test databases are cloned from.
var postgresTemplate struct {
	once sync.Once
	name string
	err  error
}

func postgresTemplateName(ctx context.Context, admin *sql.DB, adminDSN string) (string, error) {
	postgresTemplate.once.Do(func() {
		postgresTemplate.name, postgresTemplate.err = ensurePostgresTemplate(ctx, admin, adminDSN)
	})
	return postgresTemplate.name, postgresTemplate.err
}

// ensurePostgresTemplate returns the name of a database migrated by this
// binary's migration set, creating it when absent. The name embeds a digest of
// the migrations, so a changed migration set gets a new template rather than a
// stale one, and repeated runs against a long-lived server reuse the existing
// one. The template is built under a work-in-progress name and renamed into
// place only after Migrate succeeds, so an interrupted build can never leave a
// half-migrated database under the real name. A server-side advisory lock
// makes concurrent test processes take turns; the loser finds the winner's
// template already present.
func ensurePostgresTemplate(ctx context.Context, admin *sql.DB, adminDSN string) (string, error) {
	digest, err := db.MigrationsDigest()
	if err != nil {
		return "", fmt.Errorf("migrations digest: %w", err)
	}
	name := "shtest_tpl_" + digest[:16]

	conn, err := admin.Conn(ctx)
	if err != nil {
		return "", fmt.Errorf("acquire admin conn: %w", err)
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, `SELECT pg_advisory_lock($1)`, postgresTemplateLockKey); err != nil {
		return "", fmt.Errorf("template advisory lock: %w", err)
	}
	// The session lock would otherwise outlive this call on the pooled
	// connection and block every other test process until admin closes.
	defer conn.ExecContext(ctx, `SELECT pg_advisory_unlock($1)`, postgresTemplateLockKey) //nolint:errcheck

	var one int
	switch err := conn.QueryRowContext(ctx, `SELECT 1 FROM pg_database WHERE datname = $1`, name).Scan(&one); err {
	case nil:
		return name, nil
	case sql.ErrNoRows:
	default:
		return "", fmt.Errorf("probe template %s: %w", name, err)
	}

	// A process killed mid-build leaves its work-in-progress database behind on
	// a long-lived server; a later process that draws the same pid takes it over.
	wip := fmt.Sprintf("%s_wip_%d", name, os.Getpid())
	if _, err := conn.ExecContext(ctx, `DROP DATABASE IF EXISTS `+wip); err != nil {
		return "", fmt.Errorf("drop stale template %s: %w", wip, err)
	}
	if _, err := conn.ExecContext(ctx, `CREATE DATABASE `+wip); err != nil {
		return "", fmt.Errorf("create template %s: %w", wip, err)
	}
	if err := migratePostgresTemplate(swapDatabase(adminDSN, wip)); err != nil {
		_, _ = conn.ExecContext(ctx, `DROP DATABASE IF EXISTS `+wip)
		return "", fmt.Errorf("migrate template %s: %w", wip, err)
	}
	// RENAME refuses while any session is still attached. The migrating pool is
	// closed, but its backends can take a moment to exit, so retry briefly.
	var renameErr error
	for attempt := 0; attempt < 20; attempt++ {
		if _, renameErr = conn.ExecContext(ctx, `ALTER DATABASE `+wip+` RENAME TO `+name); renameErr == nil {
			return name, nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	_, _ = conn.ExecContext(ctx, `DROP DATABASE IF EXISTS `+wip)
	return "", fmt.Errorf("rename template %s to %s: %w", wip, name, renameErr)
}

// migratePostgresTemplate runs the real migrations against the work-in-progress
// template and closes every connection to it, which RENAME requires.
func migratePostgresTemplate(dsn string) error {
	store, err := db.Open(dsn)
	if err != nil {
		return err
	}
	defer store.Close()
	return store.Migrate()
}

func newPostgres(t testing.TB, adminDSN string) (*db.Store, string) {
	t.Helper()
	ctx := context.Background()
	// Create a uniquely-named database on the admin connection, then open it.
	admin, err := sql.Open("pgx", adminDSN)
	if err != nil {
		t.Fatalf("open admin postgres: %v", err)
	}
	template, err := postgresTemplateName(ctx, admin, adminDSN)
	if err != nil {
		_ = admin.Close()
		t.Fatalf("postgres template: %v", err)
	}
	dbName := fmt.Sprintf("shtest_%d_%d", time.Now().UnixNano(), counter.Add(1))
	// dbName is composed of digits + a fixed prefix, so it is a safe identifier.
	// Cloning the template yields a fully migrated database, ledger included,
	// so the store below needs no Migrate call.
	if _, err := admin.Exec(`CREATE DATABASE ` + dbName + ` TEMPLATE ` + template); err != nil {
		_ = admin.Close()
		t.Fatalf("create test database: %v", err)
	}
	// Register the drop cleanup immediately after CREATE DATABASE so that any
	// subsequent failure (Open, or inside the test) still drops the database.
	// t.Cleanup runs LIFO, so this runs LAST (after the store is closed by the
	// cleanup registered below).
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
func NewPostgres(t testing.TB) (*db.Store, string) {
	t.Helper()
	adminDSN := os.Getenv(dsnEnv)
	if adminDSN == "" {
		t.Skip("SHINYHUB_TEST_POSTGRES_DSN not set; skipping Postgres-only test")
	}
	// Per-test isolation rewrites the DSN's database name via swapDatabase, which
	// only understands URL-form DSNs (postgres://host/db). A keyword DSN
	// (host=... dbname=...) would silently keep the admin database, so child
	// processes handed this DSN would share the admin DB instead of the isolated
	// one. Fail loudly rather than misbehave.
	if !strings.Contains(adminDSN, "://") {
		t.Fatalf("%s must be a URL-form DSN (postgres://...); keyword DSNs are not supported by per-test database isolation", dsnEnv)
	}
	return newPostgres(t, adminDSN)
}

// RequirePostgres skips the test unless a Postgres DSN is configured. Use for
// Postgres-specific assertions (advisory locks, type behavior).
func RequirePostgres(t testing.TB) {
	t.Helper()
	if os.Getenv(dsnEnv) == "" {
		t.Skipf("%s not set; skipping Postgres-specific test", dsnEnv)
	}
}

// SkipIfPostgres skips a SQLite-only test (pragmas, VACUUM INTO, legacy adoption)
// when running against Postgres.
func SkipIfPostgres(t testing.TB) {
	t.Helper()
	if os.Getenv(dsnEnv) != "" {
		t.Skip("SQLite-only test; skipping under Postgres")
	}
}
