package db

import (
	"context"
	"database/sql"
	"embed"
	"fmt"
	"io/fs"
	"log/slog"
	"path"
	"regexp"
	"sort"
	"strconv"
	"time"

	_ "modernc.org/sqlite"
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

var migrationFileRE = regexp.MustCompile(`^(\d+)_.*\.sql$`)

type migration struct {
	version int
	name    string
	sql     string
}

// loadMigrations parses every embedded migrations/NNN_*.sql file into an
// ordered slice keyed by the numeric prefix. Filenames that do not match the
// NNN_name.sql convention are a build-time mistake and fail loudly.
func loadMigrations() ([]migration, error) {
	entries, err := fs.ReadDir(migrationsFS, "migrations")
	if err != nil {
		return nil, fmt.Errorf("read migrations dir: %w", err)
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
		body, err := migrationsFS.ReadFile(path.Join("migrations", e.Name()))
		if err != nil {
			return nil, fmt.Errorf("read migration %q: %w", e.Name(), err)
		}
		ms = append(ms, migration{version: v, name: e.Name(), sql: string(body)})
	}
	sort.Slice(ms, func(i, j int) bool { return ms[i].version < ms[j].version })
	return ms, nil
}

type Store struct {
	db *sql.DB
}

func Open(dsn string) (*Store, error) {
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}
	db.SetMaxOpenConns(1)
	if _, err := db.Exec("PRAGMA foreign_keys = ON"); err != nil {
		return nil, fmt.Errorf("enable foreign keys: %w", err)
	}
	return &Store{db: db}, nil
}

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
	migrations, err := loadMigrations()
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
		legacy, err := s.hasLegacySchema()
		if err != nil {
			return err
		}
		if legacy {
			// The old runner left a fully-migrated DB without a ledger.
			// Adopt it: record the embedded set as applied, run nothing.
			now := time.Now().UTC().Format(time.RFC3339)
			tx, err := s.db.Begin()
			if err != nil {
				return fmt.Errorf("baseline begin: %w", err)
			}
			for _, m := range migrations {
				if _, err := tx.Exec(
					`INSERT INTO schema_migrations (version, name, applied_at) VALUES (?, ?, ?)`,
					m.version, m.name, now); err != nil {
					_ = tx.Rollback()
					return fmt.Errorf("baseline record %s: %w", m.name, err)
				}
			}
			if err := tx.Commit(); err != nil {
				return fmt.Errorf("baseline commit: %w", err)
			}
			slog.Info("migrations: baselined existing database", "versions", len(migrations))
			return nil
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

// PingContext verifies DB connectivity.
func (s *Store) PingContext(ctx context.Context) error {
	return s.db.PingContext(ctx)
}

// DB returns the underlying *sql.DB. It is exposed for test helpers that need
// to insert rows directly without going through query methods.
func (s *Store) DB() *sql.DB {
	return s.db
}
