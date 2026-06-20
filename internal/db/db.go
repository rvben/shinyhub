package db

import (
	"context"
	"database/sql"
	"embed"
	"fmt"
	"io/fs"
	"log/slog"
	"math/rand/v2"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
	_ "modernc.org/sqlite"
)

//go:embed migrations/sqlite/*.sql migrations/postgres/*.sql
var migrationsFS embed.FS

var migrationFileRE = regexp.MustCompile(`^(\d+)_.*\.sql$`)

type migration struct {
	version int
	name    string
	sql     string
}

// loadMigrations parses every embedded migrations/<subdir>/NNN_*.sql file into
// an ordered slice keyed by the numeric prefix. Filenames that do not match the
// NNN_name.sql convention are a build-time mistake and fail loudly.
func loadMigrations(subdir string) ([]migration, error) {
	dir := path.Join("migrations", subdir)
	entries, err := fs.ReadDir(migrationsFS, dir)
	if err != nil {
		return nil, fmt.Errorf("read migrations dir %q: %w", dir, err)
	}
	var ms []migration
	seen := map[int]string{}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		m := migrationFileRE.FindStringSubmatch(e.Name())
		if m == nil {
			return nil, fmt.Errorf("migration file %q does not match NNN_name.sql", e.Name())
		}
		v, err := strconv.Atoi(m[1])
		if err != nil {
			return nil, fmt.Errorf("migration %q: bad version: %w", e.Name(), err)
		}
		if prev, dup := seen[v]; dup {
			return nil, fmt.Errorf("duplicate migration version %d: %q and %q", v, prev, e.Name())
		}
		seen[v] = e.Name()
		body, err := migrationsFS.ReadFile(path.Join(dir, e.Name()))
		if err != nil {
			return nil, fmt.Errorf("read migration %q: %w", e.Name(), err)
		}
		ms = append(ms, migration{version: v, name: e.Name(), sql: string(body)})
	}
	sort.Slice(ms, func(i, j int) bool { return ms[i].version < ms[j].version })
	return ms, nil
}

// migrationsSubdir returns the dialect-specific migration subdirectory.
func (s *Store) migrationsSubdir() string {
	if _, isPG := s.d.(pgDialect); isPG {
		return "postgres"
	}
	return "sqlite"
}

type Store struct {
	db *boundDB
	d  dialect

	// auditErrHook, if set, is invoked when an audit-event write fails. It lets
	// the server surface dropped audit events as a metric without coupling the
	// db package to the metrics registry. Never nil-checked off the hot path:
	// audit writes are infrequent relative to request volume.
	auditErrHook func()
}

// SetAuditErrorHook registers a callback invoked whenever LogAuditEvent fails to
// persist an event. The server wires this to a metrics counter so a persistent
// audit-write failure (e.g. disk full) can be alerted on rather than silently
// dropping the compliance trail.
func (s *Store) SetAuditErrorHook(hook func()) {
	s.auditErrHook = hook
}

// fileDBMaxConns caps the connection pool for file-backed databases. WAL lets
// many readers run concurrently while SQLite serializes the single writer
// (busy_timeout makes a contended writer wait instead of erroring), so a small
// pool improves read concurrency without risking corruption.
const fileDBMaxConns = 8

// slowQueryThreshold is the latency above which a DB op is always logged.
// Faster ops are sampled at slowQuerySampleRate to bound log volume.
const (
	slowQueryThreshold  = 200 * time.Millisecond
	slowQuerySampleRate = 0.01
)

// isMemoryDSN reports whether the DSN names an in-memory database. Each
// connection to ":memory:" is an independent database, so a pool would hand
// out connections to different empty databases; memory DSNs must stay at a
// single connection.
func isMemoryDSN(dsn string) bool {
	return strings.Contains(dsn, ":memory:") || strings.Contains(dsn, "mode=memory")
}

// withPragmas appends the durability/concurrency pragmas modernc applies on
// every pooled connection. Setting them via the DSN (rather than a one-shot
// PRAGMA Exec) guarantees every connection in the pool is configured, not just
// the first. journal_mode=WAL is omitted for memory DBs, which do not support
// it.
func withPragmas(dsn string) string {
	pragmas := []string{
		"_pragma=busy_timeout(5000)",
		"_pragma=foreign_keys(1)",
		"_pragma=synchronous(NORMAL)",
	}
	if !isMemoryDSN(dsn) {
		pragmas = append(pragmas, "_pragma=journal_mode(WAL)")
	}
	sep := "?"
	if strings.Contains(dsn, "?") {
		sep = "&"
	}
	return dsn + sep + strings.Join(pragmas, "&")
}

// IsPostgresDSN reports whether the DSN selects the Postgres backend. Any other
// DSN (file path, :memory:, file: URI) is SQLite, preserving existing behavior.
// This is the same dispatch check used by Open to pick the backend.
func IsPostgresDSN(dsn string) bool {
	return strings.HasPrefix(dsn, "postgres://") || strings.HasPrefix(dsn, "postgresql://")
}

func Open(dsn string) (*Store, error) {
	if IsPostgresDSN(dsn) {
		return openPostgres(dsn)
	}
	return openSQLite(dsn)
}

func openSQLite(dsn string) (*Store, error) {
	memory := isMemoryDSN(dsn)
	raw, err := sql.Open("sqlite", withPragmas(dsn))
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}
	if memory {
		raw.SetMaxOpenConns(1)
	} else {
		raw.SetMaxOpenConns(fileDBMaxConns)
		raw.SetMaxIdleConns(fileDBMaxConns)
		raw.SetConnMaxIdleTime(5 * time.Minute)
	}
	// Verify the pragmas took effect on a real connection. A silent failure
	// here (e.g. WAL rejected on a read-only volume) would otherwise surface
	// much later as data-integrity or lock-contention bugs.
	var fk int
	if err := raw.QueryRow("PRAGMA foreign_keys").Scan(&fk); err != nil {
		_ = raw.Close()
		return nil, fmt.Errorf("verify foreign_keys pragma: %w", err)
	}
	if fk != 1 {
		_ = raw.Close()
		return nil, fmt.Errorf("foreign_keys pragma not enabled (got %d)", fk)
	}
	d := sqliteDialect{}
	return &Store{db: &boundDB{real: raw, d: d}, d: d}, nil
}

// pgMaxConns bounds the Postgres pool. The control plane issues a modest query
// rate; a small pool keeps connection use predictable while leaving headroom for
// the watcher/scheduler loops and request handlers.
const pgMaxConns = 16

func openPostgres(dsn string) (*Store, error) {
	raw, err := sql.Open("pgx", dsn)
	if err != nil {
		return nil, fmt.Errorf("open postgres: %w", err)
	}
	raw.SetMaxOpenConns(pgMaxConns)
	raw.SetMaxIdleConns(pgMaxConns)
	raw.SetConnMaxIdleTime(5 * time.Minute)
	d := pgDialect{}
	return &Store{db: &boundDB{real: raw, d: d}, d: d}, nil
}

// timed records the latency of a DB op. Slow ops (over slowQueryThreshold) are
// always logged at warn; the rest are sampled at slowQuerySampleRate and
// logged at debug so steady-state latency is observable without flooding logs.
// Usage: defer s.timed("GetAppBySlug")().
func (s *Store) timed(op string) func() {
	start := time.Now()
	return func() {
		d := time.Since(start)
		switch {
		case d >= slowQueryThreshold:
			slog.Warn("db: slow op", "op", op, "ms", d.Milliseconds())
		case rand.Float64() < slowQuerySampleRate:
			slog.Debug("db: op latency", "op", op, "ms", d.Milliseconds())
		}
	}
}

// legacyBaselineVersion is the highest schema version that any pre-ledger
// binary ever produced. The original non-versioned runner shipped migrations
// 001-012; the schema_migrations ledger and every migration after 012 were
// added later. A pre-ledger database therefore has schema through this
// version and nothing beyond it, so baselining records 1..legacyBaselineVersion
// as applied without running them and lets the normal loop apply the rest.
const legacyBaselineVersion = 12

// Migrate applies every embedded migration that has not yet been recorded in
// the schema_migrations ledger. Each migration runs inside its own
// transaction; a failure aborts that migration without recording it and stops
// the run, so the schema never silently drifts.
//
// Pre-ledger databases (created by the original non-versioned runner, which
// always applied every migration all-or-nothing on boot) are baselined: the
// currently-embedded versions are recorded as already applied without being
// re-executed. A genuinely fresh database has no core tables and runs every
// migration from scratch.
func (s *Store) Migrate() error {
	defer s.timed("Migrate")()
	migrations, err := loadMigrations(s.migrationsSubdir())
	if err != nil {
		return err
	}
	if len(migrations) == 0 {
		return fmt.Errorf("no embedded migrations found")
	}

	if _, err := s.db.Exec(`CREATE TABLE IF NOT EXISTS schema_migrations (
		version    INTEGER PRIMARY KEY,
		name       TEXT NOT NULL,
		applied_at TEXT NOT NULL
	)`); err != nil {
		return fmt.Errorf("create schema_migrations: %w", err)
	}

	applied, err := s.appliedMigrations()
	if err != nil {
		return err
	}

	if len(applied) == 0 {
		if _, isSQLite := s.d.(sqliteDialect); isSQLite {
			legacy, err := s.hasLegacySchema()
			if err != nil {
				return err
			}
			if legacy {
				return s.adoptLegacySchema(migrations)
			}
		}
	}

	for _, m := range migrations {
		if applied[m.version] {
			continue
		}
		tx, err := s.db.Begin()
		if err != nil {
			return fmt.Errorf("migrate %s: begin: %w", m.name, err)
		}
		if _, err := tx.Exec(m.sql); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("migrate %s: %w", m.name, err)
		}
		if _, err := tx.Exec(
			`INSERT INTO schema_migrations (version, name, applied_at) VALUES (?, ?, ?)`,
			m.version, m.name, time.Now().UTC().Format(time.RFC3339)); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("migrate %s: record: %w", m.name, err)
		}
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("migrate %s: commit: %w", m.name, err)
		}
		slog.Info("migrations: applied", "version", m.version, "name", m.name)
	}
	return nil
}

// adoptLegacySchema brings a pre-ledger database under ledger management.
// Migrations through legacyBaselineVersion predate the ledger and are already
// present, so they are recorded as applied without running. Later migrations
// run normally; an ALTER whose column already exists (a database whose ledger
// was lost after a newer upgrade) is treated as already-satisfied. Any other
// error aborts so the schema never silently drifts.
func (s *Store) adoptLegacySchema(migrations []migration) error {
	now := time.Now().UTC().Format(time.RFC3339)
	baselined, applied := 0, 0
	for _, m := range migrations {
		if m.version <= legacyBaselineVersion {
			if err := s.recordMigration(m, now); err != nil {
				return fmt.Errorf("baseline %s: %w", m.name, err)
			}
			baselined++
			continue
		}
		tx, err := s.db.Begin()
		if err != nil {
			return fmt.Errorf("adopt %s: begin: %w", m.name, err)
		}
		if _, err := tx.Exec(m.sql); err != nil {
			_ = tx.Rollback()
			if !isColumnAlreadyPresent(err) {
				return fmt.Errorf("adopt %s: %w", m.name, err)
			}
			// Schema already present (ledger lost after a newer upgrade).
			if rerr := s.recordMigration(m, now); rerr != nil {
				return fmt.Errorf("adopt %s: record: %w", m.name, rerr)
			}
			continue
		}
		if _, err := tx.Exec(
			`INSERT INTO schema_migrations (version, name, applied_at) VALUES (?, ?, ?)`,
			m.version, m.name, now); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("adopt %s: record: %w", m.name, err)
		}
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("adopt %s: commit: %w", m.name, err)
		}
		applied++
	}
	slog.Info("migrations: adopted legacy database", "baselined", baselined, "applied", applied)
	return nil
}

// recordMigration marks a migration as applied without running it.
func (s *Store) recordMigration(m migration, appliedAt string) error {
	_, err := s.db.Exec(
		`INSERT INTO schema_migrations (version, name, applied_at) VALUES (?, ?, ?)`,
		m.version, m.name, appliedAt)
	return err
}

// isColumnAlreadyPresent matches the SQLite error for ADD COLUMN against a
// column that already exists. On the legacy adoption path it means the
// migration's schema is already present, so the migration is recorded as
// applied rather than treated as a failure.
func isColumnAlreadyPresent(err error) bool {
	return err != nil && strings.Contains(err.Error(), "duplicate column name")
}

// appliedMigrations returns the set of versions recorded in the ledger.
func (s *Store) appliedMigrations() (map[int]bool, error) {
	rows, err := s.db.Query(`SELECT version FROM schema_migrations`)
	if err != nil {
		return nil, fmt.Errorf("read schema_migrations: %w", err)
	}
	defer rows.Close()
	applied := map[int]bool{}
	for rows.Next() {
		var v int
		if err := rows.Scan(&v); err != nil {
			return nil, fmt.Errorf("scan schema_migrations: %w", err)
		}
		applied[v] = true
	}
	return applied, rows.Err()
}

// hasLegacySchema reports whether the database already contains core tables
// without a migration ledger, i.e. it was created by the original
// non-versioned runner and needs baselining rather than re-migration.
func (s *Store) hasLegacySchema() (bool, error) {
	var name string
	err := s.db.QueryRow(
		`SELECT name FROM sqlite_master WHERE type='table' AND name='users'`).Scan(&name)
	switch err {
	case nil:
		return true, nil
	case sql.ErrNoRows:
		return false, nil
	default:
		return false, fmt.Errorf("probe legacy schema: %w", err)
	}
}

func (s *Store) Close() error {
	return s.db.Close()
}

// rawDB returns the underlying *sql.DB for SQLite-only internals that genuinely
// need a raw connection (e.g. BEGIN IMMEDIATE on a dedicated conn).
func (s *Store) rawDB() *sql.DB { return s.db.real }

// SchemaVersion returns the highest migration version recorded in the
// schema_migrations ledger, or 0 if the database has no ledger yet.
func (s *Store) SchemaVersion() (int, error) {
	var v sql.NullInt64
	err := s.db.QueryRow(`SELECT MAX(version) FROM schema_migrations`).Scan(&v)
	switch {
	case err == nil && v.Valid:
		return int(v.Int64), nil
	case err == nil:
		return 0, nil
	case strings.Contains(err.Error(), "no such table"):
		return 0, nil
	default:
		return 0, fmt.Errorf("read schema version: %w", err)
	}
}

// LatestSchemaVersion returns the highest migration version embedded in this
// binary. A backup whose recorded schema version exceeds this value was taken
// by a newer build and cannot be safely restored by this one.
// Backups are SQLite-only, so the SQLite ledger is the reference.
func LatestSchemaVersion() (int, error) {
	return latestEmbeddedVersion("sqlite")
}

// latestEmbeddedVersion returns the highest migration version embedded in this
// binary for the given dialect subdirectory.
func latestEmbeddedVersion(subdir string) (int, error) {
	ms, err := loadMigrations(subdir)
	if err != nil {
		return 0, err
	}
	max := 0
	for _, m := range ms {
		if m.version > max {
			max = m.version
		}
	}
	return max, nil
}

// VerifySchemaCompatibility returns an error if the database's recorded schema
// version is newer than the highest migration this binary embeds, which means
// the database was migrated by a newer build. Running an older binary against
// such a database is unsafe: this code does not understand columns or tables
// added by the newer migrations, risking silent corruption or panics. Migrate()
// only applies missing lower-numbered migrations and never removes higher ones,
// so call this after Migrate() to catch a downgrade before serving traffic.
func (s *Store) VerifySchemaCompatibility() error {
	dbVer, err := s.SchemaVersion()
	if err != nil {
		return err
	}
	binVer, err := latestEmbeddedVersion(s.migrationsSubdir())
	if err != nil {
		return err
	}
	if dbVer > binVer {
		return fmt.Errorf("database schema version %d was created by a newer shinyhub build (this binary supports up to version %d); downgrade is not supported - upgrade the server or restore from a compatible backup", dbVer, binVer)
	}
	return nil
}

// BackupTo writes a transactionally consistent copy of the database to dest
// using SQLite's "VACUUM INTO". It is safe to call while the server is running:
// the snapshot reflects a single point-in-time and is itself a defragmented,
// single-file database with no WAL/SHM sidecars.
// On non-SQLite backends this returns an error; use pg_dump for Postgres backups.
func (s *Store) BackupTo(dest string) error {
	defer s.timed("BackupTo")()
	if _, isSQLite := s.d.(sqliteDialect); !isSQLite {
		return fmt.Errorf("BackupTo is only supported on SQLite (Postgres backups use pg_dump)")
	}
	quoted := "'" + strings.ReplaceAll(dest, "'", "''") + "'"
	if _, err := s.rawDB().Exec("VACUUM INTO " + quoted); err != nil {
		return fmt.Errorf("vacuum into %s: %w", dest, err)
	}
	return nil
}

// PingContext verifies DB connectivity.
func (s *Store) PingContext(ctx context.Context) error {
	return s.db.PingContext(ctx)
}

// DB returns the querier for this store. It is exposed for test helpers that
// need to insert rows directly without going through query methods.
func (s *Store) DB() Execer {
	return s.db
}
