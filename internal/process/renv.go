package process

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// RProjectLibraryDir is the bundle-relative library ShinyHub provisions for an
// R bundle that ships renv.lock without renv's own activation files.
//
// renv restores into .libPaths()[1]. An renv-initialized bundle carries a
// .Rprofile that sources renv/activate.R, and that is what puts the project
// library first. A bundle with a bare lockfile has nothing doing so, leaving
// .libPaths()[1] as a system library - which is read-only under any hardened
// service confinement (systemd ProtectSystem=strict, a read-only container
// root), so the restore fails and the app can never start. ShinyHub therefore
// supplies the missing project library itself.
//
// The path is deliberately relative. Every R runtime starts the process with
// its working directory at the bundle (that is what `shiny::runApp('.')`
// already relies on), and .libPaths() normalizes a relative entry against the
// working directory at the time of the call, so one relative name is correct
// for the native host, a Docker bind mount at /app, and a task definition
// alike - where a baked absolute host path would be wrong in two of the three.
const RProjectLibraryDir = ".shinyhub-rlib"

// RenvPolicyEnv is the renv configuration ShinyHub imposes on every R build
// and launch. It is environment rather than an options() call in the -e
// expression because renv reads its configuration while the project profile is
// being sourced, which happens before any expression runs: an options() call
// is evaluated too late to affect activation.
//
// Disabling the sandbox is what keeps an app deletable. renv's sandbox is a
// mode 0555 directory tree, and a directory with no write bit cannot have its
// children unlinked, so an app that built one could never be removed: the
// delete failed, the slug stayed occupied, and the reconcile loop retried the
// same failing unlink indefinitely. fsx.RemoveAll recovers such a tree, but
// not creating it is the fix; the sandbox only shadows the ambient library,
// which the project library already takes precedence over.
//
// It is layered after the app's own environment, so an app cannot re-enable
// the sandbox for its replicas.
func RenvPolicyEnv() []string {
	return []string{"RENV_CONFIG_SANDBOX_ENABLED=FALSE"}
}

// RenvPolicyEnvFor returns RenvPolicyEnv for a bundle that can activate renv,
// and nil for one that cannot.
//
// It is keyed on the bundle rather than on the resolved app type because the
// sandbox is created by renv at R startup, whoever started R. A bundle that
// declares its own [app] command - a plumber API, a custom Rscript entrypoint -
// never goes through app.R detection, but still activates renv and still builds
// the mode 0555 tree that made its app undeletable.
func RenvPolicyEnvFor(bundleDir string) []string {
	if !renvPresent(bundleDir) {
		return nil
	}
	return RenvPolicyEnv()
}

// renvPresent reports whether anything in the bundle could bring renv up: a
// lockfile to restore from, or renv's own activation script.
func renvPresent(bundleDir string) bool {
	for _, rel := range [][]string{{"renv.lock"}, {"renv", "activate.R"}} {
		if _, err := os.Stat(filepath.Join(bundleDir, filepath.Join(rel...))); err == nil {
			return true
		}
	}
	return false
}

// InterposedRLibrary returns RProjectLibraryDir when bundleDir needs ShinyHub
// to supply the library renv restores into, and "" when it does not - either
// because there is no lockfile to restore, or because the bundle activates
// renv itself and interposing would shadow renv's own project library.
func InterposedRLibrary(bundleDir string) string {
	if _, err := os.Stat(filepath.Join(bundleDir, "renv.lock")); err != nil {
		return ""
	}
	if renvActivates(bundleDir) {
		return ""
	}
	return RProjectLibraryDir
}

// EnsureInterposedRLibrary creates the interposed library if bundleDir needs
// one, returning its bundle-relative name (or "" when none is needed). renv
// creates a missing library directory itself on the host, but under the build
// sandbox the parent is the only writable root and a pre-existing directory is
// the difference between a write rule that applies and one that is silently
// dropped, so it is created up front.
func EnsureInterposedRLibrary(bundleDir string) (string, error) {
	lib := InterposedRLibrary(bundleDir)
	if lib == "" {
		return "", nil
	}
	if err := os.MkdirAll(filepath.Join(bundleDir, lib), 0o755); err != nil {
		return "", fmt.Errorf("create R project library: %w", err)
	}
	return lib, nil
}

// renvActivates reports whether the bundle brings renv's own project library up
// at R startup, in which case interposing would shadow it.
//
// Activation is a property of the .Rprofile, the only file R sources before the
// app runs, and it has two spellings. renv::init() writes a source() of
// renv/activate.R, which does nothing if that script is absent, so both parts
// are required. A hand-written profile can instead call renv::activate()
// against a host-installed renv, which needs no script in the bundle. An
// activate.R that no profile sources is never run either way.
func renvActivates(bundleDir string) bool {
	f, err := os.Open(filepath.Join(bundleDir, ".Rprofile"))
	if err != nil {
		return false
	}
	defer f.Close()
	// The profile is deployer-controlled, so the read is bounded. renv writes a
	// single source() line; a profile that mentions activation past 64 KiB is
	// not one renv wrote.
	head, err := io.ReadAll(io.LimitReader(f, 64<<10))
	if err != nil {
		return false
	}
	profile := string(head)
	if strings.Contains(profile, "renv::activate") {
		return true
	}
	if !strings.Contains(profile, "renv/activate.R") {
		return false
	}
	_, err = os.Stat(filepath.Join(bundleDir, "renv", "activate.R"))
	return err == nil
}

// RLibPathsExpr is the R expression that puts lib first on the library search
// path, keeping every ambient entry (including anything an operator set via
// R_LIBS) reachable behind it.
func RLibPathsExpr(lib string) string {
	return fmt.Sprintf(".libPaths(c(%s, .libPaths()));", quoteR(lib))
}

// quoteR renders s as an R single-quoted string literal.
func quoteR(s string) string {
	return "'" + strings.NewReplacer(`\`, `\\`, `'`, `\'`).Replace(s) + "'"
}

// RenvRestoreArgv returns the Rscript invocation that restores a bundle's
// packages. lib is the interposed library from InterposedRLibrary; "" leaves
// the destination to renv, which is what an renv-activated bundle needs since
// its activation already selected the project library.
func RenvRestoreArgv(lib string) []string {
	expr := "renv::restore(prompt = FALSE)"
	if lib != "" {
		// The library is passed to restore() explicitly rather than left to
		// .libPaths()[1] so the destination does not depend on renv's project
		// inference, and set on .libPaths() as well so the restore resolves
		// already-installed dependencies against the same library.
		expr = fmt.Sprintf("%s renv::restore(prompt = FALSE, library = %s)",
			RLibPathsExpr(lib), quoteR(lib))
	}
	return []string{"Rscript", "-e", expr}
}

// renvRestoreCmd builds the renv::restore command. renv evaluates the
// project's renv profile (deployer-controlled R code), so the env is
// scrubbed of server secrets via SanitizedEnv.
func renvRestoreCmd(ctx context.Context, bundleDir, lib string) *exec.Cmd {
	argv := RenvRestoreArgv(lib)
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Dir = bundleDir
	cmd.Env = append(SanitizedEnv(), RenvPolicyEnv()...)
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

	lib, err := EnsureInterposedRLibrary(bundleDir)
	if err != nil {
		return err
	}

	out, err := renvRestoreCmd(ctx, bundleDir, lib).CombinedOutput()
	if err != nil {
		switch ctx.Err() {
		case context.DeadlineExceeded:
			return fmt.Errorf("build exceeded the build timeout: %w", ctx.Err())
		case context.Canceled:
			return fmt.Errorf("build canceled: %w", ctx.Err())
		}
		return fmt.Errorf("%w\n%s", err, out)
	}
	return nil
}
