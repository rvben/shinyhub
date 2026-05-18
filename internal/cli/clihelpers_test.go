package cli

import (
	"bytes"
	"testing"

	"github.com/spf13/cobra"
)

// forceWriters points cmd and every descendant at w. The cli package's cobra
// commands are package-level singletons shared by every test in the package;
// sibling tests call SetOut on individual leaves, so a parent SetOut does not
// reliably propagate (a child with its own non-nil writer ignores the parent).
// Forcing the writer onto the whole subtree makes captured output
// deterministic regardless of test execution order.
func forceWriters(cmd *cobra.Command, w *bytes.Buffer) {
	cmd.SetOut(w)
	cmd.SetErr(w)
	for _, sub := range cmd.Commands() {
		forceWriters(sub, w)
	}
}

// execCLI runs the real CLI command tree for the given fully-qualified args
// (e.g. execCLI(t, "apps", "set", "demo", "--replicas", "3")) through the
// exact wiring the shipped binary uses: a fresh root with AddCommandsTo, then
// root.Execute. It returns the combined stdout+stderr the commands produced.
//
// This is the order-independent replacement for the fragile
// `xCmd.SetArgs(...); xCmd.Execute()` idiom. The cli package's cobra commands
// are package singletons: AddCommandsTo re-parents them, and cobra's
// Execute on a parented command reroutes to c.Root() using the root's args
// (command.go:1090), so calling appsCmd.Execute() after any prior
// AddCommandsTo runs the wrong (stale) root. A fresh root per call gives
// correct dispatch; forceWriters defeats per-leaf writer contamination from
// sibling tests. Flag-state resets (Changed markers, --json, --tail, …) remain
// the caller's responsibility via the existing resetXFlags helpers, since the
// flag vars live on the singletons.
func execCLI(t *testing.T, args ...string) (string, error) {
	t.Helper()
	root := &cobra.Command{Use: "shinyhub", SilenceErrors: true}
	AddCommandsTo(root)
	var buf bytes.Buffer
	forceWriters(root, &buf)
	root.SetArgs(args)
	err := root.Execute()
	return buf.String(), err
}
