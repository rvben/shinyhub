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
	got := buildCommand(dir, 41000, 1, "127.0.0.1", true, true)
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
	got := buildCommand(dir, 41000, 1, "127.0.0.1", true, true)
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
	got := buildCommand(dir, 41000, 1, "127.0.0.1", false, true)
	want := []string{
		"uv", "run", "--no-project", "--with-requirements", "requirements.txt",
		"shiny", "run", "app.py", "--host", "127.0.0.1", "--port", "41000",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("buildCommand =\n  %q\nwant\n  %q", got, want)
	}
}

// synthProject writes a pyproject.toml plus the synthesized marker (the state
// EnsureProject leaves) alongside a requirements.txt in dir.
func synthProject(t *testing.T, dir string) {
	t.Helper()
	for name, body := range map[string]string{
		"requirements.txt":               "shiny\n",
		"pyproject.toml":                 "[project]\nname='shinyhub-app'\n",
		process.SynthesizedProjectMarker: "1\n",
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// A synthesized project launches in project mode where this host prepared the
// deps (the .venv is present), not via --with-requirements.
func TestBuildCommand_SynthesizedProjectRunsInProjectModeOnHost(t *testing.T) {
	dir := t.TempDir()
	synthProject(t, dir)
	got := buildCommand(dir, 41000, 1, "127.0.0.1", false, true)
	want := []string{
		"uv", "run",
		"shiny", "run", "app.py", "--host", "127.0.0.1", "--port", "41000",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("buildCommand =\n  %q\nwant\n  %q", got, want)
	}
}

// Off-host (container/worker), a synthesized project's .venv is not present, so
// it must fall back to requirements.txt even though a pyproject is in the dir.
func TestBuildCommand_SynthesizedProjectFallsBackOffHost(t *testing.T) {
	dir := t.TempDir()
	synthProject(t, dir)
	got := buildCommand(dir, 41000, 1, "127.0.0.1", false, false)
	want := []string{
		"uv", "run", "--no-project", "--with-requirements", "requirements.txt",
		"shiny", "run", "app.py", "--host", "127.0.0.1", "--port", "41000",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("buildCommand =\n  %q\nwant\n  %q", got, want)
	}
}

// An author-shipped pyproject (no synthesized marker) ships with the bundle, so
// it is project mode everywhere, including off-host.
func TestBuildCommand_AuthorProjectIsProjectModeOffHost(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "pyproject.toml"), []byte("[project]\nname='x'\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	got := buildCommand(dir, 41000, 1, "127.0.0.1", false, false)
	want := []string{
		"uv", "run",
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
			var m *Manifest
			if tc.manifest != "" {
				if err := os.WriteFile(filepath.Join(dir, ManifestFilename), []byte(tc.manifest), 0o644); err != nil {
					t.Fatal(err)
				}
				var err error
				m, err = LoadManifest(dir)
				if err != nil {
					t.Fatalf("LoadManifest: %v", err)
				}
			}
			mgr := process.NewManager(t.TempDir(), process.NewNativeRuntime())
			mgr.SetAutoInstrumentAppsDefault(tc.fleet)
			got := resolveAutoInstrument(Params{Slug: "x", BundleDir: dir, Manager: mgr}, m)
			if got != tc.want {
				t.Errorf("resolveAutoInstrument = %v, want %v", got, tc.want)
			}
		})
	}
}

// An unparseable manifest is now fatal at boot (via resolveBootParams). The
// old warn-and-fallback behavior was in resolveAutoInstrument; the new contract
// is that the caller (resolveBootParams) never reaches resolveAutoInstrument
// when the manifest cannot be parsed. This test pins the new contract: a
// nil manifest (absent file) still uses the fleet default, and the unreadable
// case is covered by TestResolveBootParams_UnparseableManifestIsFatal.
func TestResolveAutoInstrument_NilManifestUsesFleetDefault(t *testing.T) {
	mgr := process.NewManager(t.TempDir(), process.NewNativeRuntime())
	mgr.SetAutoInstrumentAppsDefault(true)
	if got := resolveAutoInstrument(Params{Slug: "x", Manager: mgr}, nil); !got {
		t.Error("nil manifest should use fleet default true")
	}
}
