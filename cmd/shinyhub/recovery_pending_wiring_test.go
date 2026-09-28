package main

import (
	"os"
	"strings"
	"testing"
)

// The startup re-adoption window is declared in main.go, which cannot be unit
// imported. Everything below it depends on that one call: the API layer reports
// a surviving replica as "reconciling" instead of stopped only while the flag is
// set, so a refactor that drops the call silently restores the original defect —
// a healthy, serving app shown as down for as long as recovery takes — with
// every unit test still green.
//
// Presence alone is not the contract, though. The call is only worth anything if
// it happens before anyone can read the manager and before the pass it describes
// runs, so this pins its position between two independent bounds rather than
// merely asserting it exists somewhere in the file.
func TestMainMarksRecoveryPendingBeforeTheManagerIsReadable(t *testing.T) {
	src, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatalf("read main.go: %v", err)
	}
	s := string(src)

	const (
		construct = "mgr := process.NewManager("
		mark      = "mgr.MarkRecoveryPending()"
		serve     = "srv := api.New(cfg, store, mgr, prx)"
		recover   = "lifecycle.RecoverProcesses("
	)
	idx := map[string]int{}
	for _, needle := range []string{construct, mark, serve, recover} {
		i := strings.Index(s, needle)
		if i < 0 {
			t.Fatalf("main.go no longer contains %q; the wiring this test pins has moved or gone", needle)
		}
		if strings.Count(s, needle) != 1 {
			t.Fatalf("%q appears %d times in main.go; the position check below is ambiguous", needle, strings.Count(s, needle))
		}
		idx[needle] = i
	}

	// Lower bound: the manager has to exist first. This one is a sanity check on
	// the anchors more than on the code.
	if idx[mark] < idx[construct] {
		t.Errorf("MarkRecoveryPending is called before the manager is built")
	}
	// Upper bound one: nothing may be able to read the manager before the window
	// is declared. The API server is the reader that matters — it is what answers
	// GET /api/apps while recovery is still running.
	if idx[mark] > idx[serve] {
		t.Errorf("MarkRecoveryPending happens after the API server is handed the manager, so a request that lands in between still reports surviving replicas as stopped")
	}
	// Upper bound two: marking after the pass it describes would leave the flag
	// set with nothing left to clear it, turning a fifteen-second inaccuracy into
	// a permanent one.
	if idx[mark] > idx[recover] {
		t.Errorf("MarkRecoveryPending happens after RecoverProcesses, so the window it opens is never closed")
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
	src, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatalf("read main.go: %v", err)
	}
	s := string(src)

	const (
		serve   = "srv := api.New(cfg, store, mgr, prx)"
		prepare = "lifecycle.PrepareRecovery(store)"
		retry   = "retryOwnerStep(\"prepare process recovery\""
		recover = "lifecycle.RecoverProcesses("
	)
	idx := map[string]int{}
	for _, needle := range []string{serve, prepare, retry, recover} {
		i := strings.Index(s, needle)
		if i < 0 {
			t.Fatalf("main.go no longer contains %q; the wiring this test pins has moved or gone", needle)
		}
		if strings.Count(s, needle) != 1 {
			t.Fatalf("%q appears %d times in main.go; the position check below is ambiguous", needle, strings.Count(s, needle))
		}
		idx[needle] = i
	}

	// Lower bound: PrepareRecovery must be resolved through the same retry
	// pattern as every other fallible startup step, not called once and given
	// up on. This is a sanity check on the anchors more than on the code.
	if idx[prepare] < idx[retry] {
		t.Errorf("PrepareRecovery is not wrapped in the retryOwnerStep it needs; a transient database error would abandon recovery instead of retrying")
	}
	// Lower bound two: the server is already serving requests by this point, so
	// a slow or retried prepare step must not block behind it in the wrong
	// order; this just anchors the two calls to the same startup phase.
	if idx[prepare] < idx[serve] {
		t.Errorf("PrepareRecovery happens before the API server is constructed; the wiring this test pins has moved")
	}
	// Upper bound: PrepareRecovery must fully resolve every app's bundle
	// directory before RecoverProcesses is allowed to adopt anything, so a
	// lookup failure for one app fails the whole snapshot instead of handing
	// RecoverProcesses an unresolved ("") bundle directory for it.
	if idx[prepare] > idx[recover] {
		t.Errorf("PrepareRecovery happens after RecoverProcesses, so recovery can run on an unresolved bundle directory")
	}
}
