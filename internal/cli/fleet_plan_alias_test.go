package cli

import (
	"path/filepath"
	"testing"
)

// TestFleetPlan_FailOnChangesAlias verifies --fail-on-changes behaves like
// --detailed-exitcode (exit 2 on pending changes), giving CI a flag whose name
// states the intent instead of requiring Terraform vocabulary.
func TestFleetPlan_FailOnChangesAlias(t *testing.T) {
	_, _, setResp := setupCLITest(t)
	setResp(200, `[{"slug":"ops","access":"private","managed_by":"fleet:eu","content_digest":"sha256:LIVE"}]`)
	dir := t.TempDir()
	mustWrite(t, filepath.Join(dir, "a", "app.py"), "print(1)\n")
	writeFleetManifest(t, dir, "fleet_id=\"eu\"\n\n[[app]]\nslug=\"newp\"\nsource=\"./a\"\nvisibility=\"private\"\n")

	_, err := execCLI(t, "fleet", "plan", "-f", filepath.Join(dir, "shinyhub-fleet.toml"), "--fail-on-changes")
	if exitCode(err) != 2 {
		t.Fatalf("pending changes with --fail-on-changes: exit = %d, want 2", exitCode(err))
	}
}
