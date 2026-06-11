package cli

import (
	"bytes"
	"encoding/json"
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
	root := &cobra.Command{Use: "shinyhub", SilenceErrors: true} // matches rootCmd: Report() owns all error output
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

// ERR-6: silencing cobra's own error line (to kill the duplicate) must not also
// swallow flag-parse errors. Those happen before RunE, so the dedupe wrapper
// never sees them; the user must still be told what flag was wrong.
func TestFleet_FlagParseErrorIsStillReported(t *testing.T) {
	_, _, _ = setupCLITest(t)
	out := execFleetRealRoot(t, "fleet", "plan", "--nonexistent-flag")
	if !strings.Contains(out, "unknown flag") {
		t.Fatalf("a flag-parse error must still reach the user, got:\n%q", out)
	}
}

// ERR-6: a flag error on the fleet parent must appear exactly once. With
// SilenceErrors set globally, cobra never prints flag errors itself, so the
// FlagErrorFunc is the sole printer and printing unconditionally cannot
// double-report.
func TestFleet_ParentFlagParseErrorNotDuplicated(t *testing.T) {
	_, _, _ = setupCLITest(t)
	out := execFleetRealRoot(t, "fleet", "--nonexistent-flag")
	if !strings.Contains(out, "unknown flag") {
		t.Fatalf("parent flag-parse error must reach the user, got:\n%q", out)
	}
	if n := strings.Count(out, "unknown flag"); n != 1 {
		t.Fatalf("parent flag error must appear exactly once, got %d:\n%s", n, out)
	}
}

// ERR-6: fleet FlagErrorFunc prose + reportTo envelope in TTY table mode.
// The fleet FlagErrorFunc prints "error: ..." to stderr then returns the
// wrapped error. When main() then calls reportTo with TTY=true + table format,
// it must NOT print a second prose line (Reported must be true on the error).
// Combined stderr must contain "error:" exactly once; the envelope is last.
func TestFleet_FlagParseErrorExactlyOneProseAndEnvelope(t *testing.T) {
	_, _, _ = setupCLITest(t)
	resetFormatState(t)

	root := &cobra.Command{Use: "shinyhub", SilenceErrors: true}
	AddCommandsTo(root)
	var stderr bytes.Buffer
	forceWriters(root, &bytes.Buffer{}) // stdout goes nowhere
	root.SetErr(&stderr)
	for _, sub := range allSubcommands(root) {
		sub.SetErr(&stderr)
	}
	root.SetArgs([]string{"fleet", "plan", "--nonexistent-flag"})
	runErr := root.Execute()

	// Pipe the error through reportTo with TTY=true + table (the duplicate path).
	reportTo(&stderr, true, formatTable, runErr)

	out := stderr.String()
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")

	// The last line must be a JSON envelope.
	lastLine := lines[len(lines)-1]
	var env map[string]any
	if jerr := json.Unmarshal([]byte(lastLine), &env); jerr != nil {
		t.Fatalf("last stderr line must be the JSON envelope, got: %q\nfull: %s", lastLine, out)
	}

	// There must be exactly one non-envelope line (the FlagErrorFunc prose).
	// reportTo must NOT add a second prose line because Reported is set.
	proseLines := lines[:len(lines)-1]
	if len(proseLines) != 1 {
		t.Errorf("want exactly 1 prose line before the envelope, got %d:\n%s", len(proseLines), out)
	}
}

// ERR-6: root FlagErrorFunc must NOT print prose (root.go wraps and returns
// without printing). reportTo then produces exactly one prose line in TTY
// table mode. Verify the combined output has exactly one prose line.
func TestRoot_FlagParseErrorExactlyOneProseViaReport(t *testing.T) {
	_, _, _ = setupCLITest(t)
	resetFormatState(t)

	root := &cobra.Command{Use: "shinyhub", SilenceErrors: true}
	AddCommandsTo(root)
	var stderr bytes.Buffer
	forceWriters(root, &bytes.Buffer{})
	root.SetErr(&stderr)
	for _, sub := range allSubcommands(root) {
		sub.SetErr(&stderr)
	}
	root.SetArgs([]string{"apps", "list", "--nonexistent-flag"})
	runErr := root.Execute()

	// Report through reportTo (TTY=true, table) to enable prose.
	reportTo(&stderr, true, formatTable, runErr)

	out := stderr.String()
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	// Last line must be the envelope.
	lastLine := lines[len(lines)-1]
	var env map[string]any
	if jerr := json.Unmarshal([]byte(lastLine), &env); jerr != nil {
		t.Fatalf("last line must be the JSON envelope, got: %q\nfull: %s", lastLine, out)
	}
	// There must be exactly one prose line (from reportTo only, FlagErrorFunc silent).
	proseLines := lines[:len(lines)-1]
	if len(proseLines) != 1 {
		t.Errorf("want exactly 1 prose line before the envelope, got %d:\n%s", len(proseLines), out)
	}
}
