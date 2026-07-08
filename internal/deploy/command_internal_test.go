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
		"uv", "run", "--frozen", "--no-sync",
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
		"uv", "run", "--frozen", "--no-sync",
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

// An author-shipped pyproject (no synthesized marker) is project mode off-host
// too, but no host prep ran there (HostPreparesDeps is false for container and
// worker tiers) and bundles never carry a .venv, so the launch itself must
// build the environment: with a shipped uv.lock it syncs from the lockfile
// (--frozen, no --no-sync).
func TestBuildCommand_AuthorProjectOffHostSyncsFromShippedLock(t *testing.T) {
	dir := t.TempDir()
	for name, body := range map[string]string{
		"pyproject.toml": "[project]\nname='x'\n",
		"uv.lock":        "version = 1\n",
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	got := buildCommand(dir, 41000, 1, "127.0.0.1", false, false)
	want := []string{
		"uv", "run", "--frozen",
		"shiny", "run", "app.py", "--host", "127.0.0.1", "--port", "41000",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("buildCommand =\n  %q\nwant\n  %q", got, want)
	}
}

// An author pyproject without a shipped uv.lock cannot use --frozen (uv errors
// "Unable to find lockfile"); off-host the launch resolves and syncs itself.
func TestBuildCommand_AuthorProjectOffHostWithoutLockResolvesAtLaunch(t *testing.T) {
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

// ON-HOST project-mode launches must do ZERO dependency work: the uv sync in
// resolveBootParams (untimed, before the health-check window) prepares the
// locked .venv, and the launch only execs against it. A plain `uv run` re-checks
// the lock and syncs on start; on a cold first boot that uncached resolve/build
// can stall past the health timeout and fail the boot. --frozen (no lock
// resolution) and --no-sync (no environment sync) pin the launch to pure exec.
// Off-host is the opposite contract (no prep ever ran, the launch must sync);
// the AuthorProjectOffHost tests above pin those shapes exactly.
func TestBuildCommand_ProjectModeLaunchDoesNoDependencyWork(t *testing.T) {
	mkAuthor := func(t *testing.T) string {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "pyproject.toml"), []byte("[project]\nname='x'\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		return dir
	}
	mkSynth := func(t *testing.T) string { dir := t.TempDir(); synthProject(t, dir); return dir }

	cases := []struct {
		name  string
		mkDir func(*testing.T) string
	}{
		{"author pyproject on host", mkAuthor},
		{"synthesized project on host", mkSynth},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := buildCommand(tc.mkDir(t), 41000, 1, "127.0.0.1", false, true)
			if len(got) < 4 || got[0] != "uv" || got[1] != "run" || got[2] != "--frozen" || got[3] != "--no-sync" {
				t.Fatalf("on-host project-mode launch must start with `uv run --frozen --no-sync`, got %q", got)
			}
			for _, a := range got {
				if a == "--with-requirements" {
					t.Errorf("project-mode launch must not install deps at start, got %q", got)
				}
			}
		})
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
