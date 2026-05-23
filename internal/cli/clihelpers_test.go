package cli

import (
	"bytes"
	"io"
	"testing"

	"github.com/spf13/cobra"
)

// forceWriters points cmd and every descendant at w so captured output is
// deterministic. AddCommandsTo now builds a fresh command tree per call with
// no shared writers, so this is belt-and-suspenders: it also defeats any
// per-leaf SetOut a test performs on a command it constructed itself.
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
// AddCommandsTo constructs every command (and its flags) fresh per call, so
// flag values and cobra Changed markers cannot leak between tests regardless
// of execution order — no resetXFlags bookkeeping is required.
func execCLI(t *testing.T, args ...string) (string, error) {
	t.Helper()
	return execCLIStdin(t, nil, args...)
}

// execCLISplit runs the real CLI command tree with stdout and stderr captured
// separately, so a test can assert that a --json command emits ONLY the
// machine-readable envelope on stdout and routes human progress to stderr.
func execCLISplit(t *testing.T, args ...string) (stdout, stderr string, err error) {
	t.Helper()
	root := &cobra.Command{Use: "shinyhub", SilenceErrors: true}
	AddCommandsTo(root)
	var outBuf, errBuf bytes.Buffer
	root.SetOut(&outBuf)
	root.SetErr(&errBuf)
	for _, sub := range allSubcommands(root) {
		sub.SetOut(&outBuf)
		sub.SetErr(&errBuf)
	}
	root.SetArgs(args)
	err = root.Execute()
	return outBuf.String(), errBuf.String(), err
}

func allSubcommands(cmd *cobra.Command) []*cobra.Command {
	var all []*cobra.Command
	for _, sub := range cmd.Commands() {
		all = append(all, sub)
		all = append(all, allSubcommands(sub)...)
	}
	return all
}

// execCLIStdin is execCLI with an explicit stdin reader, for commands that
// read from cmd.InOrStdin() (e.g. the `apps delete` confirmation prompt).
// cobra propagates the root's In to every subcommand via InOrStdin, so setting
// it on the fresh root reaches the leaf without touching a package singleton.
func execCLIStdin(t *testing.T, stdin io.Reader, args ...string) (string, error) {
	t.Helper()
	root := &cobra.Command{Use: "shinyhub", SilenceErrors: true}
	AddCommandsTo(root)
	var buf bytes.Buffer
	forceWriters(root, &buf)
	if stdin != nil {
		root.SetIn(stdin)
	}
	root.SetArgs(args)
	err := root.Execute()
	return buf.String(), err
}
