package deploy

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/rvben/shinyhub/internal/process"
)

func TestBuildCommand_AutoInstrumentWrapsRequirementsMode(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "requirements.txt"), []byte("shiny\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	got := buildCommand(dir, 41000, 1, "127.0.0.1", true)
	want := []string{
		"uv", "run", "--no-project", "--with-requirements", "requirements.txt",
		"--with", "opentelemetry-distro",
		"--with", "opentelemetry-exporter-otlp",
		"--with", "opentelemetry-instrumentation-starlette",
		"--with", "opentelemetry-instrumentation-requests",
		"--with", "opentelemetry-instrumentation-httpx",
		"opentelemetry-instrument",
		"shiny", "run", "app.py", "--host", "127.0.0.1", "--port", "41000",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("buildCommand =\n  %q\nwant\n  %q", got, want)
	}
}

func TestBuildCommand_AutoInstrumentWrapsProjectMode(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "pyproject.toml"), []byte("[project]\nname='x'\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	got := buildCommand(dir, 41000, 1, "127.0.0.1", true)
	want := []string{
		"uv", "run",
		"--with", "opentelemetry-distro",
		"--with", "opentelemetry-exporter-otlp",
		"--with", "opentelemetry-instrumentation-starlette",
		"--with", "opentelemetry-instrumentation-requests",
		"--with", "opentelemetry-instrumentation-httpx",
		"opentelemetry-instrument",
		"shiny", "run", "app.py", "--host", "127.0.0.1", "--port", "41000",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("buildCommand =\n  %q\nwant\n  %q", got, want)
	}
}

// Default off: byte-for-byte today's command, in both dependency modes.
func TestBuildCommand_NoInstrumentUnchanged(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "requirements.txt"), []byte("shiny\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	got := buildCommand(dir, 41000, 1, "127.0.0.1", false)
	want := []string{
		"uv", "run", "--no-project", "--with-requirements", "requirements.txt",
		"shiny", "run", "app.py", "--host", "127.0.0.1", "--port", "41000",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("buildCommand =\n  %q\nwant\n  %q", got, want)
	}
}

func TestResolveAutoInstrument_ManifestOverridesFleetDefault(t *testing.T) {
	cases := []struct {
		name     string
		fleet    bool
		manifest string // "" = no manifest file
		want     bool
	}{
		{"fleet on, no manifest", true, "", true},
		{"fleet off, no manifest", false, "", false},
		{"fleet on, app opts out", true, "[tracing]\nauto = false\n", false},
		{"fleet off, app opts in", false, "[tracing]\nauto = true\n", true},
		{"fleet on, manifest without [tracing]", true, "[app]\nreplicas = 1\n", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			if tc.manifest != "" {
				if err := os.WriteFile(filepath.Join(dir, ManifestFilename), []byte(tc.manifest), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			mgr := process.NewManager(t.TempDir(), process.NewNativeRuntime())
			mgr.SetAutoInstrumentAppsDefault(tc.fleet)
			got := resolveAutoInstrument(Params{Slug: "x", BundleDir: dir, Manager: mgr})
			if got != tc.want {
				t.Errorf("resolveAutoInstrument = %v, want %v", got, tc.want)
			}
		})
	}
}

// A manifest that fails to re-read at boot (validated at deploy time, but
// disk trouble happens) must fall back to the fleet default, never error.
func TestResolveAutoInstrument_UnreadableManifestFallsBackToFleet(t *testing.T) {
	dir := t.TempDir()
	// A directory named shinyhub.toml makes os.ReadFile fail with a
	// non-NotExist error.
	if err := os.Mkdir(filepath.Join(dir, ManifestFilename), 0o755); err != nil {
		t.Fatal(err)
	}
	mgr := process.NewManager(t.TempDir(), process.NewNativeRuntime())
	mgr.SetAutoInstrumentAppsDefault(true)
	if got := resolveAutoInstrument(Params{Slug: "x", BundleDir: dir, Manager: mgr}); !got {
		t.Error("unreadable manifest should fall back to fleet default true")
	}
}
