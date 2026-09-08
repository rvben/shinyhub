package backup

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
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
	// PruneErr is set when a new snapshot was written but removing old
	// snapshots beyond database.pre_migration_snapshot_retention failed. The
	// new snapshot itself is unaffected; a failure to reclaim space on the
	// safety net for a bad upgrade must never block that upgrade from
	// starting, so callers log this and continue rather than treating it as
	// an error.
	PruneErr string
}

// PreMigrationSnapshot copies the SQLite database aside before pending
// migrations run, so a bad upgrade can be rolled back by swapping the file
// back. It is a no-op when nothing is pending, when the operator opted out, or
// on Postgres (where pg_dump is the supported path).
//
// A snapshot that cannot be written is an error, never a silent skip: startup
// must stop rather than migrate a database the operator cannot get back.
// After a successful snapshot, older ones beyond
// database.pre_migration_snapshot_retention are pruned so an unattended
// string of upgrades cannot grow this list forever.
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
	if perr := pruneOldSnapshots(dbPath, cfg.Database.PreMigrationSnapshotRetention); perr != nil {
		res.PruneErr = perr.Error()
	}
	return res, nil
}

// pruneOldSnapshots removes pre-migration snapshot files for dbPath beyond the
// newest keep. Snapshots are ordered by modification time rather than by
// name: the embedded schema version's digit width varies ("v9" versus "v10"),
// which sorts wrong lexically, while the file's mtime always reflects the
// real order snapshots were taken in.
func pruneOldSnapshots(dbPath string, keep int) error {
	if keep <= 0 {
		keep = 5
	}
	matches, err := filepath.Glob(dbPath + ".pre-migration-v*-*.sqlite")
	if err != nil {
		return fmt.Errorf("list pre-migration snapshots: %w", err)
	}
	if len(matches) <= keep {
		return nil
	}

	type snapshot struct {
		path    string
		modTime time.Time
	}
	snaps := make([]snapshot, 0, len(matches))
	for _, m := range matches {
		info, serr := os.Stat(m)
		if serr != nil {
			continue
		}
		snaps = append(snaps, snapshot{path: m, modTime: info.ModTime()})
	}
	sort.Slice(snaps, func(i, j int) bool { return snaps[i].modTime.Before(snaps[j].modTime) })

	toDelete := len(snaps) - keep
	for i := 0; i < toDelete; i++ {
		if err := os.Remove(snaps[i].path); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("remove old pre-migration snapshot %s: %w", snaps[i].path, err)
		}
	}
	return nil
}
