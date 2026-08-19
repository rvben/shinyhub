package cli

import (
	"fmt"
	"os"
	"strings"
	"testing"
)

// unitPath is the packaged systemd unit, read from the CLI package because the
// exit-code contract it depends on is defined here.
const unitPath = "../../deploy/systemd/shinyhub.service"

// TestSystemdUnit_PreventsRestartOnSchemaIncompatible proves the shipped unit
// stops restarting when serve exits with the schema-incompatible code. Without
// it, Restart=on-failure retries a start that cannot succeed until the binary
// or the database changes, and the unit reports "activating (auto-restart)"
// rather than failed - so monitoring sees a healthy-looking unit forever.
func TestSystemdUnit_PreventsRestartOnSchemaIncompatible(t *testing.T) {
	code := 0
	for _, ki := range kindTable {
		if ki.Kind == KindSchemaIncompatible {
			code = ki.ExitCode
		}
	}
	if code == 0 {
		t.Fatal("kindTable has no exit code for KindSchemaIncompatible")
	}

	b, err := os.ReadFile(unitPath)
	if err != nil {
		t.Fatalf("read unit: %v", err)
	}
	unit := string(b)

	// The directive must name the code the binary actually exits with, so
	// renumbering the kind without updating the unit fails here.
	want := fmt.Sprintf("RestartPreventExitStatus=%d", code)
	if !strings.Contains(unit, want) {
		t.Errorf("unit must contain %q so a permanent schema failure stops the restart loop", want)
	}

	// Two bounds: the directive is worthless unless Restart= is actually on,
	// and it must sit in [Service] rather than a later section.
	if !strings.Contains(unit, "Restart=on-failure") {
		t.Error("unit no longer sets Restart=on-failure; re-check that RestartPreventExitStatus still applies")
	}
	if idx := strings.Index(unit, want); idx >= 0 {
		if install := strings.Index(unit, "[Install]"); install >= 0 && idx > install {
			t.Error("RestartPreventExitStatus must be in [Service], not after [Install]")
		}
	}
}
