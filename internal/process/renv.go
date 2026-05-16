package process

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
)

// renvRestoreCmd builds the renv::restore command. renv evaluates the
// project's renv profile (deployer-controlled R code), so the env is
// scrubbed of server secrets via SanitizedEnv.
func renvRestoreCmd(bundleDir string) *exec.Cmd {
	cmd := exec.Command("Rscript", "-e",
		`options(renv.config.sandbox.enabled=FALSE); renv::restore(prompt=FALSE)`)
	cmd.Dir = bundleDir
	cmd.Env = SanitizedEnv()
	return cmd
}

// SyncR runs renv::restore() in bundleDir to install R package dependencies.
// It is a no-op when renv.lock does not exist (app manages its own packages).
func SyncR(bundleDir string) error {
	lockfile := filepath.Join(bundleDir, "renv.lock")
	if _, err := os.Stat(lockfile); errors.Is(err, fs.ErrNotExist) {
		return nil // no renv.lock — nothing to restore
	}

	if out, err := renvRestoreCmd(bundleDir).CombinedOutput(); err != nil {
		return fmt.Errorf("renv::restore: %w\n%s", err, out)
	}
	return nil
}
