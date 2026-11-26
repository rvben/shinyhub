package process

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"
)

func writeProbeFile(t *testing.T, dir, rel, content string) {
	t.Helper()
	path := filepath.Join(dir, rel)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// The probe keeps every uv option, in order, and replaces only the command,
// so uv builds exactly the environment the command would run in.
func TestUVRunEnvironmentProbe_KeepsOptionsAndReplacesCommand(t *testing.T) {
	noop := trustedNoop(t)
	dir := t.TempDir()
	writeProbeFile(t, dir, "fetch.py", "print('hi')\n")
	writeProbeFile(t, dir, "pkg/fetch.py", "print('hi')\n")
	cases := []struct {
		name string
		argv []string
		want string
	}{
		{"documented producer", []string{"uv", "run", "--with-requirements", "requirements.txt", "python", "fetch.py"},
			"uv run --with-requirements requirements.txt -- true"},
		{"python file target", []string{"uv", "run", "--no-project", "--with-requirements", "requirements.txt", "fetch.py", "--out", "x"},
			"uv run --no-project --with-requirements requirements.txt -- true"},
		{"nested file without its own project", []string{"uv", "run", "pkg/fetch.py"}, "uv run -- true"},
		{"module", []string{"uv", "run", "-m", "fetch"}, "uv run -- true"},
		{"options after the module flag", []string{"uv", "run", "--module", "--with-requirements", "requirements.txt", "-q", "fetch", "--out", "x"},
			"uv run --with-requirements requirements.txt -q -- true"},
		{"separator", []string{"/usr/local/bin/uv", "run", "--frozen", "--", "python", "fetch.py"},
			"/usr/local/bin/uv run --frozen -- true"},
		{"global options before run", []string{"uv", "--offline", "--color", "never", "--cache-dir=/c", "run", "-qq", "python", "fetch.py"},
			"uv --offline --color never --cache-dir=/c run -qq -- true"},
		{"attached and separate values", []string{"uv", "run", "--python=3.12", "-p", "3.12", "-w", "six", "--with", "attrs", "--extra", "x", "python", "fetch.py"},
			"uv run --python=3.12 -p 3.12 -w six --with attrs --extra x -- true"},
		{"value that looks like a command", []string{"uv", "run", "--package", "python", "fetch"},
			"uv run --package python -- true"},
		{"directory inside the bundle", []string{"uv", "run", "--directory", "pkg", "fetch.py"},
			"uv run --directory pkg -- true"},
		{"project inside the bundle", []string{"uv", "run", "--project=pkg", "python", "fetch.py"},
			"uv run --project=pkg -- true"},
		{"project relative to the directory", []string{"uv", "--directory", "pkg", "run", "--project", "..", "fetch.py"},
			"uv --directory pkg run --project .. -- true"},
	}
	for _, c := range cases {
		probe, ok := UVRunEnvironmentProbe(c.argv, nil, dir)
		if !ok || probe.NotChecked != "" {
			t.Errorf("%s: ok=%v notChecked=%q, want a probe", c.name, ok, probe.NotChecked)
			continue
		}
		want := strings.ReplaceAll(c.want, "-- true", "-- "+noop)
		if got := strings.Join(probe.Argv, " "); got != want {
			t.Errorf("%s: probe = %q, want %q", c.name, got, want)
		}
	}
}

// A command that does not launch uv has no uv environment to reproduce.
func TestUVRunEnvironmentProbe_NotUV(t *testing.T) {
	for _, argv := range [][]string{nil, {}, {"python", "fetch.py"}, {"Rscript", "fetch.R"}, {"/opt/uv-tools/run.sh"}} {
		if probe, ok := UVRunEnvironmentProbe(argv, nil, t.TempDir()); ok || probe.Argv != nil || probe.NotChecked != "" {
			t.Errorf("%q: ok=%v probe=%+v, want not applicable", argv, ok, probe)
		}
	}
}

// A uv launch whose environment the probe cannot reproduce exactly is
// reported as not checked, with the reason, and never probed.
func TestUVRunEnvironmentProbe_NotChecked(t *testing.T) {
	dir := t.TempDir()
	writeProbeFile(t, dir, "inline.py", "# /// script\n# dependencies = [\"six\"]\n# ///\nprint('hi')\n")
	writeProbeFile(t, dir, "tools/pyproject.toml", "[project]\nname = \"tools\"\nversion = \"0\"\n")
	writeProbeFile(t, dir, "tools/fetch.py", "print('hi')\n")
	writeProbeFile(t, dir, "pinned/.python-version", "3.12\n")
	writeProbeFile(t, dir, "pinned/jobs/fetch.py", "print('hi')\n")
	writeProbeFile(t, dir, "env-include.txt", "six\n-r ${PINS_DIR}/pins.txt\n")
	writeProbeFile(t, dir, "meta/script.py", "# /// script\n# dependencies = [\"six\"]\n# ///\n")
	if err := os.Symlink(t.TempDir(), filepath.Join(dir, "escape")); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name   string
		argv   []string
		reason string
	}{
		{"uvx", []string{"uvx", "ruff"}, "uvx"},
		{"other subcommand", []string{"uv", "tool", "run", "ruff"}, "not uv run"},
		{"no subcommand", []string{"uv", "--offline"}, "no subcommand"},
		{"no command", []string{"uv", "run", "--frozen"}, "no command"},
		{"separator without command", []string{"uv", "run", "--"}, "no command"},
		{"module without name", []string{"uv", "run", "-m"}, "no module"},
		{"module flag before options without name", []string{"uv", "run", "-m", "-q"}, "no module"},
		{"script flag", []string{"uv", "run", "--script", "fetch.py"}, "--script"},
		{"short script flag", []string{"uv", "run", "-s", "fetch.py"}, "-s"},
		{"gui script", []string{"uv", "run", "--gui-script", "fetch.pyw"}, "--gui-script"},
		{"stdin", []string{"uv", "run", "-"}, "stdin"},
		{"url", []string{"uv", "run", "https://example.com/fetch.py"}, "fetches"},
		{"inline metadata", []string{"uv", "run", "inline.py"}, "inline script dependencies"},
		{"nested project", []string{"uv", "run", "tools/fetch.py"}, "project in tools"},
		{"nested Python version pin", []string{"uv", "run", "pinned/jobs/fetch.py"}, "Python version pin in pinned"},
		{"include through an env var", []string{"uv", "run", "--with-requirements", "env-include.txt", "python"}, "env var reference"},
		{"outside the bundle", []string{"uv", "run", "../fetch.py"}, "outside"},
		{"absolute path", []string{"uv", "run", "/srv/fetch.py"}, "outside"},
		{"directory outside the bundle", []string{"uv", "run", "--directory", "..", "python", "fetch.py"}, "--directory .. is outside the bundle"},
		{"absolute directory", []string{"uv", "--directory=/srv", "run", "python", "fetch.py"}, "--directory /srv is outside the bundle"},
		{"project outside the bundle", []string{"uv", "run", "--project", "/srv/app", "python", "fetch.py"}, "--project /srv/app is outside the bundle"},
		{"project escaping through the directory", []string{"uv", "run", "--directory", "tools", "--project", "../..", "python"}, "--project ../.. is outside the bundle"},
		{"symlinked directory", []string{"uv", "run", "--directory", "escape", "python", "fetch.py"}, "--directory escape is outside the bundle"},
		{"file beside the directory", []string{"uv", "run", "--directory", "tools", "../inline.py"}, "outside"},
		{"inline metadata under the directory", []string{"uv", "run", "--directory", "meta", "script.py"}, "inline script dependencies"},
		{"unknown option", []string{"uv", "run", "--frobnicate", "python", "fetch.py"}, "--frobnicate"},
		{"unknown attached option", []string{"uv", "run", "--frobnicate=1", "python", "fetch.py"}, "--frobnicate"},
		{"run option before run", []string{"uv", "--with", "six", "run", "python"}, "--with"},
		{"missing value", []string{"uv", "run", "--with"}, "has no value"},
		{"help", []string{"uv", "run", "--help"}, "no environment"},
		{"launcher in the bundle", []string{"./uv", "run", "python"}, "./uv is the app's own launcher"},
		{"relative launcher", []string{"bin/uv", "run", "python"}, "bin/uv is the app's own launcher"},
		{"launcher beside the bundle", []string{"../uv", "run", "python"}, "../uv is the app's own launcher"},
		{"absolute launcher in the bundle", []string{filepath.Join(dir, "tools", "uv"), "run", "python"}, "the app's own launcher"},
	}
	for _, c := range cases {
		probe, ok := UVRunEnvironmentProbe(c.argv, nil, dir)
		if !ok || probe.Argv != nil {
			t.Errorf("%s: ok=%v argv=%q, want not checked", c.name, ok, probe.Argv)
			continue
		}
		if !strings.Contains(probe.NotChecked, c.reason) {
			t.Errorf("%s: reason %q lacks %q", c.name, probe.NotChecked, c.reason)
		}
	}
}

// The `uv run` options of the parser are the ones uv itself documents, so an
// option uv takes with a value is never mistaken for a flag whose value then
// reads as the command.
func TestUVRunOptionTablesAreDisjoint(t *testing.T) {
	for name := range uvRunFlagOptions {
		if uvRunValueOptions[name] || uvGlobalValueOptions[name] {
			t.Errorf("%s is both a flag and a value option", name)
		}
	}
}

func trustedNoop(t *testing.T) string {
	t.Helper()
	noop, err := uvRunNoop()
	if err != nil {
		t.Skipf("no true executable: %v", err)
	}
	return noop
}

// The probe runs the server's own `true` by absolute path, never a bare name
// uv would look up in the environment it builds, where the app's
// dependencies can install an executable of that name first on PATH.
func TestUVRunEnvironmentProbe_RunsTheServersOwnTrue(t *testing.T) {
	probe, ok := UVRunEnvironmentProbe([]string{"uv", "run", "python", "fetch.py"}, nil, t.TempDir())
	if !ok || probe.NotChecked != "" {
		t.Fatalf("ok=%v notChecked=%q, want a probe", ok, probe.NotChecked)
	}
	noop := probe.Argv[len(probe.Argv)-1]
	if !filepath.IsAbs(noop) {
		t.Fatalf("probe runs %q, want an absolute path", noop)
	}
	if info, err := os.Stat(noop); err != nil || info.IsDir() {
		t.Fatalf("probe runs %q: %v", noop, err)
	}
}

// The offline forms put --offline right after `run`, so uv takes the
// environment from its cache alone, and change nothing else: the command and
// its arguments are untouched, and a command already offline stays as it is.
func TestUVRunEnvironmentProbe_OfflineForms(t *testing.T) {
	noop := trustedNoop(t)
	cases := []struct {
		name        string
		argv        []string
		wantProbe   string
		wantCommand string
	}{
		{"plain", []string{"uv", "run", "--with-requirements", "r.txt", "python", "fetch.py", "--offline"},
			"uv run --offline --with-requirements r.txt -- true",
			"uv run --offline --with-requirements r.txt python fetch.py --offline"},
		{"global options", []string{"uv", "--directory", "sub", "run", "-m", "fetch"},
			"uv --directory sub run --offline -- true",
			"uv --directory sub run --offline -m fetch"},
		{"already offline before run", []string{"uv", "--offline", "run", "python"},
			"uv --offline run -- true",
			"uv --offline run python"},
		{"already offline after run", []string{"uv", "run", "--offline", "--", "python"},
			"uv run --offline -- true",
			"uv run --offline -- python"},
	}
	dir := t.TempDir()
	writeProbeFile(t, dir, "sub/x", "")
	for _, c := range cases {
		probe, ok := UVRunEnvironmentProbe(c.argv, nil, dir)
		if !ok || probe.NotChecked != "" {
			t.Errorf("%s: ok=%v notChecked=%q, want a probe", c.name, ok, probe.NotChecked)
			continue
		}
		if got, want := strings.Join(probe.OfflineArgv, " "), strings.ReplaceAll(c.wantProbe, "-- true", "-- "+noop); got != want {
			t.Errorf("%s: offline probe = %q, want %q", c.name, got, want)
		}
		if got := strings.Join(probe.OfflineCommand, " "); got != c.wantCommand {
			t.Errorf("%s: offline command = %q, want %q", c.name, got, c.wantCommand)
		}
	}
}

// uv takes its working directory and project from UV_WORKING_DIR and
// UV_PROJECT when the options are absent, so the probe reads the target and
// the containment from the same place uv does; an option still wins.
func TestUVRunEnvironmentProbe_ReadsUVsDirectoryFromTheEnvironment(t *testing.T) {
	dir := t.TempDir()
	writeProbeFile(t, dir, "script.py", "print('hi')\n")
	writeProbeFile(t, dir, "meta/script.py", "# /// script\n# dependencies = [\"six\"]\n# ///\n")
	cases := []struct {
		name   string
		argv   []string
		env    []string
		reason string // "" when the probe stands in for the command
	}{
		{"working dir", []string{"uv", "run", "script.py"}, []string{"UV_WORKING_DIR=meta"}, "inline script dependencies"},
		{"last entry wins", []string{"uv", "run", "script.py"}, []string{"UV_WORKING_DIR=.", "UV_WORKING_DIR=meta"}, "inline script dependencies"},
		{"option wins", []string{"uv", "run", "--directory", ".", "script.py"}, []string{"UV_WORKING_DIR=meta"}, ""},
		{"working dir outside", []string{"uv", "run", "python"}, []string{"UV_WORKING_DIR=/srv"}, "UV_WORKING_DIR is outside the bundle"},
		{"project outside", []string{"uv", "run", "python"}, []string{"UV_PROJECT=../other"}, "UV_PROJECT is outside the bundle"},
		{"option outside", []string{"uv", "run", "--project", "../other", "python"}, nil, "uv --project ../other is outside the bundle"},
		{"no environment", []string{"uv", "run", "script.py"}, nil, ""},
	}
	for _, c := range cases {
		probe, ok := UVRunEnvironmentProbe(c.argv, c.env, dir)
		if !ok {
			t.Errorf("%s: not a uv launch", c.name)
			continue
		}
		if c.reason == "" && (probe.NotChecked != "" || probe.Argv == nil) {
			t.Errorf("%s: notChecked = %q, want a probe", c.name, probe.NotChecked)
		}
		if c.reason != "" && (probe.Argv != nil || !strings.Contains(probe.NotChecked, c.reason)) {
			t.Errorf("%s: argv = %q notChecked = %q, want not checked for %q", c.name, probe.Argv, probe.NotChecked, c.reason)
		}
		// An env var can be a secret, so its value stays out of the reason.
		for _, kv := range c.env {
			if _, value, _ := strings.Cut(kv, "="); len(value) > 1 && strings.Contains(probe.NotChecked, value) {
				t.Errorf("%s: reason %q repeats the value of %s", c.name, probe.NotChecked, kv)
			}
		}
	}
}

// A command that needs the network at every launch has no offline forms: with
// uv's cache disabled nothing is kept, a refresh, upgrade or reinstall would
// be silently undone (or refused) offline, and a remote requirements file is
// fetched each time. The probe still checks the environment.
func TestUVRunEnvironmentProbe_NoOfflineFormsWithoutACache(t *testing.T) {
	cases := []struct {
		name    string
		argv    []string
		env     []string
		offline bool
	}{
		{"flag", []string{"uv", "--no-cache", "run", "python"}, nil, false},
		{"short flag", []string{"uv", "run", "-n", "python"}, nil, false},
		{"environment", []string{"uv", "run", "python"}, []string{"UV_NO_CACHE=1"}, false},
		{"environment spelled true", []string{"uv", "run", "python"}, []string{"UV_NO_CACHE=True"}, false},
		{"environment false", []string{"uv", "run", "python"}, []string{"UV_NO_CACHE=false"}, true},
		{"environment empty", []string{"uv", "run", "python"}, []string{"UV_NO_CACHE="}, true},
		{"refresh", []string{"uv", "run", "--refresh", "python"}, nil, false},
		{"refresh package", []string{"uv", "run", "--refresh-package=six", "python"}, nil, false},
		{"upgrade", []string{"uv", "run", "-U", "python"}, nil, false},
		{"upgrade package", []string{"uv", "run", "-P", "six", "python"}, nil, false},
		{"reinstall", []string{"uv", "run", "--reinstall", "python"}, nil, false},
		{"remote requirements", []string{"uv", "run", "--with-requirements", "https://example.com/req.txt", "python"}, nil, false},
		{"local requirements", []string{"uv", "run", "--with-requirements", "requirements.txt", "python"}, nil, true},
		{"cache kept", []string{"uv", "run", "python"}, nil, true},
	}
	for _, c := range cases {
		probe, ok := UVRunEnvironmentProbe(c.argv, c.env, t.TempDir())
		if !ok || probe.NotChecked != "" || probe.Argv == nil {
			t.Errorf("%s: ok=%v notChecked=%q, want a probe", c.name, ok, probe.NotChecked)
			continue
		}
		if got := probe.OfflineArgv != nil && probe.OfflineCommand != nil; got != c.offline {
			t.Errorf("%s: offline forms = %v (%q, %q), want %v", c.name, got, probe.OfflineArgv, probe.OfflineCommand, c.offline)
		}
	}
}

// A script URL can carry an access token, and the reason it was not checked
// reaches logs and deploy warnings, so it never repeats the URL.
func TestUVRunEnvironmentProbe_LeavesTheScriptURLOutOfTheReason(t *testing.T) {
	probe, ok := UVRunEnvironmentProbe([]string{"uv", "run", "https://example.com/fetch.py?token=secret-1234"}, nil, t.TempDir())
	if !ok || probe.Argv != nil || probe.NotChecked == "" {
		t.Fatalf("ok=%v argv=%q notChecked=%q, want not checked", ok, probe.Argv, probe.NotChecked)
	}
	if strings.Contains(probe.NotChecked, "secret-1234") || strings.Contains(probe.NotChecked, "example.com") {
		t.Errorf("reason %q repeats the URL", probe.NotChecked)
	}
}

// The check gets none of the app's data, which a launch links into the bundle
// as data/: a path there is not checked, whether the link is still absent (a
// fresh bundle) or already present (a rollback target, where building through
// it would write to live data).
// An installed uv given by absolute path is the server's, and is probed.
func TestUVRunEnvironmentProbe_ProbesAnInstalledUV(t *testing.T) {
	probe, ok := UVRunEnvironmentProbe([]string{"/opt/uv/bin/uv", "run", "python"}, nil, t.TempDir())
	if !ok || probe.Argv == nil || probe.Argv[0] != "/opt/uv/bin/uv" {
		t.Fatalf("ok=%v probe=%+v, want a probe through /opt/uv/bin/uv", ok, probe)
	}
}

// Planning the check reads requirements files and the Python target before
// any timeout applies, so it reads only bounded regular files: a FIFO would
// block it and a device or huge file would never end. Such an input is left
// unchecked, not read.
func TestUVRunEnvironmentProbe_ReadsOnlyBoundedRegularFiles(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"pipe.txt", "fetch.py"} {
		if err := syscall.Mkfifo(filepath.Join(dir, name), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	writeProbeFile(t, dir, "large.txt", "six\n")
	if err := os.Truncate(filepath.Join(dir, "large.txt"), maxProbeInputBytes+1); err != nil {
		t.Fatal(err)
	}
	writeProbeFile(t, dir, "nested.txt", "six\n-r pipe.txt\n")
	cases := []struct {
		name   string
		argv   []string
		reason string
	}{
		{"fifo requirements", []string{"uv", "run", "--with-requirements", "pipe.txt", "python"}, "pipe.txt cannot be inspected"},
		{"fifo include", []string{"uv", "run", "--with-requirements", "nested.txt", "python"}, "nested.txt cannot be inspected"},
		{"oversized requirements", []string{"uv", "run", "--with-requirements", "large.txt", "python"}, "large.txt cannot be inspected"},
		{"fifo target", []string{"uv", "run", "fetch.py"}, "fetch.py"},
	}
	for _, c := range cases {
		done := make(chan UVRunProbe, 1)
		go func() {
			probe, _ := UVRunEnvironmentProbe(c.argv, nil, dir)
			done <- probe
		}()
		select {
		case probe := <-done:
			if probe.Argv != nil || !strings.Contains(probe.NotChecked, c.reason) {
				t.Errorf("%s: argv = %q notChecked = %q, want not checked for %q", c.name, probe.Argv, probe.NotChecked, c.reason)
			}
		case <-time.After(5 * time.Second):
			// Unblock the reader so the test binary can exit.
			if f, err := os.OpenFile(filepath.Join(dir, "pipe.txt"), os.O_WRONLY|syscall.O_NONBLOCK, 0); err == nil {
				f.Close()
			}
			if f, err := os.OpenFile(filepath.Join(dir, "fetch.py"), os.O_WRONLY|syscall.O_NONBLOCK, 0); err == nil {
				f.Close()
			}
			t.Fatalf("%s: planning the check blocked on its input", c.name)
		}
	}
}

func TestUVRunEnvironmentProbe_LeavesTheDataDirUnchecked(t *testing.T) {
	fresh, linked, live := t.TempDir(), t.TempDir(), t.TempDir()
	if err := os.Symlink(live, filepath.Join(linked, "data")); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name   string
		argv   []string
		env    []string
		reason string // "" when the probe stands in for the command
	}{
		{"requirements", []string{"uv", "run", "--with-requirements", "data/requirements.txt", "python"}, nil, "data dir"},
		{"requirements joined", []string{"uv", "run", "--with-requirements=./data/req.txt", "python"}, nil, "data dir"},
		{"from a directory", []string{"uv", "run", "--directory", "src", "--with-requirements", "../data/req.txt", "python"}, nil, "data dir"},
		{"editable", []string{"uv", "run", "--with-editable", "data/pkg", "python"}, nil, "data dir"},
		{"working dir", []string{"uv", "run", "--directory", "data/app", "python"}, nil, "data dir"},
		{"project", []string{"uv", "run", "--project", "./data", "python"}, nil, "data dir"},
		{"cache dir", []string{"uv", "--cache-dir", "data/cache", "run", "python"}, nil, "data dir"},
		{"project environment", []string{"uv", "run", "python"}, []string{"UV_PROJECT_ENVIRONMENT=data/venv"}, "UV_PROJECT_ENVIRONMENT"},
		{"project environment outside", []string{"uv", "run", "python"}, []string{"UV_PROJECT_ENVIRONMENT=/opt/venv"}, "UV_PROJECT_ENVIRONMENT"},
		{"project environment inside", []string{"uv", "run", "python"}, []string{"UV_PROJECT_ENVIRONMENT=.venv-jobs"}, ""},
		{"project environment absolute inside", []string{"uv", "run", "--directory", "src", "python"}, []string{"UV_PROJECT_ENVIRONMENT=" + "{bundle}/.venv-jobs"}, ""},
		{"project environment from a project", []string{"uv", "run", "--directory", "src", "--project", "..", "python"}, []string{"UV_PROJECT_ENVIRONMENT=data/venv"}, "UV_PROJECT_ENVIRONMENT"},
		{"project environment in a subproject", []string{"uv", "run", "--project", "src", "python"}, []string{"UV_PROJECT_ENVIRONMENT=../data/venv"}, "UV_PROJECT_ENVIRONMENT"},
		{"requirements include", []string{"uv", "run", "--with-requirements", "includes.txt", "python"}, nil, "data dir"},
		{"requirements nested include", []string{"uv", "run", "--with-requirements", "nested.txt", "python"}, nil, "data dir"},
		{"requirements find links", []string{"uv", "run", "--with-requirements", "links.txt", "python"}, nil, "data dir"},
		{"requirements local include", []string{"uv", "run", "--with-requirements", "local.txt", "python"}, nil, ""},
		{"requirements file URL include", []string{"uv", "run", "--with-requirements", "url-include.txt", "python"}, nil, "data dir"},
		{"requirements file URL", []string{"uv", "run", "--with-requirements", "file://{bundle}/data/req.txt", "python"}, nil, "data dir"},
		{"find links file URL", []string{"uv", "run", "--find-links", "file://{bundle}/data/wheels", "python"}, nil, "data dir"},
		{"find links", []string{"uv", "run", "--find-links", "data/wheels", "python"}, nil, "data dir"},
		{"find links short", []string{"uv", "run", "-f", "./data/wheels", "python"}, nil, "data dir"},
		{"find links in the bundle", []string{"uv", "run", "-f", "wheels", "python"}, nil, ""},
		{"find links from the environment", []string{"uv", "run", "python"}, []string{"UV_FIND_LINKS=https://example.com/wheels, data/wheels"}, "UV_FIND_LINKS"},
		{"env file from the environment", []string{"uv", "run", "python"}, []string{"UV_ENV_FILE=.env data/runtime.env"}, "UV_ENV_FILE"},
		{"env file from the environment in the bundle", []string{"uv", "run", "python"}, []string{"UV_ENV_FILE=.env"}, ""},
		{"cache dir from the environment", []string{"uv", "run", "python"}, []string{"UV_CACHE_DIR=data/cache"}, "UV_CACHE_DIR"},
		{"config file from the environment", []string{"uv", "run", "--directory", "src", "python"}, []string{"UV_CONFIG_FILE=../data/uv.toml"}, "UV_CONFIG_FILE"},
		{"env file option", []string{"uv", "run", "--env-file", "data/runtime.env", "python"}, nil, "data dir"},
		{"find links from the environment in the bundle", []string{"uv", "run", "python"}, []string{"UV_FIND_LINKS=https://example.com/wheels,wheels"}, ""},
		{"active environment outside", []string{"uv", "run", "--active", "python"}, []string{"VIRTUAL_ENV=/opt/shared-venv"}, "VIRTUAL_ENV"},
		{"active environment in data", []string{"uv", "run", "--active", "python"}, []string{"VIRTUAL_ENV={bundle}/data/venv"}, "VIRTUAL_ENV"},
		{"active environment inside", []string{"uv", "run", "--active", "python"}, []string{"VIRTUAL_ENV={bundle}/.venv"}, ""},
		{"inactive environment", []string{"uv", "run", "python"}, []string{"VIRTUAL_ENV=/opt/shared-venv"}, ""},
		{"a name like data", []string{"uv", "run", "--with-requirements", "data-requirements.txt", "python"}, nil, ""},
		{"a package named data", []string{"uv", "run", "--with", "data", "python"}, nil, ""},
		{"with a local wheel", []string{"uv", "run", "--with", "six,./data/wheels/pkg.whl", "python"}, nil, "data dir"},
		{"with a short local path", []string{"uv", "run", "-w", "data/pkg", "python"}, nil, "data dir"},
		{"with a named direct reference", []string{"uv", "run", "--with=pkg @ file://{bundle}/data/pkg", "python"}, nil, "data dir"},
		{"with a local path in the bundle", []string{"uv", "run", "--with", "./vendor/pkg", "python"}, nil, ""},
		{"with a remote reference", []string{"uv", "run", "--with", "pkg @ git+https://example.com/data/pkg", "python"}, nil, ""},
		{"script in data", []string{"uv", "run", "data/fetch.py"}, nil, "data dir"},
		{"script in data from a directory", []string{"uv", "run", "--directory", "src", "../data/fetch.py"}, nil, "data dir"},
		{"script in the bundle", []string{"uv", "run", "fetch.py"}, nil, ""},
	}
	for _, dir := range []string{fresh, linked} {
		writeProbeFile(t, dir, "includes.txt", "six\n-r data/extra.txt\n")
		writeProbeFile(t, dir, "nested.txt", "-c reqs/inner.txt\n")
		writeProbeFile(t, dir, "reqs/inner.txt", "--requirement=../data/extra.txt # pinned\n")
		writeProbeFile(t, dir, "links.txt", "--find-links ./data/wheels\nsix\n")
		writeProbeFile(t, dir, "local.txt", "-r reqs/base.txt\n--find-links https://example.com/wheels\n")
		writeProbeFile(t, dir, "reqs/base.txt", "six\n")
		writeProbeFile(t, dir, "url-include.txt", "six\n-r file://"+filepath.Join(dir, "data", "pins.txt")+"\n")
		for _, c := range cases {
			env, argv := slices.Clone(c.env), slices.Clone(c.argv)
			for i := range env {
				env[i] = strings.ReplaceAll(env[i], "{bundle}", dir)
			}
			for i := range argv {
				argv[i] = strings.ReplaceAll(argv[i], "{bundle}", dir)
			}
			probe, ok := UVRunEnvironmentProbe(argv, env, dir)
			if !ok {
				t.Errorf("%s: not a uv launch", c.name)
				continue
			}
			if c.reason == "" && (probe.NotChecked != "" || probe.Argv == nil) {
				t.Errorf("%s in %s: notChecked = %q, want a probe", c.name, dir, probe.NotChecked)
			}
			// Through an existing link a path also reads as outside the bundle.
			if c.reason != "" && (probe.Argv != nil || !strings.Contains(probe.NotChecked, c.reason) && !strings.Contains(probe.NotChecked, "outside the bundle")) {
				t.Errorf("%s in %s: argv = %q notChecked = %q, want not checked for %q", c.name, dir, probe.Argv, probe.NotChecked, c.reason)
			}
			for _, kv := range env {
				if _, value, _ := strings.Cut(kv, "="); strings.Contains(probe.NotChecked, value) {
					t.Errorf("%s: reason %q repeats the value of %s", c.name, probe.NotChecked, kv)
				}
			}
		}
	}
}
