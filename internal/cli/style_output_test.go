package cli

import (
	"bytes"
	"strings"
	"testing"
)

// tableCommands is every read command that renders a human table, with the
// fixture arguments bootContractServer seeds.
var tableCommands = [][]string{
	{"apps", "list"},
	{"apps", "show", "demo"},
	{"apps", "deployments", "demo"},
	{"apps", "access", "list", "demo"},
	{"apps", "access", "group-list", "demo"},
	{"tokens", "list"},
	{"env", "ls", "demo"},
	{"data", "ls", "demo"},
	{"schedule", "ls", "demo"},
	{"schedule", "runs", "demo", "nightly"},
	{"schedule", "status", "demo"},
	{"share", "ls", "demo"},
	{"users", "list"},
	{"fleet", "status"},
}

// TestTableOutputHasNoANSIWhenNotATerminal drives every table-rendering command
// end to end against a real server, with the force-color variables set, and
// asserts the captured output carries no escape sequence.
//
// This is the guarantee the styling layer rests on, checked at the surface
// rather than at the styler: a script parsing `shinyhub apps list`, a log file,
// or a diff of captured output must be byte-identical to what this CLI printed
// before there was any color. FORCE_COLOR is set precisely because it is the
// one thing that can override the terminal test - it must still not reach a
// writer that is not a terminal.
func TestTableOutputHasNoANSIWhenNotATerminal(t *testing.T) {
	t.Setenv("FORCE_COLOR", "1")
	t.Setenv("CLICOLOR_FORCE", "1")
	host, token := bootContractServer(t)

	for _, args := range tableCommands {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			full := append(append([]string{}, args...), "-o", "table")
			out := runContractCLI(t, host, token, full...)
			if out == "" {
				t.Fatalf("no output; the command rendered nothing to assert on")
			}
			if i := strings.IndexByte(out, 0x1b); i >= 0 {
				t.Errorf("output carries an ANSI escape at byte %d:\n%q", i, out)
			}
		})
	}
}

// TestANSIDetectorSeesAnEscape is the positive control for the sweep above: the
// same check, run against output that IS painted, must fail. Without it, a
// detector that never fires would report every command clean.
func TestANSIDetectorSeesAnEscape(t *testing.T) {
	var buf bytes.Buffer
	newTable("SLUG", "STATUS").
		row(txt("demo"), statusTxt("running")).
		renderWith(&buf, styler{color: true})

	if strings.IndexByte(buf.String(), 0x1b) < 0 {
		t.Errorf("painted render carries no escape, so the sweep's check proves nothing: %q", buf.String())
	}
}
