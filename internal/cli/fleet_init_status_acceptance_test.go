package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The setupCLITest fake returns the same body for every path. fleet plan
// makes GET /api/apps (consumes this array) and a best-effort
// GET /api/server-info (unmarshal into the caps wrapper simply fails on the
// array and yields zero caps - fine, plan does not use caps).
const acceptanceApps = `[
	{"slug":"web","access":"public","status":"running","managed_by":null},
	{"slug":"api","access":"private","status":"running","managed_by":null}
]`

func TestAcceptance_InitSourceRootRoundTripsThroughPlan(t *testing.T) {
	_, _, setResp := setupCLITest(t)
	setResp(200, acceptanceApps)

	dir := t.TempDir()
	manifest := filepath.Join(dir, "shinyhub-fleet.toml")
	// On-disk layout the design's one-command migration assumes:
	// <source-root>/<slug>/, each with at least one bundled file.
	for _, slug := range []string{"web", "api"} {
		appDir := filepath.Join(dir, "apps", slug)
		if err := os.MkdirAll(appDir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(appDir, "app.py"), []byte("print(1)\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	if out, err := execCLI(t, "fleet", "init", "--fleet-id", "prod-eu",
		"--source-root", "./apps", "-f", manifest); err != nil {
		t.Fatalf("init failed: %v\n%s", err, out)
	}

	// No manual edits: plan succeeds (exit 0, default) and shows both apps as
	// adopt (server managed_by is null => present but not owned).
	out, err := execCLI(t, "fleet", "plan", "-f", manifest)
	if err != nil {
		t.Fatalf("plan on generated manifest failed: %v\n%s", err, out)
	}
	if !strings.Contains(out, "web") || !strings.Contains(out, "api") {
		t.Fatalf("plan did not list both apps:\n%s", out)
	}
	if !strings.Contains(out, "adopt") {
		t.Fatalf("expected adopt actions for unowned apps:\n%s", out)
	}
	if strings.Contains(out, "source") && strings.Contains(out, "not found") {
		t.Fatalf("sources should resolve with no edits:\n%s", out)
	}
}

func TestAcceptance_InitNoSourceRootFailsPlanWithPreciseMessage(t *testing.T) {
	_, _, setResp := setupCLITest(t)
	setResp(200, acceptanceApps)

	dir := t.TempDir()
	manifest := filepath.Join(dir, "shinyhub-fleet.toml")
	if out, err := execCLI(t, "fleet", "init", "--fleet-id", "prod-eu", "-f", manifest); err != nil {
		t.Fatalf("init failed: %v\n%s", err, out)
	}

	out, err := execCLI(t, "fleet", "plan", "-f", manifest)
	if err == nil {
		t.Fatalf("plan must fail on the commented-source scaffold\n%s", out)
	}
	if code := exitCode(err); code != 1 {
		t.Fatalf("exit code = %d, want 1 (manifest validation)", code)
	}
	if !strings.Contains(out, "source is required") {
		t.Fatalf("plan must print the precise 'source is required' message, not a parse error:\n%s", out)
	}
	if strings.Contains(out, "TOML parse error") {
		t.Fatalf("must NOT be a confusing parse error:\n%s", out)
	}
}

func TestAcceptance_StatusNoManifestSegmentsOwnership(t *testing.T) {
	_, _, setResp := setupCLITest(t)
	setResp(200, `[
		{"slug":"owned","access":"public","status":"running","content_digest":"sha256:abcdef012345","managed_by":"fleet:prod-eu"},
		{"slug":"loose","access":"private","status":"stopped","managed_by":null}
	]`)

	out, err := execCLI(t, "fleet", "status", "-o", "table")
	if err != nil {
		t.Fatalf("status failed: %v\n%s", err, out)
	}
	if !strings.Contains(out, "owned") || !strings.Contains(out, "fleet:prod-eu") {
		t.Fatalf("managed app/owner missing:\n%s", out)
	}
	if !strings.Contains(out, "loose") || !strings.Contains(out, "unmanaged") {
		t.Fatalf("unmanaged app missing:\n%s", out)
	}
	if !strings.Contains(out, "1 fleet-managed, 1 unmanaged") {
		t.Fatalf("segment summary missing:\n%s", out)
	}
}
