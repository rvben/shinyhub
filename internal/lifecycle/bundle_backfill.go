package lifecycle

import (
	"github.com/rvben/shinyhub/internal/db"
	"github.com/rvben/shinyhub/internal/deploy"
)

// NormalizeBundleDirs rewrites any relative deployments.bundle_dir to its
// canonical absolute path under appsDir, reconstructed from the app slug and
// version (CWD-independent, so it is correct on any instance). Idempotent.
func NormalizeBundleDirs(store *db.Store, appsDir string) error {
	rows, err := store.DeploymentsWithRelativeBundleDir()
	if err != nil {
		return err
	}
	for _, r := range rows {
		abs := deploy.BundleDir(appsDir, r.Slug, r.Version)
		if err := store.SetDeploymentBundleDir(r.ID, abs); err != nil {
			return err
		}
	}
	return nil
}
