package cli

import (
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

var updateGolden = flag.Bool("update-golden", false, "rewrite testdata golden files")

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
	// Assert the plan ran read-only (GET-only) and produced output.
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

func TestFleetPlan_HumanGolden(t *testing.T) {
	_, _, setResp := setupCLITest(t)
	setResp(200, `[
	  {"slug":"ops-metrics","access":"private","managed_by":"fleet:eu","content_digest":"sha256:LIVE"},
	  {"slug":"retired","access":"private","managed_by":"fleet:eu","content_digest":"sha256:zz"}
	]`)
	dir := t.TempDir()
	mustWrite(t, filepath.Join(dir, "a", "app.py"), "print(1)\n")
	writeFleetManifest(t, dir, `
fleet_id = "eu"

[[app]]
slug = "sales-explorer"
source = "./a"
visibility = "private"

[[app]]
slug = "ops-metrics"
source = "./a"
visibility = "private"
`)
	out, err := execCLI(t, "fleet", "plan", "-f", filepath.Join(dir, "shinyhub-fleet.toml"), "--no-color", "-o", "table")
	if err != nil {
		t.Fatalf("unexpected error: %v\n%s", err, out)
	}
	norm := normalizeDigests(out)
	golden := filepath.Join("testdata", "fleet_plan_basic.golden")
	if *updateGolden {
		if werr := os.WriteFile(golden, []byte(norm), 0o644); werr != nil {
			t.Fatal(werr)
		}
	}
	want, rerr := os.ReadFile(golden)
	if rerr != nil {
		t.Fatalf("read golden (run with -update-golden first): %v", rerr)
	}
	if norm != string(want) {
		t.Fatalf("golden mismatch:\n--- got ---\n%s\n--- want ---\n%s", norm, want)
	}
}

func TestFleetPlan_JSONEnvelope(t *testing.T) {
	_, _, setResp := setupCLITest(t)
	setResp(200, `[{"slug":"ops","access":"private","managed_by":"fleet:eu","content_digest":"sha256:LIVE"}]`)
	dir := t.TempDir()
	mustWrite(t, filepath.Join(dir, "a", "app.py"), "print(1)\n")
	writeFleetManifest(t, dir, "fleet_id = \"eu\"\n\n[[app]]\nslug=\"ops\"\nsource=\"./a\"\nvisibility=\"private\"\n")
	out, err := execCLI(t, "fleet", "plan", "-f", filepath.Join(dir, "shinyhub-fleet.toml"), "--json")
	if err != nil {
		t.Fatalf("unexpected error: %v\n%s", err, out)
	}
	var env map[string]any
	if jerr := json.Unmarshal([]byte(strings.TrimSpace(out)), &env); jerr != nil {
		t.Fatalf("output is not valid JSON: %v\n%s", jerr, out)
	}
	if env["schema_version"].(float64) != 1 {
		t.Fatalf("schema_version = %v, want 1", env["schema_version"])
	}
	if env["fleet_id"] != "eu" {
		t.Fatalf("fleet_id = %v", env["fleet_id"])
	}
	if _, ok := env["apps"]; !ok {
		t.Fatal("missing apps[]")
	}
}

// EXIT-1: under --json --detailed-exitcode the JSON summary must report the
// same exit code the process returns, not a hardcoded 0/"report only".
func TestFleetPlan_JSONExitCodeMirrorsDetailedExitcode(t *testing.T) {
	_, _, setResp := setupCLITest(t)
	setResp(200, `[{"slug":"ops","access":"private","managed_by":"fleet:eu","content_digest":"sha256:LIVE"}]`)
	dir := t.TempDir()
	mustWrite(t, filepath.Join(dir, "a", "app.py"), "print(1)\n")
	// A new app => pending changes.
	writeFleetManifest(t, dir, "fleet_id=\"eu\"\n\n[[app]]\nslug=\"newp\"\nsource=\"./a\"\nvisibility=\"private\"\n")

	out, err := execCLI(t, "fleet", "plan", "-f", filepath.Join(dir, "shinyhub-fleet.toml"), "--json", "--detailed-exitcode")
	if exitCode(err) != 2 {
		t.Fatalf("pending changes: process exit = %d, want 2", exitCode(err))
	}
	var env map[string]any
	if jerr := json.Unmarshal([]byte(strings.TrimSpace(out)), &env); jerr != nil {
		t.Fatalf("output is not valid JSON: %v\n%s", jerr, out)
	}
	summary, ok := env["summary"].(map[string]any)
	if !ok {
		t.Fatalf("missing summary object: %s", out)
	}
	if got := summary["exit_code"].(float64); got != 2 {
		t.Errorf("summary.exit_code = %v, want 2 (must mirror the process exit code)", got)
	}
	if reason, _ := summary["exit_reason"].(string); !strings.Contains(reason, "pending") {
		t.Errorf("summary.exit_reason = %q, want it to mention pending changes", reason)
	}
}

func TestFleetPlan_DetailedExitcode(t *testing.T) {
	_, _, setResp := setupCLITest(t)
	setResp(200, `[{"slug":"ops","access":"private","managed_by":"fleet:eu","content_digest":"sha256:LIVE"}]`)
	dir := t.TempDir()
	mustWrite(t, filepath.Join(dir, "a", "app.py"), "print(1)\n")

	// Pending changes (a new app) => exit 2 under --detailed-exitcode.
	writeFleetManifest(t, dir, "fleet_id=\"eu\"\n\n[[app]]\nslug=\"newp\"\nsource=\"./a\"\nvisibility=\"private\"\n")
	_, err := execCLI(t, "fleet", "plan", "-f", filepath.Join(dir, "shinyhub-fleet.toml"), "--detailed-exitcode")
	if exitCode(err) != 2 {
		t.Fatalf("pending changes: exit = %d, want 2", exitCode(err))
	}

	// No-op: only the already-live owned app, unchanged => exit 0.
	mustWrite(t, filepath.Join(dir, "a", "app.py"), "print(1)\n")
	dg, _ := digestLocalDir(filepath.Join(dir, "a"))
	setResp(200, `[{"slug":"ops","access":"private","managed_by":"fleet:eu","content_digest":"`+dg+`"}]`)
	writeFleetManifest(t, dir, "fleet_id=\"eu\"\n\n[[app]]\nslug=\"ops\"\nsource=\"./a\"\nvisibility=\"private\"\n")
	_, err = execCLI(t, "fleet", "plan", "-f", filepath.Join(dir, "shinyhub-fleet.toml"), "--detailed-exitcode")
	if exitCode(err) != 0 {
		t.Fatalf("no changes: exit = %d, want 0", exitCode(err))
	}
}

func TestFleetHelp_ListsPlanAndExitCodes(t *testing.T) {
	_, _, _ = setupCLITest(t)

	top, err := execCLI(t, "fleet", "--help")
	if err != nil {
		t.Fatalf("fleet --help error: %v", err)
	}
	if !strings.Contains(top, "plan") || !strings.Contains(top, "Example:") {
		t.Fatalf("fleet --help should list `plan` and an Example block:\n%s", top)
	}

	ph, err := execCLI(t, "fleet", "plan", "--help")
	if err != nil {
		t.Fatalf("fleet plan --help error: %v", err)
	}
	for _, want := range []string{"Exit codes:", "  0 ", "  1 ", "  2 ", "  3 ", "--detailed-exitcode", "Example:"} {
		if !strings.Contains(ph, want) {
			t.Fatalf("fleet plan --help missing %q:\n%s", want, ph)
		}
	}
}

// normalizeDigests replaces sha256:<hex> with sha256:XXXX and the ephemeral
// httptest server URL with a stable placeholder so golden output is stable.
func normalizeDigests(s string) string {
	s = regexpMustCompile(`sha256:[0-9a-f]+`).ReplaceAllString(s, "sha256:XXXX")
	s = regexpMustCompile(`server=http://[^\s]+`).ReplaceAllString(s, "server=http://SERVER")
	// The Next-block echoes the (absolute, t.TempDir) manifest path after -f;
	// collapse it to a stable placeholder so the golden is deterministic.
	s = regexpMustCompile(`-f \S+`).ReplaceAllString(s, "-f FLEET.toml")
	return s
}

func regexpMustCompile(p string) *regexp.Regexp { return regexp.MustCompile(p) }
