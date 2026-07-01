//go:build linux

package sandbox

import (
	"fmt"

	"github.com/landlock-lsm/go-landlock/landlock"
)

// Apply enforces the spec on the current thread/process using Landlock, which
// also sets NO_NEW_PRIVS. It is best-effort: on kernels without Landlock or with
// an older ABI it enforces what the kernel supports and does not fail, so an app
// on an old kernel still starts (with weaker or no confinement) rather than
// being blocked. Paths that do not exist are ignored rather than erroring.
//
// Landlock restrictions are inherited across execve, so applying here in the
// re-exec shim confines the app image that replaces this process.
func Apply(spec Spec) error {
	if !spec.Level.Enabled() {
		return nil
	}
	rules := make([]landlock.Rule, 0, len(spec.ReadPaths)+len(spec.WritePaths))
	for _, p := range spec.ReadPaths {
		rules = append(rules, landlock.RODirs(p).IgnoreIfMissing())
	}
	for _, p := range spec.WritePaths {
		// WithRefer grants LANDLOCK_ACCESS_FS_REFER (ABI v2+), so renaming or
		// hard-linking a file between two writable trees (e.g. writing to $TMPDIR
		// then atomically renaming into the app/data dir) is allowed. Without it
		// ABI v2+ denies cross-directory renames even between writable paths.
		rules = append(rules, landlock.RWDirs(p).WithRefer().IgnoreIfMissing())
	}
	if err := landlock.V9.BestEffort().RestrictPaths(rules...); err != nil {
		return fmt.Errorf("apply landlock: %w", err)
	}
	return nil
}

// Supported reports whether this platform has an isolation enforcement backend.
func Supported() bool { return true }
