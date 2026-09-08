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

	lifecycle.RecoverProcesses(store, mgr, proxy.New(), 0, false, "")

	if mgr.RecoveryPending() {
		t.Error("recovery finished but the manager still reports a pass outstanding; every unadopted replica would read as reconciling forever")
	}
}

// TestRecoverProcesses_ClearsRecoveryPendingOnFailure covers the exit path that
// a non-deferred clear would miss. Recovery gives up early when it cannot even
// list the running apps, and that is precisely the run after which the window
// must still close: the pass is over, it is not going to learn any more, and the
// watchdog owns reconciliation from here. Leaving the flag set on the error path
// would strand the control plane in "checking" exactly when something is wrong.
func TestRecoverProcesses_ClearsRecoveryPendingOnFailure(t *testing.T) {
	store := dbtest.New(t)
	mgr := process.NewManager(t.TempDir(), process.NewNativeRuntime())
	mgr.MarkRecoveryPending()
	store.Close() // ListRunningApps now fails, taking recovery's early return

	lifecycle.RecoverProcesses(store, mgr, proxy.New(), 0, false, "")

	if mgr.RecoveryPending() {
		t.Error("recovery bailed out and left the startup window open; the clear must cover every exit path, not just the successful one")
	}
}
