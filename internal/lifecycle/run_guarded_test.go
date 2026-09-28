package lifecycle

import (
	"testing"

	"github.com/rvben/shinyhub/internal/safego"
)

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

// TestRunGuarded_RepanicsFatal proves a safego.Fatal panic (raised when a
// watchdog restart reaches Manager.Start's launch-to-publish window) is not
// absorbed by runGuarded like an ordinary panic: it must still propagate out,
// since swallowing it would leave a launched process with no tracking entry.
func TestRunGuarded_RepanicsFatal(t *testing.T) {
	var recovered any
	func() {
		defer func() { recovered = recover() }()
		runGuarded("test", func() { panic(safego.Fatal{Value: "boom"}) })
	}()
	f, ok := recovered.(safego.Fatal)
	if !ok {
		t.Fatalf("expected a safego.Fatal panic to propagate out of runGuarded, got %#v", recovered)
	}
	if f.Value != "boom" {
		t.Fatalf("expected original panic value preserved, got %v", f.Value)
	}
}
