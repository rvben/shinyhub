package main

import (
	"fmt"
	"log/slog"
	"time"

	"github.com/rvben/shinyhub/internal/backup"
	"github.com/rvben/shinyhub/internal/config"
	"github.com/rvben/shinyhub/internal/db"
)

// prepareDatabase brings an opened store to this binary's schema and
// refuses to serve one it cannot safely run against. The order is the
// contract: the rollback snapshot is written before any migration, and the
// compatibility, legacy-writer and topology checks see the migrated schema.
func prepareDatabase(cfg *config.Config, store *db.Store, logger *slog.Logger, now time.Time) error {
	// Copy the database aside before changing its schema, so a bad upgrade can
	// be rolled back by putting the old binary and this file back. A snapshot
	// that cannot be written aborts startup: migrating a database the operator
	// cannot recover is worse than not starting.
	snap, err := backup.PreMigrationSnapshot(cfg, store, now)
	if err != nil {
		return fmt.Errorf("pre-migration snapshot: %w (set database.pre_migration_snapshot: false to skip it)", err)
	}
	if snap.PruneErr != "" {
		logger.Warn("prune old pre-migration snapshots failed", "err", snap.PruneErr,
			"remedy", "delete stale *.pre-migration-v*.sqlite files beside the database by hand")
	}
	switch {
	case snap.Path != "":
		logger.Info("pre-migration snapshot written", "path", snap.Path,
			"pending_migrations", len(snap.Pending),
			"retention", cfg.Database.PreMigrationSnapshotRetention)
	case len(snap.Pending) > 0 && store.IsPostgres():
		logger.Warn("applying migrations with no snapshot; take a pg_dump before upgrading a Postgres deployment",
			"pending_migrations", len(snap.Pending))
	case len(snap.Pending) > 0:
		logger.Info("applying migrations without a pre-migration snapshot",
			"pending_migrations", len(snap.Pending), "reason", snap.Skipped)
	}
	if err := store.Migrate(); err != nil {
		return fmt.Errorf("db migrate: %w", err)
	}
	// Refuse to serve a database that a newer build already migrated past this
	// binary: the schema would carry columns/tables this code cannot read,
	// risking silent corruption. Fail fast with an actionable message instead.
	if err := store.VerifySchemaCompatibility(); err != nil {
		return fmt.Errorf("schema compatibility: %w", err)
	}
	if legacyRuns, err := store.CountLegacyUnfencedScheduleRuns(); err != nil {
		return fmt.Errorf("legacy schedule writer fence: %w", err)
	} else if legacyRuns > 0 {
		return fmt.Errorf("legacy schedule writer fence: %d schedule run(s) were running during the 0.12.x upgrade and may have surviving unfenced descendants; stop every old server, verify those process trees are gone, then run `shinyhub resolve-legacy-schedule-writers --acknowledge-processes-stopped` before restarting", legacyRuns)
	}
	if err := validateStoredProducerTopology(store, cfg.Runtime); err != nil {
		return fmt.Errorf("producer topology: %w", err)
	}
	return nil
}
