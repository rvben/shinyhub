package db

import (
	"context"
	"database/sql"
	_ "embed"
	"fmt"
	"strings"

	_ "modernc.org/sqlite"
)

//go:embed migrations/001_init.sql
var initSQL string

//go:embed migrations/002_hibernate_timeout.sql
var migration002SQL string

//go:embed migrations/003_app_members.sql
var migration003SQL string

//go:embed migrations/004_oauth_accounts.sql
var migration004SQL string

//go:embed migrations/005_app_members_role.sql
var migration005SQL string

//go:embed migrations/006_audit.sql
var migration006SQL string

//go:embed migrations/007_resource_limits.sql
var migration007SQL string

//go:embed migrations/008_revoked_tokens.sql
var migration008SQL string

//go:embed migrations/009_app_env_vars.sql
var migration009SQL string

//go:embed migrations/010_replicas.sql
var migration010SQL string

//go:embed migrations/011_schedules.sql
var migration011SQL string

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

func (s *Store) Migrate() error {
	if _, err := s.db.Exec(initSQL); err != nil {
		return fmt.Errorf("migrate init: %w", err)
	}
	if _, err := s.db.Exec(migration002SQL); err != nil {
		// SQLite returns "duplicate column name" when this migration has already
		// been applied. Treat that as a no-op so Migrate() stays idempotent.
		if !strings.Contains(err.Error(), "duplicate column name") {
			return fmt.Errorf("migrate 002: %w", err)
		}
	}
	if _, err := s.db.Exec(migration003SQL); err != nil {
		return fmt.Errorf("migrate 003: %w", err)
	}
	if _, err := s.db.Exec(migration004SQL); err != nil {
		return fmt.Errorf("migrate 004: %w", err)
	}
	if _, err := s.db.Exec(migration005SQL); err != nil {
		if !strings.Contains(err.Error(), "duplicate column name") {
			return fmt.Errorf("migrate 005: %w", err)
		}
	}
	if _, err := s.db.Exec(migration006SQL); err != nil {
		return fmt.Errorf("migrate 006: %w", err)
	}
	if _, err := s.db.Exec(migration007SQL); err != nil {
		if !strings.Contains(err.Error(), "duplicate column name") {
			return fmt.Errorf("migrate 007: %w", err)
		}
	}
	if _, err := s.db.Exec(migration008SQL); err != nil {
		return fmt.Errorf("migrate 008: %w", err)
	}
	if _, err := s.db.Exec(migration009SQL); err != nil {
		return fmt.Errorf("migrate 009: %w", err)
	}
	if _, err := s.db.Exec(migration010SQL); err != nil {
		if !strings.Contains(err.Error(), "duplicate column name") &&
			!strings.Contains(err.Error(), "no such column") {
			return fmt.Errorf("migrate 010: %w", err)
		}
	}
	if _, err := s.db.Exec(migration011SQL); err != nil {
		return fmt.Errorf("migrate 011: %w", err)
	}
	return nil
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
