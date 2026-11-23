package jobs_test

import (
	"archive/zip"
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rvben/shinyhub/internal/db"
	"github.com/rvben/shinyhub/internal/deploy"
	"github.com/rvben/shinyhub/internal/jobs"
	"github.com/rvben/shinyhub/internal/process"
)

type scheduleProbeStore struct {
	*fakeStore
	finished chan struct{}
}

func (s scheduleProbeStore) CompleteScheduleRun(params db.CompleteScheduleRunParams) (*db.ScheduleRunCompletion, error) {
	completion, err := s.fakeStore.CompleteScheduleRun(params)
	if err == nil && completion.Transition != nil {
		close(s.finished)
	}
	return completion, err
}

func TestNativeScheduleUsesPreparedInstrumentationDuringIndexOutage(t *testing.T) {
	uv, err := exec.LookPath("uv")
	if err != nil {
		t.Skip("uv not installed")
	}
	for _, tc := range []scheduleOutageScenario{
		{name: "default"},
		{name: "project index", projectIndex: true},
		{name: "upgrade existing deployment", projectIndex: true, upgrade: true},
		{name: "upgrade through native build runner", projectIndex: true, upgrade: true, isolated: true},
		{name: "python3.9", pythonVersion: "3.9"},
	} {
		t.Run(tc.name, func(t *testing.T) { testNativeScheduleInstrumentationOutage(t, uv, tc) })
	}
}

type scheduleOutageScenario struct {
	name, pythonVersion             string
	projectIndex, upgrade, isolated bool
}

func testNativeScheduleInstrumentationOutage(t *testing.T, uv string, scenario scheduleOutageScenario) {
	t.Helper()
	find := []string{"python", "find", "--no-project", "--offline"}
	if scenario.pythonVersion != "" {
		find = append(find, scenario.pythonVersion)
	}
	python, err := exec.Command(uv, find...).Output()
	if err != nil {
		t.Skipf("no usable local Python: %v", err)
	}

	const appDependency, devDependency = "shinyhub-job-dependency", "shinyhub-job-dev-dependency"
	extras := []string{"opentelemetry-instrumentation-logging"}
	packages := []string{devDependency, "opentelemetry-distro", "opentelemetry-exporter-otlp",
		"opentelemetry-instrumentation-starlette", "opentelemetry-instrumentation-requests",
		"opentelemetry-instrumentation-httpx", extras[0], "probe-instrumentation-conflict"}
	index := make(map[string]string, len(packages))
	wheels := make(map[string][]byte, len(packages))
	for _, pkg := range packages {
		name, data := scheduleProbeWheel(t, pkg)
		index[pkg], wheels[name] = name, data
	}
	var requests atomic.Int64
	var outage atomic.Bool
	const mirrorUser, mirrorToken = "test-deploy", "test-mirror-token"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if outage.Load() {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		if user, token, ok := r.BasicAuth(); !ok || user != mirrorUser || token != mirrorToken {
			w.Header().Set("WWW-Authenticate", `Basic realm="test"`)
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Header().Set("Cache-Control", "max-age=0")
		if name, ok := index[strings.TrimPrefix(strings.TrimSuffix(r.URL.Path, "/"), "/simple/")]; ok {
			w.Header().Set("Content-Type", "text/html")
			fmt.Fprintf(w, `<a href="/wheels/%s">%s</a>`, name, name)
			return
		}
		if data, ok := wheels[strings.TrimPrefix(r.URL.Path, "/wheels/")]; ok {
			http.ServeContent(w, r, filepath.Base(r.URL.Path), time.Time{}, bytes.NewReader(data))
			return
		}
		http.NotFound(w, r)
	}))
	defer srv.Close()

	t.Setenv("SHINYHUB_APP_ENV_ALLOW", "UV_NO_CONFIG,UV_HTTP_RETRIES,UV_PYTHON_DOWNLOADS,XDG_CONFIG_HOME")
	t.Setenv("UV_NO_CONFIG", "1")
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("UV_HTTP_RETRIES", "0")
	t.Setenv("UV_PYTHON_DOWNLOADS", "never")
	t.Setenv("UV_PYTHON", strings.TrimSpace(string(python)))
	t.Setenv("UV_CACHE_DIR", t.TempDir())
	for _, name := range []string{"UV_INDEX", "UV_INDEX_URL", "UV_EXTRA_INDEX_URL", "UV_FIND_LINKS"} {
		t.Setenv(name, "")
	}
	var indexConfig string
	if scenario.projectIndex {
		t.Setenv("UV_NO_CONFIG", "0")
		t.Setenv("UV_DEFAULT_INDEX", "")
		t.Setenv("UV_INDEX_PROBE_USERNAME", mirrorUser)
		t.Setenv("UV_INDEX_PROBE_PASSWORD", mirrorToken)
		indexConfig = fmt.Sprintf("\n[[tool.uv.index]]\nname = 'probe'\nurl = %q\ndefault = true\n", srv.URL+"/simple/")
	} else {
		t.Setenv("UV_DEFAULT_INDEX", "http://"+mirrorUser+":"+mirrorToken+"@"+strings.TrimPrefix(srv.URL, "http://")+"/simple/")
	}

	dir := t.TempDir()
	writeScheduleProbeEditable(t, filepath.Join(dir, "editable"), "probe_editable")
	writeScheduleProbeEditable(t, filepath.Join(dir, "workspace"), "probe_workspace")
	localName, localWheel := scheduleProbeWheel(t, appDependency)
	if err := os.WriteFile(filepath.Join(dir, localName), localWheel, 0o600); err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string]string{
		"pyproject.toml": "[project]\nname = 'schedule-probe'\nversion = '1.0'\nrequires-python = '>=3.9'\ndependencies = ['" + appDependency + "', 'probe-editable', 'probe-workspace']\n" +
			"[dependency-groups]\ndev = ['" + devDependency + "']\n" +
			"[tool.uv.sources]\n" + appDependency + " = {path = '" + localName + "'}\n" +
			"probe-editable = {path = 'editable', editable = true}\nprobe-workspace = {workspace = true}\n" +
			"[tool.uv.workspace]\nmembers = ['workspace']\n" + indexConfig,
		"app.py": "# managed Python app\n",
		"job.py": `import os
import subprocess
import sys
from shinyhub_job_dependency import VALUE
from probe_editable import VALUE as EDITABLE_VALUE
from probe_workspace import VALUE as WORKSPACE_VALUE

assert VALUE == 42
assert EDITABLE_VALUE == 'probe_editable' and WORKSPACE_VALUE == 'probe_workspace'
if sys.argv[1] in ('success', 'failure'):
    import opentelemetry_instrumentation_logging
    from shinyhub_job_dev_dependency import VALUE as DEV_VALUE
    assert DEV_VALUE == 42
    assert os.path.samefile(os.environ['VIRTUAL_ENV'], sys.prefix)
    child_prefix = subprocess.check_output(['python', '-c', 'import sys; print(sys.prefix)'], text=True).strip()
    assert os.path.samefile(child_prefix, sys.prefix)
else:
    from importlib.util import find_spec
    assert find_spec('opentelemetry_instrumentation_logging') is None
with open('executions', 'a') as marker:
    marker.write(sys.argv[1] + '\n')
raise SystemExit(7 if sys.argv[1] == 'failure' else 0)
`,
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	manager := process.NewManager(dir, process.NewNativeRuntime())
	manager.SetAutoInstrumentAppsDefault(!scenario.upgrade)
	manager.SetAutoInstrumentExtraPackages(extras)
	params := deploy.Params{Slug: "test-app", BundleDir: dir, Manager: manager, PrepareOnly: true}
	if _, err := deploy.Run(params); err != nil {
		t.Fatal(err)
	}
	if scenario.upgrade {
		manager.SetAutoInstrumentAppsDefault(true)
		checkState := func(want string) {
			t.Helper()
			got, err := deploy.NativePythonInstrumentationState(dir, true, extras)
			if err != nil || got != want {
				t.Fatalf("instrumentation state = %q, %v, want %q", got, err, want)
			}
		}
		prepare := func(publish func(func() error) error) error {
			if scenario.isolated {
				process.SetHostCommandRunner(func(ctx context.Context, cwd string, argv, env []string, out io.Writer) error {
					createsStage := argv[0] == "mkdir" || argv[0] == "uv" && argv[1] == "venv"
					if cwd != dir || createsStage && !strings.HasPrefix(argv[len(argv)-1], filepath.Join(dir, ".venv")+string(filepath.Separator)) {
						return fmt.Errorf("native broker build staging must stay within the app tree: %q", argv)
					}
					cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
					cmd.Dir, cmd.Env, cmd.Stdout, cmd.Stderr = cwd, env, out, out
					return cmd.Run()
				})
				defer process.SetHostCommandRunner(nil)
			}
			return deploy.PrepareExistingPythonInstrumentation(context.Background(), params, publish)
		}
		checkState("not_prepared")
		outage.Store(true)
		if err := prepare(func(commit func() error) error {
			t.Fatal("failed resolution must not publish an environment")
			return nil
		}); err == nil {
			t.Fatal("expected package resolution to fail during the outage")
		}
		checkState("not_prepared")
		outage.Store(false)
		manager.SetAutoInstrumentExtraPackages([]string{"probe-instrumentation-conflict"})
		if err := prepare(func(commit func() error) error {
			t.Fatal("incompatible dependencies must not publish an environment")
			return nil
		}); err == nil || !strings.Contains(err.Error(), "shinyhub-job-dependency") {
			t.Fatalf("expected a resolution conflict with the deployed app dependency, got %v", err)
		}
		manager.SetAutoInstrumentExtraPackages(extras)
		if err := prepare(func(commit func() error) error {
			checkState("not_prepared")
			return commit()
		}); err != nil {
			t.Fatal(err)
		}
		checkState("ready")
		if entries, err := filepath.Glob(filepath.Join(dir, ".venv", ".shinyhub-otel-build-*")); err != nil || len(entries) != 0 {
			t.Fatalf("left repair staging behind: %v, %v", entries, err)
		}
	}
	if requests.Load() == 0 {
		t.Fatal("build never reached the test index")
	}

	// Linux builds and sandboxed workloads use the version-local uv cache.
	// Move it out of the bundle as well as starting with an empty global cache.
	if err := os.Rename(filepath.Join(dir, ".uv-cache"), filepath.Join(t.TempDir(), "uv-cache")); err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	t.Setenv("UV_CACHE_DIR", t.TempDir())
	outage.Store(true)
	requests.Store(0)
	tracer, _ := newJobsTracer(t)
	runtimeDir := t.TempDir()

	for _, tc := range []struct {
		argv, status string
		code         int
		newSettings  bool
	}{
		{`["uv","run","--locked","--no-sync","--no-index","python","job.py","success"]`, "succeeded", 0, false},
		{`["uv","run","python","job.py","failure"]`, "failed", 7, false},
		{`["uv","run","--frozen","--no-sync","--no-dev","python","job.py","custom"]`, "succeeded", 0, false},
		{`["python","job.py","fallback"]`, "succeeded", 0, true},
		{`["uv","run","python","job.py","declared-fallback"]`, "succeeded", 0, true},
	} {
		// A terminal row is visible before the scheduler drops its overlap
		// fence. Independent schedules let each command exercise RunOnce.
		schedule, app := makeSchedule("skip", 30), makeApp()
		schedule.CommandJSON = tc.argv
		store := scheduleProbeStore{fakeStore: newFakeStore(schedule, app), finished: make(chan struct{})}
		store.deployments[0].BundleDir = dir
		jobManager, err := jobs.NewManager(manager, nil, process.DefaultTier, store, nil, runtimeDir, runtimeDir)
		if err != nil {
			t.Fatal(err)
		}
		cfg := enabledTracing
		cfg.AutoInstrumentApps = true
		cfg.AutoInstrumentExtraPackages = extras
		if tc.newSettings {
			cfg.AutoInstrumentExtraPackages = []string{"a-different-instrumentation-package"}
		}
		jobManager.SetTracing(cfg, tracer)
		id, err := jobManager.Run(schedule.ID, "manual", nil)
		if err != nil {
			t.Fatal(err)
		}
		select {
		case <-store.finished:
		case <-time.After(45 * time.Second):
			t.Fatalf("schedule run %d never reached a terminal row", id)
		}
		row, err := store.GetScheduleRun(id)
		if err != nil {
			t.Fatal(err)
		}
		if row.Status != tc.status || row.ExitCode == nil || *row.ExitCode != tc.code {
			log, _ := os.ReadFile(store.logPaths[len(store.logPaths)-1].Path)
			t.Fatalf("command %s did not finish as %s with exit %d: %+v\n%s", tc.argv, tc.status, tc.code, row, log)
		}
	}
	marker, err := os.ReadFile(filepath.Join(dir, "executions"))
	if err != nil || string(marker) != "success\nfailure\ncustom\nfallback\ndeclared-fallback\n" {
		t.Fatalf("expected each payload to execute once: %q, %v", marker, err)
	}
	if n := requests.Load(); n != 0 {
		t.Fatalf("jobs made %d package-index requests during the outage", n)
	}
}

// A local PEP 660 backend makes the fixture independent of public build
// dependencies. Its wheel exposes the source directory through a .pth file.
func writeScheduleProbeEditable(t *testing.T, dir, module string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	project := fmt.Sprintf("[project]\nname = %q\nversion = '1.0'\nrequires-python = '>=3.9'\n[build-system]\nrequires = []\nbuild-backend = 'backend'\nbackend-path = ['.']\n", strings.ReplaceAll(module, "_", "-"))
	backend := fmt.Sprintf(`from pathlib import Path
from zipfile import ZipFile

def build_editable(wheel_directory, config_settings=None, metadata_directory=None):
    module = %q
    dist = module + '-1.0.dist-info/'
    wheel = module + '-1.0-py3-none-any.whl'
    with ZipFile(Path(wheel_directory) / wheel, 'w') as package:
        package.writestr(module + '.pth', str(Path(__file__).parent) + '\n')
        package.writestr(dist + 'WHEEL', 'Wheel-Version: 1.0\nRoot-Is-Purelib: true\nTag: py3-none-any\n')
        package.writestr(dist + 'METADATA', 'Metadata-Version: 2.1\nName: ' + module.replace('_', '-') + '\nVersion: 1.0\n')
        package.writestr(dist + 'RECORD', '')
    return wheel
`, module)
	for name, body := range map[string]string{"pyproject.toml": project, "backend.py": backend, module + ".py": fmt.Sprintf("VALUE = %q\n", module)} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

func scheduleProbeWheel(t *testing.T, pkg string) (string, []byte) {
	t.Helper()
	mod := strings.ReplaceAll(pkg, "-", "_")
	dist := mod + "-1.0.dist-info/"
	files := map[string]string{
		mod + ".py":       "VALUE = 42\n",
		dist + "WHEEL":    "Wheel-Version: 1.0\nRoot-Is-Purelib: true\nTag: py3-none-any\n",
		dist + "METADATA": "Metadata-Version: 2.1\nName: " + pkg + "\nVersion: 1.0\n",
		dist + "RECORD":   "",
	}
	if pkg == "opentelemetry-distro" {
		files[dist+"entry_points.txt"] = "[console_scripts]\nopentelemetry-instrument = opentelemetry_distro:run\n"
		files[mod+".py"] = "import os, sys\ndef run():\n    os.execv(sys.argv[1], sys.argv[1:])\n"
	}
	if pkg == "opentelemetry-instrumentation-logging" {
		files[dist+"METADATA"] += "Requires-Dist: shinyhub-job-dependency>=1\n"
	}
	if pkg == "probe-instrumentation-conflict" {
		files[dist+"METADATA"] += "Requires-Dist: shinyhub-job-dependency>=2\n"
	}
	var wheel bytes.Buffer
	z := zip.NewWriter(&wheel)
	for name, body := range files {
		w, err := z.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := fmt.Fprint(w, body); err != nil {
			t.Fatal(err)
		}
	}
	if err := z.Close(); err != nil {
		t.Fatal(err)
	}
	return mod + "-1.0-py3-none-any.whl", wheel.Bytes()
}
