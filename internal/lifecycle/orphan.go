package lifecycle

import (
	"context"
	"log/slog"

	"github.com/rvben/shinyhub/internal/config"
	"github.com/rvben/shinyhub/internal/db"
	"github.com/rvben/shinyhub/internal/storage"
)

// AppSecretsCleaner removes an app's external secret-backend resources (Fargate
// Secrets Manager entries and per-app task-definition revisions) when the app
// is deleted. A nil cleaner disables this step (no secrets backend configured).
type AppSecretsCleaner interface {
	CleanupApp(ctx context.Context, appID int64) error
}

// ReconcileDeletingApps finishes app deletions that were interrupted between
// the 'deleting' tombstone and the row removal (a crash, or a cleanup failure
// that handleDeleteApp deliberately deferred). For each tombstoned app it
// retries on-disk cleanup and external secret-backend cleanup and, only once
// both succeed, drops the row. A still-failing cleanup leaves the tombstone in
// place for the next startup rather than orphaning bytes, secrets, or task-def
// revisions. cleaner may be nil.
func ReconcileDeletingApps(ctx context.Context, store *db.Store, cfg *config.Config, cleaner AppSecretsCleaner) {
	apps, err := store.ListDeletingApps()
	if err != nil {
		slog.Error("reconcile deleting apps: list", "err", err)
		return
	}
	for _, app := range apps {
		if err := storage.OnAppDelete(cfg, app.Slug); err != nil {
			slog.Error("reconcile deleting apps: cleanup still failing; tombstone retained",
				"slug", app.Slug, "err", err)
			continue
		}
		if cleaner != nil {
			if err := cleaner.CleanupApp(ctx, app.ID); err != nil {
				slog.Error("reconcile deleting apps: secret cleanup still failing; tombstone retained",
					"slug", app.Slug, "err", err)
				continue
			}
		}
		if err := store.DeleteApp(app.Slug); err != nil {
			slog.Error("reconcile deleting apps: row removal failed",
				"slug", app.Slug, "err", err)
			continue
		}
		slog.Info("reconcile deleting apps: finished", "slug", app.Slug)
	}
}

// LogOrphanAppDirs reports slug directories under the apps/app-data roots that
// have no owning DB row. It only logs (never deletes): auto-removing user
// bytes on boot is unacceptable, so an operator must reclaim the space
// deliberately. Run AFTER ReconcileDeletingApps so freshly-cleaned slugs are
// not reported.
func LogOrphanAppDirs(store *db.Store, cfg *config.Config) {
	slugs, err := store.AllSlugs()
	if err != nil {
		slog.Error("orphan dir sweep: list slugs", "err", err)
		return
	}
	known := make(map[string]bool, len(slugs))
	for _, s := range slugs {
		known[s] = true
	}
	orphans, err := storage.SweepOrphanDirs(cfg, known)
	if err != nil {
		slog.Error("orphan dir sweep: scan", "err", err)
		// fall through: still report whatever was found before the error
	}
	for _, p := range orphans {
		slog.Warn("orphan dir sweep: directory has no owning app row (not deleted)", "path", p)
	}
	if len(orphans) > 0 {
		slog.Warn("orphan dir sweep: complete", "orphans", len(orphans))
	}
}
