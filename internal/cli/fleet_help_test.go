package cli

import (
	"strings"
	"testing"
)

func TestFleetHelp_ListsAllFourSubcommandsAndExample(t *testing.T) {
	out, err := execCLI(t, "fleet", "--help")
	if err != nil {
		t.Fatalf("fleet --help error: %v\n%s", err, out)
	}
	for _, sub := range []string{"init", "plan", "apply", "status"} {
		if !strings.Contains(out, sub) {
			t.Fatalf("fleet --help omits %q:\n%s", sub, out)
		}
	}
	// Worked example block present.
	if !strings.Contains(out, "shinyhub fleet init --fleet-id prod-eu --source-root ./apps") {
		t.Fatalf("fleet --help missing the worked init example:\n%s", out)
	}
	if !strings.Contains(out, "shinyhub fleet apply -f shinyhub-fleet.toml --prune --yes") {
		t.Fatalf("fleet --help missing the worked apply example:\n%s", out)
	}
}

func TestFleetSubcommandsCarryExitCodeTableAndExample(t *testing.T) {
	for _, sub := range []string{"init", "status"} {
		out, err := execCLI(t, "fleet", sub, "--help")
		if err != nil {
			t.Fatalf("fleet %s --help error: %v\n%s", sub, err, out)
		}
		if !strings.Contains(out, "Exit codes:") {
			t.Fatalf("fleet %s --help missing exit-code table:\n%s", sub, out)
		}
		if !strings.Contains(out, "Example:") {
			t.Fatalf("fleet %s --help missing example:\n%s", sub, out)
		}
	}
}
