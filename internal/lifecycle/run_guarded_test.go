package lifecycle

import "testing"

// TestRunGuarded_RecoversPanic proves a panic inside a guarded background-loop
// iteration is contained (logged, not propagated) so it cannot crash the whole
// process and freeze fleet-wide self-healing (PROD-1). A subsequent call still
// runs normally.
func TestRunGuarded_RecoversPanic(t *testing.T) {
	// Must not propagate the panic.
	runGuarded("test", func() { panic("boom") })

	ran := false
	runGuarded("test", func() { ran = true })
	if !ran {
		t.Fatal("runGuarded did not run fn after recovering a prior panic")
	}
}
