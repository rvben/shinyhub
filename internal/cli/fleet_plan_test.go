package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestFleetPlan_ComputesDiffFromServer(t *testing.T) {
	_, reqs, setResp := setupCLITest(t)
	// Two server apps: one owned+unchanged, one owned+absent (delete).
	setResp(200, `[
	  {"slug":"alpha","access":"private","managed_by":"fleet:eu","content_digest":"sha256:LIVE"},
	  {"slug":"gone","access":"private","managed_by":"fleet:eu","content_digest":"sha256:x"}
	]`)

	dir := t.TempDir()
	mustWrite(t, filepath.Join(dir, "apps", "alpha", "app.py"), "print(1)\n")
	writeFleetManifest(t, dir, `
fleet_id = "eu"

[[app]]
slug = "alpha"
source = "./apps/alpha"
visibility = "private"

[[app]]
slug = "newone"
source = "./apps/alpha"
visibility = "public"
`)

	out, err := execCLI(t, "fleet", "plan", "-f", filepath.Join(dir, "shinyhub-fleet.toml"))
	if err != nil {
		t.Fatalf("unexpected error: %v\n%s", err, out)
	}
	// At least the apps list GET must have happened; never a mutation.
	sawApps := false
	for _, r := range *reqs {
		if r.Method != "GET" {
			t.Fatalf("plan must be read-only; saw %s %s", r.Method, r.Path)
		}
		if r.Path == "/api/apps" {
			sawApps = true
		}
	}
	if !sawApps {
		t.Fatal("expected a GET /api/apps call")
	}
	// Rendering lands in Task 9; here we only assert it ran without mutating.
	if !strings.Contains(out, "alpha") {
		t.Fatalf("expected diff to mention alpha:\n%s", out)
	}
}

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
