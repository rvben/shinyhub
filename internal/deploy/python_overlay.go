package deploy

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/rvben/shinyhub/internal/fsx"
	"github.com/rvben/shinyhub/internal/process"
	"github.com/rvben/shinyhub/internal/pythontrace"
	"github.com/rvben/shinyhub/internal/sandbox"
)

// The overlay lives alongside the app's interpreter, so it is retained with
// the deployment and excluded from bundles with the rest of the venv.
func pythonOverlayDir(bundleDir string, packages []string) (string, error) {
	lock, err := os.ReadFile(filepath.Join(bundleDir, "uv.lock"))
	if err != nil {
		return "", err
	}
	key := sha256.Sum256(append([]byte(strings.Join(packages, "\x00")+"\x00"), lock...))
	return filepath.Abs(filepath.Join(bundleDir, ".venv", fmt.Sprintf(".shinyhub-otel-%x", key)))
}

func appPython(bundleDir string) string {
	return filepath.Join(bundleDir, ".venv", "bin", "python")
}

func hasPreparedPython(bundleDir string) bool {
	return useProjectMode(bundleDir, true) && executable(appPython(bundleDir))
}

func executable(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.Mode().IsRegular() && info.Mode().Perm()&0o111 != 0
}

func preparedInstrumenter(bundleDir string, packages []string) []string {
	dir, err := pythonOverlayDir(bundleDir, packages)
	if err != nil {
		return nil
	}
	python := filepath.Join(dir, "bin", "python")
	if _, err := os.Stat(filepath.Join(dir, "ready")); err != nil || !executable(python) {
		return nil
	}
	// uv sets PATH for the base project. Subprocesses of the instrumented
	// workload should see the overlay's Python and tools first.
	return []string{"/bin/sh", "-c", `PATH="${1%/*}:$PATH" VIRTUAL_ENV="${1%/bin/*}" exec "$@"`, "sh", python, "-c", overlayEntrypoint, python}
}

type pythonInstrumentation struct {
	packages     []string
	prepared     bool
	instrumenter []string
}

func resolvePythonInstrumentation(bundleDir string, packages []string, hostDeps bool) pythonInstrumentation {
	if len(packages) > 0 && hostDeps && hasPreparedPython(bundleDir) {
		return pythonInstrumentation{prepared: true, instrumenter: preparedInstrumenter(bundleDir, packages)}
	}
	return pythonInstrumentation{packages: packages}
}

func (p pythonInstrumentation) modulePrefix(bundleDir string, hostDeps bool) ([]string, bool) {
	prefix := pythonEnvironmentPrefix(bundleDir, p.packages, hostDeps)
	if p.prepared {
		if len(p.instrumenter) == 0 {
			slog.Warn("Python app instrumentation environment unavailable; running without auto-instrumentation", "bundle", bundleDir)
			return prefix, false
		}
		prefix = append(append(prefix, "--offline", "--"), p.instrumenter...)
		return append(prefix, "-c", pythontrace.Bootstrap, "app-module"), true
	}
	if len(p.packages) > 0 {
		return append(prefix, pythontrace.ModulePrefix()...), true
	}
	return prefix, false
}

// uv resolves against distributions installed in its target environment.
// Adding the app's site-packages to Python's sys.path only affects imports.
// Seed metadata so uv can reuse the app's existing versions, including local
// and editable sources, without fetching copies or changing the app's venv.
const seedOverlayMetadata = `
import importlib.metadata
from pathlib import Path
import shutil
import site
import sys

prefix = Path(sys.prefix)
app_prefix = sys.argv[1]
overlay_site = Path(site.getsitepackages()[0])
seeded = set()
for app_site in site.getsitepackages([app_prefix]):
    for metadata in Path(app_site).glob('*.dist-info/METADATA'):
        name = metadata.parent.name
        if name in seeded:
            continue
        target = overlay_site / name
        target.mkdir()
        shutil.copyfile(metadata, target / 'METADATA')
        seeded.add(name)
requirements = {d.metadata['Name'] + '==' + d.version for d in importlib.metadata.distributions(path=[str(overlay_site)])}
(prefix / 'constraints.txt').write_text('\n'.join(sorted(requirements)) + '\n', encoding='utf-8')
(prefix / 'seeded.txt').write_text('\n'.join(sorted(seeded)) + '\n', encoding='utf-8')
`

// Process the app's .pth files too so editable app dependencies and workspace
// packages remain importable. Both virtual environments use the app's Python.
const overlaySitePackages = `
from pathlib import Path
import site
import sys

prefix = Path(sys.prefix)
site_dir = Path(site.getsitepackages()[0])
seeded = prefix / 'seeded.txt'
for name in seeded.read_text(encoding='utf-8').splitlines():
    if not name:
        continue
    dist = site_dir / name
    if list(dist.iterdir()) != [dist / 'METADATA']:
        raise RuntimeError('overlay replaced an app distribution: ' + name)
    (dist / 'METADATA').unlink()
    dist.rmdir()
seeded.unlink()
(prefix / 'constraints.txt').unlink()
line = 'import site; list(map(site.addsitedir, site.getsitepackages([' + repr(sys.argv[1]) + '])))\n'
(site_dir / '_shinyhub_app.pth').write_text(line, encoding='utf-8')
`

// The OTel launcher may have been supplied by the app. Invoke its registered
// entry point with the overlay interpreter either way.
const loadOverlayEntrypoint = `
from importlib.metadata import distributions
import sys

sys.argv[0] = 'opentelemetry-instrument'
instrument = next(
    ep for dist in distributions() for ep in dist.entry_points
    if ep.group == 'console_scripts' and ep.name == sys.argv[0]
).load()
`

const overlayEntrypoint = loadOverlayEntrypoint + "instrument()\n"

func preparePythonOverlay(ctx context.Context, bundleDir string, packages, appEnv []string) error {
	return buildPythonOverlay(ctx, bundleDir, packages, appEnv, func(commit func() error) error { return commit() })
}

// PrepareExistingPythonInstrumentation uses a staging directory distinct from
// the live venv. The caller fences promotion against deployment changes without
// holding the app's lifecycle lock during package resolution.
func PrepareExistingPythonInstrumentation(ctx context.Context, p Params, publish func(func() error) error) error {
	manifest, err := LoadManifest(p.BundleDir)
	if err != nil {
		return err
	}
	packages := instrumentOverlay(resolveAutoInstrument(p, manifest), p.Manager.AutoInstrumentExtraPackages())
	release, err := activeBuildGate.Load().acquire(ctx, buildProgressInterval, nil)
	if err != nil {
		return err
	}
	defer release()
	env, err := p.Manager.ResolveBundleAppEnv(p.Slug, p.BundleDir)
	if err != nil {
		return err
	}
	return buildPythonOverlay(ctx, p.BundleDir, packages, env, publish)
}

// NativePythonInstrumentationState describes preparation for future launches.
// A ready overlay does not establish that an already-running process uses it.
func NativePythonInstrumentationState(bundleDir string, auto bool, extra []string) (string, error) {
	if !hasPreparedPython(bundleDir) {
		return "", nil
	}
	manifest, err := LoadManifest(bundleDir)
	if err != nil {
		return "", err
	}
	if manifest != nil && manifest.Tracing.Auto != nil {
		auto = *manifest.Tracing.Auto
	}
	packages := instrumentOverlay(auto, extra)
	if len(packages) == 0 {
		return "", nil
	}
	if preparedInstrumenter(bundleDir, packages) != nil {
		return "ready", nil
	}
	return "not_prepared", nil
}

func buildPythonOverlay(ctx context.Context, bundleDir string, packages, appEnv []string, publish func(func() error) error) error {
	if len(packages) == 0 || !hasPreparedPython(bundleDir) || preparedInstrumenter(bundleDir, packages) != nil {
		return nil
	}
	dir, err := pythonOverlayDir(bundleDir, packages)
	if err != nil {
		return err
	}
	build, err := process.ReadRequirementsBuild(bundleDir, appEnv)
	if err != nil {
		return err
	}
	appPrefix := filepath.Dir(dir)
	appInfo, err := os.Stat(appPrefix)
	if err != nil {
		return err
	}
	run := buildStepRunner
	isolated := process.HostUserIsolationEnabled()
	var buildDir string
	if isolated {
		// The broker runs as the app user and only writes to that app's
		// trees. Create the temporary directory under its identity with
		// private group access for the controller that publishes it.
		buildDir = filepath.Join(appPrefix, ".shinyhub-otel-build-"+rand.Text())
		if out, err := run(ctx, bundleDir, []string{"mkdir", "-m", "0770", buildDir}, build.Env); err != nil {
			return fmt.Errorf("create isolated instrumentation staging: %w\n%s", err, build.Output(out))
		}
	} else {
		buildDir, err = os.MkdirTemp(appPrefix, ".shinyhub-otel-build-")
		if err != nil {
			return err
		}
	}
	defer fsx.RemoveAll(buildDir)
	stage := buildDir
	if !isolated {
		// The overlay only needs to write to staging. Resolve uv configuration
		// from the original working directory with the base environment read-only.
		run = func(ctx context.Context, dir string, argv, env []string) ([]byte, error) {
			env = append(append([]string(nil), env...),
				"UV_CACHE_DIR="+filepath.Join(buildDir, "cache"),
				"XDG_CACHE_HOME="+filepath.Join(buildDir, "cache"),
				"CARGO_HOME="+filepath.Join(buildDir, "cargo"))
			return runSandboxedBuildStepWith(ctx, dir, argv, env, func(string) (sandbox.Spec, []string) {
				return sandbox.ComputeSpec(sandboxBuildLevel, buildDir), nil
			})
		}
		// Publish only the venv. Resolver caches belong to the staging tree.
		stage = filepath.Join(stage, "environment")
		if err := os.Mkdir(stage, 0o700); err != nil {
			return err
		}
	}
	base, err := filepath.Abs(appPython(bundleDir))
	if err != nil {
		return err
	}
	python := filepath.Join(stage, "bin", "python")
	constraints := filepath.Join(stage, "constraints.txt")
	install := []string{"uv", "pip", "install", "--no-python-downloads", "--python", python, "--link-mode", "copy", "--constraints", constraints}
	install = append(append(install, build.Flags...), packages...)
	for _, argv := range [][]string{
		{"uv", "venv", "--allow-existing", "--relocatable", "--no-python-downloads", "--python", base, stage},
		{python, "-c", seedOverlayMetadata, appPrefix},
		install,
		{python, "-c", overlaySitePackages, appPrefix},
		{python, "-c", loadOverlayEntrypoint},
	} {
		out, err := run(ctx, bundleDir, argv, build.Env)
		if err != nil {
			return fmt.Errorf("%w\n%s", err, build.Output(out))
		}
	}
	if err := os.WriteFile(filepath.Join(stage, "ready"), nil, 0o644); err != nil {
		return err
	}
	return publish(func() error {
		if err := ctx.Err(); err != nil {
			return err
		}
		current, err := os.Stat(appPrefix)
		if err != nil {
			return err
		}
		if !os.SameFile(appInfo, current) {
			return fmt.Errorf("app Python environment was replaced during instrumentation preparation")
		}
		if err := os.Rename(stage, dir); err != nil && preparedInstrumenter(bundleDir, packages) == nil {
			return err
		}
		return nil
	})
}

// InstrumentPythonCommand resolves schedule instrumentation before the job
// starts. Job exit codes belong to the job and never trigger a fallback.
func InstrumentPythonCommand(bundleDir string, argv []string, auto bool, extra []string, hostDeps bool) ([]string, bool, error) {
	manifest, err := LoadManifest(bundleDir)
	if err != nil {
		return nil, false, err
	}
	if manifest != nil && manifest.Tracing.Auto != nil {
		auto = *manifest.Tracing.Auto
	}
	packages := instrumentOverlay(auto, extra)
	if len(packages) == 0 {
		return argv, false, nil
	}
	plan := resolvePythonInstrumentation(bundleDir, packages, hostDeps)
	if !plan.prepared {
		command, instrumented := pythontrace.Wrap(argv, plan.packages, pythonEnvironmentPrefix(bundleDir, plan.packages, hostDeps))
		return command, instrumented, nil
	}
	projectPrefix := pythonEnvironmentPrefix(bundleDir, nil, hostDeps)
	if command, instrumented, eligible := pythontrace.WrapPrepared(argv, projectPrefix, plan.instrumenter); eligible {
		if !instrumented {
			slog.Warn("Python job instrumentation environment unavailable; running without auto-instrumentation", "bundle", bundleDir)
		}
		return command, instrumented, nil
	}
	// A command that selects its own uv environment keeps its exact argv.
	// Installing the overlay there would add a new dependency on the index.
	if _, instrumented := pythontrace.Wrap(argv, packages, projectPrefix); instrumented {
		slog.Warn("Python job has custom uv options; running without auto-instrumentation", "bundle", bundleDir)
	}
	return argv, false, nil
}
