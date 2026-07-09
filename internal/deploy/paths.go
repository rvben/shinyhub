package deploy

import "path/filepath"

// BundleDir is the canonical extracted-bundle directory for an app version:
// <appsDir>/<slug>/versions/<version>. The single source of truth for this path,
// used at deploy time and by the legacy bundle_dir backfill.
func BundleDir(appsDir, slug, version string) string {
	return filepath.Join(appsDir, slug, "versions", version)
}

// pythonInstallDir is the managed-Python store (uv's UV_PYTHON_INSTALL_DIR)
// for builds and hooks running against bundleDir. In the canonical server
// layout it is <appsDir>/<slug>/uv-python: a sibling of versions/ and
// bundles/, so one interpreter download serves every version of the app,
// survives PruneOldVersions, is reclaimed by OnAppDelete with the rest of the
// slug dir, and - deliberately per-app, not shared - never becomes writable to
// another app's build, whose deployer-controlled build backend could otherwise
// backdoor an interpreter this app executes. A bundle outside that layout
// (`shinyhub run` on an arbitrary project dir) gets a self-contained
// .uv-python inside the bundle, next to .venv and .uv-cache.
func pythonInstallDir(bundleDir string) string {
	if parent := filepath.Dir(bundleDir); filepath.Base(parent) == "versions" {
		return filepath.Join(filepath.Dir(parent), "uv-python")
	}
	return filepath.Join(bundleDir, ".uv-python")
}
