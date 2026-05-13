package deploy

import (
	"context"
	"io"
)

// SetPortCounterForTest resets the port allocator to v. Test use only.
var SetPortCounterForTest = func(v int64) { portCounter.Store(v) }

// SetSyncHooksForTest swaps the package's host-side dep installation hooks.
// Returns a restore func that re-installs the originals — pair with defer.
// Test use only.
func SetSyncHooksForTest(py, r func(string) error) (restore func()) {
	origPy, origR := pythonSyncFn, rSyncFn
	pythonSyncFn, rSyncFn = py, r
	return func() { pythonSyncFn, rSyncFn = origPy, origR }
}

// HookRunnerFunc matches the unexported hookRunner signature. Exported so
// external _test packages can substitute a recorder without spawning real
// processes.
type HookRunnerFunc = func(ctx context.Context, dir string, argv []string, env []string, w io.Writer) error

// SetHookRunnerForTest swaps the package's manifest hook runner. Returns
// a restore func — pair with defer. Test use only.
func SetHookRunnerForTest(fn HookRunnerFunc) (restore func()) {
	orig := hookRunner
	hookRunner = fn
	return func() { hookRunner = orig }
}
