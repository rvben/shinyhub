package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

func TestFleetOmissionWarning_NamesEveryConsumerAndDestination(t *testing.T) {
	root := t.TempDir()
	appDir := filepath.Join(root, "app")
	if err := os.Mkdir(appDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFleetOmissionManifest(t, filepath.Join(root, defaultFleetManifest), `
fleet_id = "analytics"

[[bundle_file]]
from = "_shared/theme.py"
to = "helpers/theme.py"
consumers = ["sales", "ops"]

[[bundle_file]]
from = "_shared/format.py"
to = "helpers/format.py"
consumers = ["ops"]

[[app]]
slug = "sales"
source = "./app"

[[app]]
slug = "ops"
source = "./app"
`)

	got := discoverFleetCompositionOmission(appDir)
	if got == nil {
		t.Fatal("expected discoverable fleet composition omission")
	}
	if strings.Join(got.Consumers, ",") != "ops,sales" {
		t.Fatalf("consumers = %v, want ops,sales", got.Consumers)
	}
	if strings.Join(got.Destinations, ",") != "helpers/format.py,helpers/theme.py" {
		t.Fatalf("destinations = %v", got.Destinations)
	}
}

func TestFleetOmissionWarning_UsesNearestManifestAndModernPrecedence(t *testing.T) {
	root := t.TempDir()
	appDir := filepath.Join(root, "team", "app")
	if err := os.MkdirAll(appDir, 0o755); err != nil {
		t.Fatal(err)
	}
	// A matching ancestor must not override the nearer manifest.
	writeFleetOmissionManifest(t, filepath.Join(root, defaultFleetManifest), omissionManifest("ancestor", "./team/app", "ancestor.py"))
	team := filepath.Join(root, "team")
	writeFleetOmissionManifest(t, filepath.Join(team, legacyFleetManifest), omissionManifest("legacy", "./app", "legacy.py"))
	writeFleetOmissionManifest(t, filepath.Join(team, defaultFleetManifest), omissionManifest("modern", "./app", "modern.py"))

	got := discoverFleetCompositionOmission(appDir)
	if got == nil {
		t.Fatal("expected discoverable fleet composition omission")
	}
	wantManifest, err := filepath.EvalSymlinks(filepath.Join(team, defaultFleetManifest))
	if err != nil {
		t.Fatal(err)
	}
	if got.Manifest != wantManifest {
		t.Fatalf("manifest = %q, want nearest modern manifest", got.Manifest)
	}
	if strings.Join(got.Destinations, ",") != "modern.py" {
		t.Fatalf("destinations = %v, want modern declaration only", got.Destinations)
	}
}

func TestFleetOmissionWarning_CommandGuidanceAndQuiet(t *testing.T) {
	root := t.TempDir()
	appDir := filepath.Join(root, "app")
	if err := os.Mkdir(appDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFleetOmissionManifest(t, filepath.Join(root, defaultFleetManifest), omissionManifest("sales", "./app", "helpers/theme.py"))

	for _, tc := range []struct {
		kind fleetOmissionCommand
		want string
	}{
		{fleetOmissionRun, "shinyhub fleet dev sales"},
		{fleetOmissionPlan, "shinyhub fleet plan -f"},
		{fleetOmissionDeploy, "shinyhub fleet apply -f"},
	} {
		t.Run(string(tc.kind), func(t *testing.T) {
			var stderr bytes.Buffer
			warnFleetCompositionOmission(&stderr, appDir, tc.kind, false)
			text := stderr.String()
			for _, want := range []string{"warning:", "sales", "helpers/theme.py", tc.want} {
				if !strings.Contains(text, want) {
					t.Fatalf("warning missing %q:\n%s", want, text)
				}
			}
			if strings.Count(text, "\n") != 1 {
				t.Fatalf("warning must be one line: %q", text)
			}
		})
	}

	var quiet bytes.Buffer
	warnFleetCompositionOmission(&quiet, appDir, fleetOmissionDeploy, true)
	if quiet.Len() != 0 {
		t.Fatalf("quiet warning = %q, want empty", quiet.String())
	}
}

func TestFleetOmissionWarning_IsHookedIntoPlainCommands(t *testing.T) {
	root := t.TempDir()
	appDir := filepath.Join(root, "app")
	if err := os.Mkdir(appDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFleetOmissionManifest(t, filepath.Join(root, defaultFleetManifest), omissionManifest("sales", "./app", "helpers/theme.py"))

	// Each command is made to stop shortly after source selection. The warning
	// must already be present, without requiring a server or starting an app.
	if err := os.WriteFile(filepath.Join(appDir, ".env"), []byte("not-an-assignment\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name string
		cmd  func() *cobra.Command
	}{
		{name: "run", cmd: newRunCmd},
		{name: "plan", cmd: newPlanCmd},
		{name: "deploy", cmd: newDeployCmd},
	}

	oldConfigOverride := configPathOverride
	configPathOverride = filepath.Join(root, "missing-config.json")
	t.Cleanup(func() { configPathOverride = oldConfigOverride })
	t.Setenv("SHINYHUB_HOST", "")
	t.Setenv("SHINYHUB_TOKEN", "")

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cmd := tc.cmd()
			var stdout, stderr bytes.Buffer
			cmd.SetOut(&stdout)
			cmd.SetErr(&stderr)
			cmd.SetArgs([]string{appDir})
			_ = cmd.Execute()
			if !strings.Contains(stderr.String(), "omits fleet bundle inputs") {
				t.Fatalf("plain %s did not emit omission warning:\n%s", tc.name, stderr.String())
			}
			if strings.Contains(stdout.String(), "omits fleet bundle inputs") {
				t.Fatalf("plain %s polluted machine-readable stdout:\n%s", tc.name, stdout.String())
			}
		})
	}
}

func TestFleetOmissionWarning_BestEffortHasNoFalsePositives(t *testing.T) {
	for _, tc := range []struct {
		name     string
		manifest string
	}{
		{name: "no manifest"},
		{name: "malformed", manifest: "fleet_id = [\n[[bundle_file]]\n"},
		{name: "unrelated local app", manifest: omissionManifest("sales", "./another", "theme.py")},
		{name: "git app", manifest: `
fleet_id = "analytics"
[[bundle_file]]
from = "_shared/theme.py"
to = "theme.py"
consumers = ["sales"]
[[app]]
slug = "sales"
source = "git+https://example.com/acme/sales.git"
`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			appDir := filepath.Join(root, "app")
			if err := os.Mkdir(appDir, 0o755); err != nil {
				t.Fatal(err)
			}
			if strings.Contains(tc.manifest, "./another") {
				if err := os.Mkdir(filepath.Join(root, "another"), 0o755); err != nil {
					t.Fatal(err)
				}
			}
			if tc.manifest != "" {
				writeFleetOmissionManifest(t, filepath.Join(root, defaultFleetManifest), tc.manifest)
			}
			if got := discoverFleetCompositionOmission(appDir); got != nil {
				t.Fatalf("unexpected warning discovery: %+v", got)
			}
		})
	}
}

func writeFleetOmissionManifest(t *testing.T, path, contents string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		t.Fatal(err)
	}
}

func omissionManifest(slug, source, destination string) string {
	return `
fleet_id = "analytics"
[[bundle_file]]
from = "_shared/theme.py"
to = "` + destination + `"
consumers = ["` + slug + `"]
[[app]]
slug = "` + slug + `"
source = "` + source + `"
`
}
