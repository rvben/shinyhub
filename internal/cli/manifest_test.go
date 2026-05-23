package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

// writeManifest writes a shinyhub.toml into a fresh temp dir and returns the dir.
func writeManifest(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "shinyhub.toml"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

// DEP-1: `shinyhub manifest validate <dir>` parses shinyhub.toml locally so a
// typo is caught before upload. A well-formed manifest reports a clean summary
// and exits 0.
func TestManifestValidate_ValidManifest(t *testing.T) {
	dir := writeManifest(t, `
[app]
replicas = 2

[[hook]]
on = "post-deploy"
command = ["Rscript", "setup.R"]

[[schedule]]
name = "nightly"
cron = "0 2 * * *"
cmd = "Rscript run.R"
`)

	var out bytes.Buffer
	cmd := newManifestCmd()
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"validate", dir})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("expected valid manifest to pass, got: %v\n%s", err, out.String())
	}
	got := out.String()
	if !strings.Contains(strings.ToLower(got), "ok") && !strings.Contains(strings.ToLower(got), "valid") {
		t.Errorf("expected a success indicator in output, got:\n%s", got)
	}
	if !strings.Contains(got, "nightly") {
		t.Errorf("expected schedule name in summary, got:\n%s", got)
	}
}

// DEP-1: a manifest with an unknown field is rejected with a clear error
// (strict-mode parse), and the command exits non-zero.
func TestManifestValidate_UnknownFieldErrors(t *testing.T) {
	dir := writeManifest(t, `
[app]
replicas = 2
typo_field = "oops"
`)

	var out bytes.Buffer
	cmd := newManifestCmd()
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"validate", dir})
	err := cmd.Execute()
	if err == nil {
		t.Fatal("expected error for unknown manifest field, got nil")
	}
	if !strings.Contains(err.Error(), "typo_field") {
		t.Errorf("error should name the offending field, got: %v", err)
	}
}

// DEP-1: validating a directory with no shinyhub.toml is not an error — the
// manifest is optional — but the command says so plainly.
func TestManifestValidate_NoManifest(t *testing.T) {
	dir := t.TempDir()

	var out bytes.Buffer
	cmd := newManifestCmd()
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"validate", dir})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("absent manifest must not be an error, got: %v", err)
	}
	if !strings.Contains(strings.ToLower(out.String()), "no shinyhub.toml") {
		t.Errorf("expected a 'no shinyhub.toml' note, got:\n%s", out.String())
	}
}

// DEP-1: validate defaults to the current directory when no path is given.
func TestManifestValidate_DefaultsToCwd(t *testing.T) {
	dir := writeManifest(t, `
[[schedule]]
name = "job"
cron = "0 * * * *"
cmd = "echo hi"
`)
	t.Chdir(dir)

	var out bytes.Buffer
	cmd := newManifestCmd()
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"validate"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("expected cwd manifest to validate, got: %v\n%s", err, out.String())
	}
}

// TestManifestCmd_RegisteredWithRoot verifies manifest is registered with root.
func TestManifestCmd_RegisteredWithRoot(t *testing.T) {
	root := &cobra.Command{Use: "root"}
	AddCommandsTo(root)
	found := false
	for _, c := range root.Commands() {
		if c.Use == "manifest" {
			found = true
			break
		}
	}
	if !found {
		t.Error("expected 'manifest' command to be registered with root")
	}
}
