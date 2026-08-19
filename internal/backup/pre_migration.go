package backup

import (
	"fmt"
	"os"
	"time"

	"github.com/rvben/shinyhub/internal/config"
	"github.com/rvben/shinyhub/internal/db"
)

// SnapshotResult describes what PreMigrationSnapshot did. Path and Skipped are
// mutually exclusive and exactly one is always set, so "no snapshot" is never
// indistinguishable from "snapshot at the empty path": a caller that logs both
// can always say what happened and why.
type SnapshotResult struct {
	// Pending lists the migration versions Migrate would apply. Reported
	// whether or not a snapshot was taken.
	Pending []int
	// Path is the snapshot that was written, empty when none was.
	Path string
	// Skipped explains why no snapshot was written, empty when one was.
	Skipped string
}

// PreMigrationSnapshot copies the SQLite database aside before pending
// migrations run, so a bad upgrade can be rolled back by swapping the file
// back. It is a no-op when nothing is pending, when the operator opted out, or
// on Postgres (where pg_dump is the supported path).
//
// A snapshot that cannot be written is an error, never a silent skip: startup
// must stop rather than migrate a database the operator cannot get back.
// Snapshots are never pruned here - removing one is an explicit decision.
func PreMigrationSnapshot(cfg *config.Config, store *db.Store, now time.Time) (SnapshotResult, error) {
	pending, err := store.PendingMigrations()
	if err != nil {
		return SnapshotResult{}, fmt.Errorf("determine pending migrations: %w", err)
	}
	res := SnapshotResult{Pending: pending}

	switch {
	case len(pending) == 0:
		res.Skipped = "no pending migrations"
		return res, nil
	case !cfg.Database.PreMigrationSnapshot:
		res.Skipped = "disabled by database.pre_migration_snapshot"
		return res, nil
	case db.IsPostgresDSN(cfg.Database.DSN):
		// VACUUM INTO is SQLite-only. Postgres operators take a pg_dump; the
		// caller surfaces that as a warning rather than blocking the upgrade.
		res.Skipped = "postgres backend (use pg_dump before upgrading)"
		return res, nil
	}

	dbPath, ok := dbFilePath(cfg.Database.DSN)
	if !ok {
		res.Skipped = "in-memory database"
		return res, nil
	}
	if _, serr := os.Stat(dbPath); serr != nil {
		res.Skipped = "new database (nothing to preserve)"
		return res, nil
	}

	from, err := store.SchemaVersion()
	if err != nil {
		return res, fmt.Errorf("read schema version: %w", err)
	}
	// Version 0 with no legacy schema is a fresh install: db.Open has already
	// created a non-empty file by now, so neither existence nor size can tell
	// an empty database from a populated one. Only the ledger can.
	if from == 0 {
		legacy, lerr := store.HasLegacySchema()
		if lerr != nil {
			return res, fmt.Errorf("probe legacy schema: %w", lerr)
		}
		if !legacy {
			res.Skipped = "new database (nothing to preserve)"
			return res, nil
		}
	}
	dest := fmt.Sprintf("%s.pre-migration-v%d-%s.sqlite", dbPath, from, now.UTC().Format("20060102T150405Z"))
	if err := store.BackupTo(dest); err != nil {
		return res, fmt.Errorf("pre-migration snapshot: %w", err)
	}
	res.Path = dest
	return res, nil
}
