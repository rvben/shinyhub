// Package storage provides on-disk lifecycle helpers shared by the API and
// CLI. It owns the slug-keyed convention for per-app directories (one under
// Storage.AppsDir for code, one under Storage.AppDataDir for persistent data).
package storage

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/rvben/shinyhub/internal/config"
)

// ErrSlugInUse is returned by RequireFreeSlug when a leftover directory exists
// for the given slug. Callers should surface this as 409 Conflict.
var ErrSlugInUse = errors.New("slug already has on-disk state")

// RequireFreeSlug returns ErrSlugInUse (wrapped with the offending path) if
// either the per-app code dir or per-app data dir already exists for slug.
// Callers must invoke this before creating the DB row so a partial-failure
// recreate cannot inherit the prior app's bytes.
func RequireFreeSlug(cfg *config.Config, slug string) error {
	for _, p := range slugPaths(cfg, slug) {
		if _, err := os.Stat(p); err == nil {
			return fmt.Errorf("%w at %s", ErrSlugInUse, p)
		} else if !os.IsNotExist(err) {
			return fmt.Errorf("stat %s: %w", p, err)
		}
	}
	return nil
}

// OnAppDelete is the post-DB-delete filesystem cleanup. Failures are joined
// and returned so the caller can attach them to the audit detail; they do
// not invalidate the delete itself (the DB row is already gone).
func OnAppDelete(cfg *config.Config, slug string) error {
	var errs []error
	for _, p := range slugPaths(cfg, slug) {
		if err := os.RemoveAll(p); err != nil {
			errs = append(errs, fmt.Errorf("remove %s: %w", p, err))
		}
	}
	return errors.Join(errs...)
}

func slugPaths(cfg *config.Config, slug string) []string {
	return []string{
		filepath.Join(cfg.Storage.AppsDir, slug),
		filepath.Join(cfg.Storage.AppDataDir, slug),
	}
}
