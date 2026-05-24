// Package storage owns ShinyHub's on-disk and pluggable storage concerns: the
// on-disk lifecycle helpers shared by the API and CLI (the slug-keyed
// convention for per-app directories, one under Storage.AppsDir for code and
// one under Storage.AppDataDir for persistent data), plus the DataVolume
// abstraction that provisions per-app mutable data on a single host's local
// disk (or, in a later phase, a shared backend).
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

// SweepOrphanDirs returns slug directories under AppsDir/AppDataDir that have
// no owning row in known. It deliberately does NOT delete anything: a
// directory with no DB row may also be operator-managed state or the result
// of a bug, and auto-deleting user bytes on boot is unacceptable. Callers log
// the result so an operator can investigate and reclaim space deliberately.
// The platform-owned upload-temp dir name is treated as part of an app's data
// dir, not a top-level slug, so it is never reported.
func SweepOrphanDirs(cfg *config.Config, known map[string]bool) ([]string, error) {
	var orphans []string
	var errs []error
	for _, base := range []string{cfg.Storage.AppsDir, cfg.Storage.AppDataDir} {
		entries, err := os.ReadDir(base)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			errs = append(errs, fmt.Errorf("read %s: %w", base, err))
			continue
		}
		for _, e := range entries {
			if !e.IsDir() {
				continue
			}
			if known[e.Name()] {
				continue
			}
			orphans = append(orphans, filepath.Join(base, e.Name()))
		}
	}
	return orphans, errors.Join(errs...)
}

func slugPaths(cfg *config.Config, slug string) []string {
	return []string{
		filepath.Join(cfg.Storage.AppsDir, slug),
		filepath.Join(cfg.Storage.AppDataDir, slug),
	}
}
