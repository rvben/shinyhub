package deploy

import "path/filepath"

// BundleDir is the canonical extracted-bundle directory for an app version:
// <appsDir>/<slug>/versions/<version>. The single source of truth for this path,
// used at deploy time and by the legacy bundle_dir backfill.
func BundleDir(appsDir, slug, version string) string {
	return filepath.Join(appsDir, slug, "versions", version)
}
