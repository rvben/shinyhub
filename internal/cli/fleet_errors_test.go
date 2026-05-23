package cli

import (
	"bytes"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

// execFleetRealRoot runs a fleet command through a root that does NOT silence
// errors, mirroring the shipped cmd/shinyhub binary. This is what surfaces the
// ERR-6 duplicate (the command writes its own guidance, then cobra reprints the
// error as a generic "Error:" line). Returns combined stdout+stderr.
func execFleetRealRoot(t *testing.T, args ...string) string {
	t.Helper()
	root := &cobra.Command{Use: "shinyhub"} // no SilenceErrors, like the real binary
	AddCommandsTo(root)
	var buf bytes.Buffer
	forceWriters(root, &buf)
	root.SetArgs(args)
	_ = root.Execute()
	return buf.String()
}

// ERR-6: a fleet command that already printed contextual guidance must not also
// trigger cobra's generic "Error:" line.
func TestFleet_ReportedErrorNotDuplicatedByCobra(t *testing.T) {
	_, _, _ = setupCLITest(t)
	out := execFleetRealRoot(t, "fleet", "plan", "-f", "does-not-exist.toml")
	if !strings.Contains(out, "fleet init") {
		t.Fatalf("expected the helpful no-manifest guidance:\n%s", out)
	}
	if strings.Contains(out, "Error:") {
		t.Fatalf("cobra's duplicate \"Error:\" line must be suppressed:\n%s", out)
	}
}

// ERR-6: an error path with no contextual message of its own must still reach
// the user exactly once (the wrapper prints it; cobra stays silent).
func TestFleet_UnreportedErrorPrintedOnce(t *testing.T) {
	_, _, _ = setupCLITest(t)
	// Invalid fleet-id is rejected before any server call and prints no message
	// of its own.
	out := execFleetRealRoot(t, "fleet", "init", "--fleet-id", "Bad ID!")
	if !strings.Contains(out, "invalid") {
		t.Fatalf("expected the invalid fleet-id message:\n%s", out)
	}
	if strings.Contains(out, "Error:") {
		t.Fatalf("must not emit cobra's \"Error:\" line (the wrapper owns printing):\n%s", out)
	}
	if n := strings.Count(out, "invalid"); n != 1 {
		t.Fatalf("invalid-id message must appear exactly once, got %d:\n%s", n, out)
	}
}
