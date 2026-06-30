package process

import (
	"context"
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
func renvRestoreCmd(ctx context.Context, bundleDir string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, "Rscript", "-e",
		`options(renv.config.sandbox.enabled=FALSE); renv::restore(prompt=FALSE)`)
	cmd.Dir = bundleDir
	cmd.Env = SanitizedEnv()
	return cmd
}

// SyncR runs renv::restore() in bundleDir to install R package dependencies.
// It is a no-op when renv.lock does not exist (app manages its own packages).
// The caller adds the "renv restore:" prefix that deployfail classifies on.
func SyncR(ctx context.Context, bundleDir string) error {
	lockfile := filepath.Join(bundleDir, "renv.lock")
	if _, err := os.Stat(lockfile); errors.Is(err, fs.ErrNotExist) {
		return nil // no renv.lock — nothing to restore
	}

	out, err := renvRestoreCmd(ctx, bundleDir).CombinedOutput()
	if err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			return fmt.Errorf("build exceeded the build timeout: %w", ctx.Err())
		}
		return fmt.Errorf("%w\n%s", err, out)
	}
	return nil
}
