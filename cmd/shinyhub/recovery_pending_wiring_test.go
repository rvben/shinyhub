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
