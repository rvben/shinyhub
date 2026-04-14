package process

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

// SyncR runs renv::restore() in bundleDir to install R package dependencies.
// It is a no-op when renv.lock does not exist (app manages its own packages).
func SyncR(bundleDir string) error {
	lockfile := filepath.Join(bundleDir, "renv.lock")
	if _, err := os.Stat(lockfile); os.IsNotExist(err) {
		return nil // no renv.lock — nothing to restore
	}

	cmd := exec.Command("Rscript", "-e",
		`options(renv.config.sandbox.enabled=FALSE); renv::restore(prompt=FALSE)`)
	cmd.Dir = bundleDir
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("renv::restore: %w\n%s", err, out)
	}
	return nil
}
