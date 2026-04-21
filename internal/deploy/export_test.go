package deploy

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
