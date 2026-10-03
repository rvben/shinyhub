package lifecycle_test

import (
	"testing"

	"github.com/rvben/shinyhub/internal/dbtest"
	"github.com/rvben/shinyhub/internal/lifecycle"
	"github.com/rvben/shinyhub/internal/process"
	"github.com/rvben/shinyhub/internal/proxy"
)

// TestRecoverProcesses_ClearsRecoveryPending pins the half of the startup window
// that ends it. The API layer reports a replica as "reconciling" rather than
// stopped for as long as this flag is set, so a recovery pass that returns
// without clearing it would not produce a fifteen-second inaccuracy but a
// permanent one: a genuinely dead replica would read as "checking" forever.
func TestRecoverProcesses_ClearsRecoveryPending(t *testing.T) {
	store := dbtest.New(t)
	mgr := process.NewManager(t.TempDir(), process.NewNativeRuntime())
	mgr.MarkRecoveryPending()
	if !mgr.RecoveryPending() {
		t.Fatal("fixture is not in the startup window; the test below would pass vacuously")
	}

	lifecycle.RecoverProcesses(store, mgr, proxy.New(), 0, false, "", nil, mustPrepareRecovery(t, store))

	if mgr.RecoveryPending() {
		t.Error("recovery finished but the manager still reports a pass outstanding; every unadopted replica would read as reconciling forever")
	}
}

// TestRecoverProcesses_ClearsRecoveryPendingOnFailure covers the exit path that
// a non-deferred clear would miss. PrepareRecovery resolves the running apps
// (and their bundle directories) before RecoverProcesses is ever called, so a
// caller that could not even list the running apps never gets past that step
// and calls RecoverProcesses with no inputs. That is precisely the run after
// which the window must still close: the pass is over, it is not going to
// learn any more, and the watchdog owns reconciliation from here. Leaving the
// flag set on this path would strand the control plane in "checking" exactly
// when something is wrong.
func TestRecoverProcesses_ClearsRecoveryPendingOnFailure(t *testing.T) {
	store := dbtest.New(t)
	mgr := process.NewManager(t.TempDir(), process.NewNativeRuntime())
	mgr.MarkRecoveryPending()
	store.Close() // PrepareRecovery now fails here, taking the caller's early return

	if _, err := lifecycle.PrepareRecovery(store); err == nil {
		t.Fatal("fixture is not exercising the failure path; PrepareRecovery unexpectedly succeeded on a closed store")
	}

	// A caller that saw PrepareRecovery fail never gets here in production
	// (main.go retries it and returns), but RecoverProcesses must still clear
	// the pending flag on its own when handed no inputs, as a defensive
	// backstop independent of the caller's discipline.
	lifecycle.RecoverProcesses(store, mgr, proxy.New(), 0, false, "", nil, nil)

	if mgr.RecoveryPending() {
		t.Error("recovery bailed out and left the startup window open; the clear must cover every exit path, not just the successful one")
	}
}
