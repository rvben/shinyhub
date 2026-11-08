package main

import (
	"os"
	"strings"
	"testing"
)

// sourceIndex returns the offset of needle in the named source file, failing
// the test unless it appears exactly once, so a position check against it is
// never ambiguous.
func sourceIndex(t *testing.T, path, needle string) int {
	t.Helper()
	src, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	s := string(src)
	i := strings.Index(s, needle)
	if i < 0 {
		t.Fatalf("%s no longer contains %q; the wiring this test pins has moved or gone", path, needle)
	}
	if n := strings.Count(s, needle); n != 1 {
		t.Fatalf("%q appears %d times in %s; the position check is ambiguous", needle, n, path)
	}
	return i
}

// sourceCount returns how often needle appears in the named source file.
func sourceCount(t *testing.T, path, needle string) int {
	t.Helper()
	src, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return strings.Count(string(src), needle)
}

// The startup re-adoption window is declared in runServe, which cannot be unit
// imported. Everything below it depends on that one call: the API layer reports
// a surviving replica as "reconciling" instead of stopped only while the flag is
// set, so a refactor that drops the call silently restores the original defect
// (a healthy, serving app shown as down for as long as recovery takes) with
// every unit test still green.
//
// Presence alone is not the contract, though. The call is only worth anything if
// it happens before anyone can read the manager and before the pass it describes
// runs, so this pins its position between two independent bounds rather than
// merely asserting it exists somewhere in the file.
func TestMainMarksRecoveryPendingBeforeTheManagerIsReadable(t *testing.T) {
	const (
		construct = "mgr := process.NewManager("
		mark      = "mgr.MarkRecoveryPending()"
		serve     = "srv := api.New(cfg, store, mgr, prx)"
		// The recovery pass runs only inside ownerRuntime.run, which cannot
		// start before the ownership scope that drives it exists.
		ownerScope = "scope := leader.NewOwnerScope(owner.run)"
		recover    = "lifecycle.RecoverProcesses("
	)
	idx := map[string]int{}
	for _, needle := range []string{construct, mark, serve, ownerScope} {
		idx[needle] = sourceIndex(t, "main.go", needle)
	}
	sourceIndex(t, "owner_runtime.go", recover)
	if n := sourceCount(t, "main.go", recover); n != 0 {
		t.Fatalf("main.go calls RecoverProcesses %d times outside the owner runtime, so the scope bound below no longer covers it", n)
	}

	// Lower bound: the manager has to exist first. This one is a sanity check on
	// the anchors more than on the code.
	if idx[mark] < idx[construct] {
		t.Errorf("MarkRecoveryPending is called before the manager is built")
	}
	// Upper bound one: nothing may be able to read the manager before the window
	// is declared. The API server is the reader that matters: it is what answers
	// GET /api/apps while recovery is still running.
	if idx[mark] > idx[serve] {
		t.Errorf("MarkRecoveryPending happens after the API server is handed the manager, so a request that lands in between still reports surviving replicas as stopped")
	}
	// Upper bound two: marking after the pass it describes would leave the flag
	// set with nothing left to clear it, turning a fifteen-second inaccuracy into
	// a permanent one. The pass can only run once the ownership scope exists.
	if idx[mark] > idx[ownerScope] {
		t.Errorf("MarkRecoveryPending happens after the ownership scope that runs RecoverProcesses is created, so the window it opens may never close")
	}
}

// TestMainPreparesRecoveryBeforeRecoverProcesses pins the ordering that keeps
// RecoverProcesses from ever adopting on an unresolved bundle directory:
// PrepareRecovery must run to completion, retried until it succeeds, before
// RecoverProcesses is called at all. A refactor that inlines the two calls out
// of order, or drops the retry, would let recovery run with an incomplete
// snapshot on a transient database error, with every other unit test still
// green.
func TestMainPreparesRecoveryBeforeRecoverProcesses(t *testing.T) {
	const (
		ownerRun = "func (r *ownerRuntime) run("
		retry    = "retryOwnerStep(\"prepare process recovery\""
		prepare  = "lifecycle.PrepareRecovery(store)"
		recover  = "lifecycle.RecoverProcesses("
	)
	idx := map[string]int{}
	for _, needle := range []string{ownerRun, retry, prepare, recover} {
		idx[needle] = sourceIndex(t, "owner_runtime.go", needle)
	}

	// Lower bound: both calls belong to the owner's startup, which runs only
	// while this instance holds ownership. This is a sanity check on the
	// anchors more than on the code.
	if idx[retry] < idx[ownerRun] {
		t.Errorf("the prepare-recovery retry is no longer inside ownerRuntime.run; the wiring this test pins has moved")
	}
	// Lower bound two: PrepareRecovery must be resolved through the same retry
	// pattern as every other fallible startup step, not called once and given
	// up on.
	if idx[prepare] < idx[retry] {
		t.Errorf("PrepareRecovery is not wrapped in the retryOwnerStep it needs; a transient database error would abandon recovery instead of retrying")
	}
	// Upper bound: PrepareRecovery must fully resolve every app's bundle
	// directory before RecoverProcesses is allowed to adopt anything, so a
	// lookup failure for one app fails the whole snapshot instead of handing
	// RecoverProcesses an unresolved ("") bundle directory for it.
	if idx[prepare] > idx[recover] {
		t.Errorf("PrepareRecovery happens after RecoverProcesses, so recovery can run on an unresolved bundle directory")
	}
}
