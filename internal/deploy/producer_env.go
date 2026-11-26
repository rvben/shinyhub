package deploy

import "time"

// BundleBuildTimeout is the dependency build budget of the bundle in
// bundleDir: its [app] build_timeout_seconds, or the platform default. An
// unreadable manifest is an error, as it is to the deploy.
func BundleBuildTimeout(bundleDir string) (time.Duration, error) {
	manifest, err := LoadManifest(bundleDir)
	if err != nil {
		return 0, err
	}
	return resolveBuildTimeout(manifest), nil
}
