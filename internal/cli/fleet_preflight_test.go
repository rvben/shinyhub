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

// The seam the unit tests cannot reach: a real GET /api/apps payload mapped
// into fleet.ObservedApp. A rename on the server must surface as name drift,
// and an app the server reports with no description at all must NOT drift
// against a declared "" - otherwise every plan would show a phantom clear.
func TestFleetPreflight_MapsNameAndDescriptionDrift(t *testing.T) {
	_, _, setResp := setupCLITest(t)
	setResp(200, `[{"slug":"ops","name":"Renamed In UI","access":"private","managed_by":"fleet:eu","content_digest":"sha256:LIVE"}]`)
	dir := t.TempDir()
	mustWrite(t, filepath.Join(dir, "a", "app.py"), "print(1)\n")
	writeFleetManifest(t, dir, "fleet_id=\"eu\"\n\n[[app]]\nslug=\"ops\"\nsource=\"./a\"\nvisibility=\"private\"\n\n  [app.config]\n  name = \"Ops Dashboard\"\n  description = \"\"\n")

	var errBuf bytes.Buffer
	pf, err := fleetPreflight(filepath.Join(dir, "shinyhub-fleet.toml"), &errBuf, "plan", 0)
	if err != nil {
		t.Fatalf("unexpected error: %v (stderr=%q)", err, errBuf.String())
	}
	if pf.cleanup != nil {
		defer pf.cleanup()
	}
	if len(pf.diff) != 1 {
		t.Fatalf("diff = %+v, want 1 app", pf.diff)
	}
	drift := map[string]string{}
	for _, c := range pf.diff[0].ConfigDrift {
		drift[c.Key] = c.Server + " -> " + c.Desired
	}
	if got := drift["name"]; got != `"Renamed In UI" -> "Ops Dashboard"` {
		t.Errorf("name drift = %q, want the server rename reverting to the manifest", got)
	}
	if got, present := drift["description"]; present {
		t.Errorf(`declared "" vs an app with no description must not drift, got %q`, got)
	}
}

func TestFleetPreflight_MapsProjectSlugAndBundleWorkerDrift(t *testing.T) {
	_, _, setResp := setupCLITest(t)
	setResp(200, `[{"slug":"ops","project_slug":"analytics","worker_isolation":"multiplex","worker_grouped_size":6,"worker_max_workers":40,"access":"private","managed_by":"fleet:eu","content_digest":"sha256:LIVE"}]`)
	dir := t.TempDir()
	mustWrite(t, filepath.Join(dir, "a", "app.py"), "print(1)\n")
	mustWrite(t, filepath.Join(dir, "a", "shinyhub.toml"), "[app.worker]\nisolation=\"grouped\"\ngrouped_size=6\nmax_workers=40\n")
	writeFleetManifest(t, dir, "fleet_id=\"eu\"\n\n[[app]]\nslug=\"ops\"\nsource=\"./a\"\nvisibility=\"private\"\n\n  [app.config]\n  project=\"analytics\"\n")

	var errBuf bytes.Buffer
	pf, err := fleetPreflight(filepath.Join(dir, "shinyhub-fleet.toml"), &errBuf, "plan", 0)
	if err != nil {
		t.Fatalf("preflight: %v (%s)", err, errBuf.String())
	}
	defer pf.cleanup()
	keys := map[string]bool{}
	for _, d := range pf.diff[0].ConfigDrift {
		keys[d.Key] = true
	}
	if keys["project"] {
		t.Fatalf("stored project_slug must round-trip, drift=%+v", pf.diff[0].ConfigDrift)
	}
	if !keys["worker_isolation"] {
		t.Fatalf("bundle worker drift missing: %+v", pf.diff[0].ConfigDrift)
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
