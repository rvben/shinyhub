package cli

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

// fleet validate is the fleet-level analogue of `manifest validate`: it checks a
// shinyhub-fleet.toml entirely offline (no server, no token, no network) so a
// malformed manifest fails in a pre-merge CI gate instead of at apply time.

// A well-formed manifest whose local sources all resolve validates cleanly and
// exits 0, without contacting any server (no config file, no host env).
func TestFleetValidate_GoodManifestOffline(t *testing.T) {
	dir := t.TempDir()
	mustWrite(t, filepath.Join(dir, "apps", "ops", "app.py"), "print(1)\n")
	mustWrite(t, filepath.Join(dir, "apps", "web", "app.py"), "print(2)\n")
	writeFleetManifest(t, dir,
		"fleet_id=\"eu\"\n\n"+
			"[[app]]\nslug=\"ops\"\nsource=\"./apps/ops\"\n\n"+
			"[[app]]\nslug=\"web\"\nsource=\"./apps/web\"\n")

	// Prove offline operation: empty HOME (no config.json) and no server env.
	t.Setenv("HOME", t.TempDir())
	t.Setenv("SHINYHUB_HOST", "")
	t.Setenv("SHINYHUB_TOKEN", "")

	out, err := execCLI(t, "fleet", "validate", "-f", filepath.Join(dir, "shinyhub-fleet.toml"))
	if err != nil {
		t.Fatalf("expected valid manifest to pass offline, got %v\n%s", err, out)
	}
	low := strings.ToLower(out)
	if !strings.Contains(low, "ok") && !strings.Contains(low, "valid") {
		t.Errorf("expected a success indicator in output, got:\n%s", out)
	}
}

// A duplicate slug is a hard error: exit 1 naming the offending slug.
func TestFleetValidate_DuplicateSlug(t *testing.T) {
	dir := t.TempDir()
	mustWrite(t, filepath.Join(dir, "a", "app.py"), "x\n")
	writeFleetManifest(t, dir,
		"fleet_id=\"eu\"\n\n"+
			"[[app]]\nslug=\"dup\"\nsource=\"./a\"\n\n"+
			"[[app]]\nslug=\"dup\"\nsource=\"./a\"\n")

	out, err := execCLI(t, "fleet", "validate", "-f", filepath.Join(dir, "shinyhub-fleet.toml"))
	if err == nil || exitCode(err) != 1 {
		t.Fatalf("want exit 1, got err=%v code=%d\n%s", err, exitCode(err), out)
	}
	if !strings.Contains(strings.ToLower(out), "duplicate slug") {
		t.Errorf("expected a 'duplicate slug' message, got:\n%s", out)
	}
}

// A source that does not resolve to a local directory is reported against its
// app, exit 1. ParseSource (shared with apply) supplies the diagnosis.
func TestFleetValidate_MissingSourceDir(t *testing.T) {
	dir := t.TempDir()
	writeFleetManifest(t, dir,
		"fleet_id=\"eu\"\n\n[[app]]\nslug=\"ops\"\nsource=\"./does-not-exist\"\n")

	out, err := execCLI(t, "fleet", "validate", "-f", filepath.Join(dir, "shinyhub-fleet.toml"))
	if err == nil || exitCode(err) != 1 {
		t.Fatalf("want exit 1, got err=%v code=%d\n%s", err, exitCode(err), out)
	}
	if !strings.Contains(out, "ops") {
		t.Errorf("expected the offending slug 'ops' in output, got:\n%s", out)
	}
}

// A local source whose per-app shinyhub.toml is invalid fails validation,
// delegating to the same parser `manifest validate` uses.
func TestFleetValidate_BadPerAppManifest(t *testing.T) {
	dir := t.TempDir()
	mustWrite(t, filepath.Join(dir, "a", "app.py"), "x\n")
	mustWrite(t, filepath.Join(dir, "a", "shinyhub.toml"), "[app]\nbogus_key = 1\n")
	writeFleetManifest(t, dir,
		"fleet_id=\"eu\"\n\n[[app]]\nslug=\"ops\"\nsource=\"./a\"\n")

	out, err := execCLI(t, "fleet", "validate", "-f", filepath.Join(dir, "shinyhub-fleet.toml"))
	if err == nil || exitCode(err) != 1 {
		t.Fatalf("want exit 1 for a bad per-app shinyhub.toml, got err=%v code=%d\n%s", err, exitCode(err), out)
	}
	if !strings.Contains(out, "ops") {
		t.Errorf("expected the offending slug 'ops' in output, got:\n%s", out)
	}
}

// A missing manifest file is a usage error (exit 1), not a crash.
func TestFleetValidate_MissingManifest(t *testing.T) {
	dir := t.TempDir()
	out, err := execCLI(t, "fleet", "validate", "-f", filepath.Join(dir, "shinyhub-fleet.toml"))
	if err == nil || exitCode(err) != 1 {
		t.Fatalf("want exit 1, got err=%v code=%d\n%s", err, exitCode(err), out)
	}
}

// validate is registered under the fleet command tree.
func TestFleetValidate_Registered(t *testing.T) {
	root := &cobra.Command{Use: "root"}
	AddCommandsTo(root)
	var fleetCmd *cobra.Command
	for _, c := range root.Commands() {
		if c.Name() == "fleet" {
			fleetCmd = c
		}
	}
	if fleetCmd == nil {
		t.Fatal("fleet command not registered with root")
	}
	for _, c := range fleetCmd.Commands() {
		if c.Name() == "validate" {
			return
		}
	}
	t.Error("expected 'validate' subcommand under fleet")
}
