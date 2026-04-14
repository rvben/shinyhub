package deploy

// SetPortCounterForTest resets the port allocator to v. Test use only.
var SetPortCounterForTest = func(v int64) { portCounter.Store(v) }
