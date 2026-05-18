package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeFleetManifest(t *testing.T, dir, content string) string {
	t.Helper()
	p := filepath.Join(dir, "shinyhub-fleet.toml")
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestFleetPlan_NoManifestIsHelpful(t *testing.T) {
	_, _, _ = setupCLITest(t)
	dir := t.TempDir()
	out, err := execCLI(t, "fleet", "plan", "-f", filepath.Join(dir, "shinyhub-fleet.toml"))
	if err == nil {
		t.Fatal("expected an error when no manifest exists")
	}
	if exitCode(err) != 1 {
		t.Fatalf("exit code = %d, want 1", exitCode(err))
	}
	if !strings.Contains(out, "fleet init") || !strings.Contains(out, "-f") {
		t.Fatalf("no-manifest message not helpful:\n%s", out)
	}
}

func TestFleetPlan_PreflightAggregatesAllProblems(t *testing.T) {
	_, _, _ = setupCLITest(t)
	dir := t.TempDir()
	writeFleetManifest(t, dir, `
[[app]]
slug = "dup"
source = "./missing-a"

[[app]]
slug = "dup"
source = "./missing-b"
visibility = "nope"
`)
	out, err := execCLI(t, "fleet", "plan", "-f", filepath.Join(dir, "shinyhub-fleet.toml"))
	if err == nil {
		t.Fatal("expected pre-flight failure")
	}
	if exitCode(err) != 1 {
		t.Fatalf("exit = %d, want 1", exitCode(err))
	}
	for _, want := range []string{
		"fleet_id is required",
		`duplicate slug "dup"`,
		`invalid visibility "nope"`,
		"not found",
		"Nothing was changed",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("pre-flight output missing %q:\n%s", want, out)
		}
	}
}
