package cli

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"
)

func TestFleetPreflight_ReturnsDiffSourcesAndHost(t *testing.T) {
	_, _, setResp := setupCLITest(t)
	setResp(200, `[{"slug":"ops","access":"private","managed_by":"fleet:eu","content_digest":"sha256:LIVE"}]`)
	dir := t.TempDir()
	mustWrite(t, filepath.Join(dir, "a", "app.py"), "print(1)\n")
	writeFleetManifest(t, dir, "fleet_id=\"eu\"\n\n[[app]]\nslug=\"ops\"\nsource=\"./a\"\nvisibility=\"private\"\n")

	var errBuf bytes.Buffer
	pf, err := fleetPreflight(filepath.Join(dir, "shinyhub-fleet.toml"), &errBuf, "plan", 0)
	if err != nil {
		t.Fatalf("unexpected error: %v (stderr=%q)", err, errBuf.String())
	}
	if pf.cleanup != nil {
		defer pf.cleanup()
	}
	if pf.manifest.FleetID != "eu" {
		t.Fatalf("fleet_id = %q", pf.manifest.FleetID)
	}
	if len(pf.diff) != 1 || pf.diff[0].Slug != "ops" {
		t.Fatalf("diff = %+v", pf.diff)
	}
	if pf.host == "" {
		t.Fatal("host empty")
	}
	if got := pf.sources["ops"]; got != filepath.Join(dir, "a") {
		t.Fatalf("sources[ops] = %q, want %q", got, filepath.Join(dir, "a"))
	}
}

func TestFleetPreflight_NoManifestHelpful(t *testing.T) {
	_, _, _ = setupCLITest(t)
	dir := t.TempDir()
	var errBuf bytes.Buffer
	pf, err := fleetPreflight(filepath.Join(dir, "shinyhub-fleet.toml"), &errBuf, "plan", 0)
	if pf != nil {
		t.Fatalf("pf must be nil on failure, got %+v", pf)
	}
	if err == nil || exitCode(err) != 1 {
		t.Fatalf("want exit 1, got err=%v code=%d", err, exitCode(err))
	}
	if !strings.Contains(errBuf.String(), "fleet init") {
		t.Fatalf("helpful guidance not printed to errOut: %q", errBuf.String())
	}
}

func TestFleetPreflight_CmdNameInHeader(t *testing.T) {
	_, _, _ = setupCLITest(t)
	dir := t.TempDir()
	// Malformed manifest forces the validating-header path.
	writeFleetManifest(t, dir, "fleet_id = \n")
	var errBuf bytes.Buffer
	_, err := fleetPreflight(filepath.Join(dir, "shinyhub-fleet.toml"), &errBuf, "apply", 0)
	if err == nil || exitCode(err) != 1 {
		t.Fatalf("want exit 1, got %v", err)
	}
	if !strings.Contains(errBuf.String(), "shinyhub fleet apply: validating") {
		t.Fatalf("header did not use cmdName: %q", errBuf.String())
	}
}
