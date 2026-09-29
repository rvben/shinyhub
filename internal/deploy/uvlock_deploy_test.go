package deploy

import (
	"context"
	"fmt"
	"reflect"
	"testing"

	"github.com/rvben/shinyhub/internal/deployevent"
	"github.com/rvben/shinyhub/internal/process"
)

const lockedPyproject = "[project]\nname = \"app\"\nversion = \"0.1.0\"\ndependencies = [\"six\"]\n"

const lockedLock = `version = 1
revision = 1

[[package]]
name = "app"
version = "0.1.0"
source = { virtual = "." }
dependencies = [
    { name = "six" },
]

[package.metadata]
requires-dist = [{ name = "six" }]

[[package]]
name = "six"
version = "1.17.0"
source = { registry = "https://mirror.example/simple" }
`

// stalePyproject declares a requirement lockedLock does not include.
const stalePyproject = "[project]\nname = \"app\"\nversion = \"0.1.0\"\ndependencies = [\"six\", \"idna\"]\n"

func captureBuildStep(t *testing.T) *[]string {
	t.Helper()
	var argv []string
	prev := buildStepRunner
	buildStepRunner = func(_ context.Context, _ string, a []string, _ []string) ([]byte, error) {
		argv = a
		return nil, nil
	}
	t.Cleanup(func() { buildStepRunner = prev })
	return &argv
}

// A shipped uv.lock is installed as-is, on the exact production build path.
func TestSandboxedPythonSync_InstallsShippedLockFrozen(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "pyproject.toml", lockedPyproject)
	writeFile(t, dir, "uv.lock", lockedLock)
	argv := captureBuildStep(t)

	if err := sandboxedPythonSync(context.Background(), dir, nil); err != nil {
		t.Fatalf("sandboxedPythonSync: %v", err)
	}
	if want := []string{"uv", "sync", "--frozen"}; !reflect.DeepEqual(*argv, want) {
		t.Errorf("argv = %q, want %q", *argv, want)
	}
}

// A synthesized project's lock comes from the server's own `uv add`, and the
// requirements' --no-index still applies alongside --frozen.
func TestSandboxedPythonSync_SynthesizedLockKeepsRequirementsFlags(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "requirements.txt", "--no-index\nsix\n")
	writeFile(t, dir, "pyproject.toml", lockedPyproject)
	writeFile(t, dir, "uv.lock", lockedLock)
	writeFile(t, dir, process.SynthesizedProjectMarker, "1\n")
	argv := captureBuildStep(t)

	if err := sandboxedPythonSync(context.Background(), dir, nil); err != nil {
		t.Fatalf("sandboxedPythonSync: %v", err)
	}
	if want := []string{"uv", "sync", "--frozen", "--no-index"}; !reflect.DeepEqual(*argv, want) {
		t.Errorf("argv = %q, want %q", *argv, want)
	}
}

// A stale lock reaches a build only on an activation or a local run (a
// promotion refuses it first), and gets a plain `uv sync` that installs what
// pyproject.toml declares, rather than a frozen install without it.
func TestSandboxedPythonSync_StaleLockResolvesAgain(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "pyproject.toml", stalePyproject)
	writeFile(t, dir, "uv.lock", lockedLock)
	argv := captureBuildStep(t)

	if err := sandboxedPythonSync(context.Background(), dir, nil); err != nil {
		t.Fatalf("sandboxedPythonSync: %v", err)
	}
	if want := []string{"uv", "sync"}; !reflect.DeepEqual(*argv, want) {
		t.Errorf("argv = %q, want %q", *argv, want)
	}
}

// A stale lock is refused only where a bundle is first uploaded (the deploy
// handler). Every path that boots a bundle, including replica recovery and
// warm expansion (which run as PrepareRequired) and restarts, rollbacks and
// restores, brings back one that was already accepted, possibly before that
// check existed, so none of them refuses it: a host build that finds the
// environment missing re-resolves with a plain `uv sync`, as it did when the
// bundle was accepted, instead of failing.
func TestResolveBundleCommand_NeverRefusesStaleLock(t *testing.T) {
	prevEnsure := ensureProjectFn
	ensureProjectFn = func(context.Context, string, []string) error { return nil }
	t.Cleanup(func() { ensureProjectFn = prevEnsure })

	for _, mode := range []PreparationMode{PrepareRequired, PrepareSkip, PrepareBestEffort} {
		for _, hostDeps := range []bool{false, true} {
			t.Run(fmt.Sprintf("mode=%d/hostDeps=%v", mode, hostDeps), func(t *testing.T) {
				dir := t.TempDir()
				writeFile(t, dir, "app.py", "from shiny import App\n")
				writeFile(t, dir, "pyproject.toml", stalePyproject)
				writeFile(t, dir, "uv.lock", lockedLock)
				argv := captureBuildStep(t)

				var events []deployevent.Event
				p := Params{Slug: "demo", BundleDir: dir, Preparation: mode,
					Progress: func(e deployevent.Event) { events = append(events, e) }}
				if _, _, err := resolveBundleCommand(p, nil, hostDeps); err != nil {
					t.Fatalf("resolveBundleCommand: %v", err)
				}
				var want []string
				if hostDeps {
					want = []string{"uv", "sync"}
				}
				if !reflect.DeepEqual(*argv, want) {
					t.Errorf("build argv = %q, want %q", *argv, want)
				}
				for _, e := range events {
					if e.Phase == "dependencies" && e.Status == deployevent.StatusFailed {
						t.Errorf("dependency build reported failure: %+v", e)
					}
				}
			})
		}
	}
}
