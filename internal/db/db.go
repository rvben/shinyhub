package db

import (
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
	return nil
}

func (s *Store) Close() error {
	return s.db.Close()
}
