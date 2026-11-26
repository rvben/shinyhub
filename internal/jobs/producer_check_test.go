package jobs_test

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rvben/shinyhub/internal/db"
	"github.com/rvben/shinyhub/internal/deployfail"
	"github.com/rvben/shinyhub/internal/jobs"
	"github.com/rvben/shinyhub/internal/process"
	"github.com/rvben/shinyhub/internal/secrets"
)

// scriptedRuntime is a local runtime whose RunOnce records what it was asked
// to run and answers with output and an exit code, or with what answer
// returns for the call when it is set.
type scriptedRuntime struct {
	process.Runtime
	mu     sync.Mutex
	calls  []process.StartParams
	output string
	exit   process.ExitInfo
	err    error
	answer func(p process.StartParams) (string, process.ExitInfo, error)
	// block, when set, makes RunOnce wait for ctx to end.
	block bool
	// containerDeps, when set, makes it a runtime whose dependencies are
	// prepared inside its container, as Docker's are.
	containerDeps bool
}

func (r *scriptedRuntime) HostProvidesAppData() bool   { return true }
func (r *scriptedRuntime) HostPreparesDeps() bool      { return !r.containerDeps }
func (r *scriptedRuntime) InheritsLifetimeFiles() bool { return true }

func (r *scriptedRuntime) RunOnce(ctx context.Context, p process.StartParams, w io.Writer) (process.ExitInfo, error) {
	r.mu.Lock()
	r.calls = append(r.calls, p)
	r.mu.Unlock()
	if r.block {
		<-ctx.Done()
		return process.ExitInfo{Code: -1, Signaled: true}, nil
	}
	if r.answer != nil {
		out, exit, err := r.answer(p)
		_, _ = io.WriteString(w, out)
		return exit, err
	}
	_, _ = io.WriteString(w, r.output)
	return r.exit, r.err
}

type producerCheckFixture struct {
	appsDir    string
	bundleDir  string
	manager    *jobs.Manager
	store      *fakeStore
	schedule   *db.Schedule
	app        *db.App
	deployment *db.Deployment
}

func newProducerCheckFixture(t *testing.T, rt process.Runtime, command string) *producerCheckFixture {
	t.Helper()
	appsDir := t.TempDir()
	bundleDir := filepath.Join(appsDir, "test-app", "versions", "v2")
	for name, content := range map[string]string{
		"requirements.txt": "python-dateutil==4.0.0\n",
		"fetch.py":         "print('fetch')\n",
	} {
		path := filepath.Join(bundleDir, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	app := makeApp()
	schedule := makeSchedule("concurrent", 30)
	schedule.Name = "refresh-data"
	schedule.DeployTrigger = "bundle_change"
	schedule.CommandJSON = command
	st := newFakeStore(schedule, app)
	key := secrets.DeriveKey("test-auth-secret")
	token, err := secrets.Encrypt(key, []byte("index-token-1234"))
	if err != nil {
		t.Fatal(err)
	}
	st.envVars = []db.AppEnvVar{
		{Key: "REGION", Value: []byte("eu-west-1")},
		{Key: "INDEX_TOKEN", Value: token, IsSecret: true},
	}
	m, err := jobs.NewManager(process.NewManager(appsDir, rt), nil, process.DefaultTier, st, key, appsDir, filepath.Join(appsDir, "app-data"))
	if err != nil {
		t.Fatal(err)
	}
	return &producerCheckFixture{
		appsDir: appsDir, bundleDir: bundleDir, manager: m, store: st, schedule: schedule, app: app,
		deployment: &db.Deployment{ID: 9, AppID: app.ID, Version: "v2", BundleDir: bundleDir, ContentDigest: "sha256:v2", Status: db.DeploymentPending},
	}
}

func (f *producerCheckFixture) check(t *testing.T) (jobs.ProducerEnvironmentCheck, error) {
	t.Helper()
	return f.manager.CheckCandidateProducerEnvironment(context.Background(), f.schedule, f.app, f.deployment)
}

const documentedProducer = `["uv","run","--with-requirements","requirements.txt","python","fetch.py"]`

// The check runs the producer's own uv options with the server's own `true`
// in place of its command, with the producer's env, in the prepared bundle
// itself, so the environment and uv cache it builds are the ones the
// producer's runs use: offline from uv's cache first, then, when the cache
// lacks it, through the package index. It gets none of a run's data access:
// no data dir, mounts, cache, locks or job cgroup, and no schedule run is
// recorded.
func TestCheckCandidateProducerEnvironment_ProbesTheBundle(t *testing.T) {
	rt := cacheRuntime(t, false)
	f := newProducerCheckFixture(t, rt, documentedProducer)

	result, err := f.check(t)
	if err != nil {
		t.Fatalf("check: %v", err)
	}
	if !result.Checked || result.NotChecked != "" || result.Schedule != "refresh-data" {
		t.Fatalf("result = %+v, want checked", result)
	}
	want := []string{
		"uv run --offline --with-requirements requirements.txt -- " + trueExecutable(t),
		"uv run --with-requirements requirements.txt -- " + trueExecutable(t),
	}
	if got := commands(rt.calls); !slices.Equal(got, want) {
		t.Fatalf("commands = %q, want %q", got, want)
	}
	for _, p := range rt.calls {
		if p.Dir != f.bundleDir || !p.EnvironmentOnly {
			t.Errorf("dir = %q environmentOnly = %v, want an environment-only launch in the bundle %q", p.Dir, p.EnvironmentOnly, f.bundleDir)
		}
		if p.AppDataPath != "" || len(p.SharedMounts) != 0 || len(p.LifetimeFiles) != 0 || p.JobRunID != 0 || p.AppCachePath != "" || p.MemoryLimitMB != 0 {
			t.Errorf("check was given run access: %+v", p)
		}
		if !contains(p.Env, "REGION=eu-west-1") || !contains(p.SecretEnv, "INDEX_TOKEN=index-token-1234") {
			t.Errorf("env = %v secretEnv = %v, want the producer's", p.Env, p.SecretEnv)
		}
		if p.JobSchedule != "refresh-data" || p.Tier != process.DefaultTier {
			t.Errorf("schedule = %q tier = %q", p.JobSchedule, p.Tier)
		}
	}
	if len(f.store.runs) != 0 {
		t.Errorf("check recorded %d schedule runs", len(f.store.runs))
	}
}

// An environment uv's cache serves offline passes without the package index,
// so an index outage cannot fail a deploy or rollback whose producers would
// run.
func TestCheckCandidateProducerEnvironment_ServedFromTheCache(t *testing.T) {
	rt := &scriptedRuntime{answer: func(p process.StartParams) (string, process.ExitInfo, error) {
		if !isOffline(p) {
			return "error: Failed to fetch: `https://pypi.org/simple/six/`\n", process.ExitInfo{Code: 2}, nil
		}
		return "", process.ExitInfo{}, nil
	}}
	f := newProducerCheckFixture(t, rt, documentedProducer)

	result, err := f.check(t)
	if err != nil || !result.Checked {
		t.Fatalf("result = %+v err = %v, want checked from the cache", result, err)
	}
	if len(rt.calls) != 1 || !isOffline(rt.calls[0]) {
		t.Fatalf("commands = %q, want only the offline probe", commands(rt.calls))
	}
}

func trueExecutable(t *testing.T) string {
	t.Helper()
	path, err := exec.LookPath("true")
	if err != nil || !filepath.IsAbs(path) {
		t.Skipf("no absolute true executable: %q %v", path, err)
	}
	return path
}

// An environment uv cannot build fails the check with uv's own diagnostic,
// classified as a build failure, with credentials and escapes removed.
func TestCheckCandidateProducerEnvironment_UnresolvableFails(t *testing.T) {
	rt := &scriptedRuntime{
		exit: process.ExitInfo{Code: 1},
		output: "\x1b[31merror\x1b[0m: No solution found when resolving `--with` dependencies:\n" +
			"  Because python-dateutil==4.0.0 was not found at https://user:pw@pkgs.example/simple?token=abc and " +
			"index-token-1234 appears here, we can conclude that your requirements are unsatisfiable.\n",
	}
	f := newProducerCheckFixture(t, rt, documentedProducer)

	_, err := f.check(t)
	var envErr *jobs.ProducerEnvironmentError
	if !errors.As(err, &envErr) {
		t.Fatalf("err = %v, want a ProducerEnvironmentError", err)
	}
	if envErr.Schedule != "refresh-data" {
		t.Errorf("schedule = %q", envErr.Schedule)
	}
	if kind := deployfail.Classify(err); kind != deployfail.BuildFailed {
		t.Errorf("kind = %q, want build_failed", kind)
	}
	msg := err.Error()
	if !strings.Contains(msg, "unsatisfiable") || !strings.Contains(msg, `schedule "refresh-data"`) {
		t.Errorf("message lacks uv's diagnostic or the schedule: %q", msg)
	}
	for _, leak := range []string{"\x1b[", "index-token-1234", "user:pw", "token=abc"} {
		if strings.Contains(msg, leak) {
			t.Errorf("message carries %q: %q", leak, msg)
		}
	}
}

// A uv the runtime cannot launch is reported as a missing runtime, as a
// deploy's own build reports it.
func TestCheckCandidateProducerEnvironment_MissingUV(t *testing.T) {
	rt := &scriptedRuntime{err: errors.New(`resolve command "uv": exec: "uv": executable file not found in $PATH`)}
	f := newProducerCheckFixture(t, rt, documentedProducer)
	_, err := f.check(t)
	var envErr *jobs.ProducerEnvironmentError
	if !errors.As(err, &envErr) || deployfail.Classify(err) != deployfail.RuntimeMissing {
		t.Fatalf("err = %v kind = %q, want runtime_missing", err, deployfail.Classify(err))
	}
}

// A uv that ran and failed is a build failure even when its output quotes a
// build tool that could not find uv for its own subprocess: the server's uv
// launched, so the runtime is not missing.
func TestCheckCandidateProducerEnvironment_NestedMissingUVIsABuildFailure(t *testing.T) {
	rt := &scriptedRuntime{
		exit:   process.ExitInfo{Code: 1},
		output: "error: Failed to build `pkg==1.0`\n  backend: exec: \"uv\": executable file not found in $PATH\n",
	}
	f := newProducerCheckFixture(t, rt, documentedProducer)
	_, err := f.check(t)
	var envErr *jobs.ProducerEnvironmentError
	if !errors.As(err, &envErr) || deployfail.Classify(err) != deployfail.BuildFailed {
		t.Fatalf("err = %v kind = %q, want build_failed", err, deployfail.Classify(err))
	}
}

// A runtime error other than a missing uv means the check itself could not
// run, as when restoring the data entry the check kept absent fails after uv
// succeeded. It fails the deploy as a server error, not as an environment
// uv cannot build.
func TestCheckCandidateProducerEnvironment_RuntimeErrorIsNotABuildFailure(t *testing.T) {
	rt := &scriptedRuntime{err: errors.New("restore data entry: permission denied")}
	f := newProducerCheckFixture(t, rt, documentedProducer)
	_, err := f.check(t)
	if err == nil || !strings.Contains(err.Error(), "restore data entry: permission denied") {
		t.Fatalf("err = %v, want the runtime's error", err)
	}
	var envErr *jobs.ProducerEnvironmentError
	if errors.As(err, &envErr) {
		t.Errorf("err = %v reports an environment that cannot be built", err)
	}
	if kind := deployfail.Classify(err); kind != deployfail.ServerError {
		t.Errorf("kind = %q, want server_error", kind)
	}
}

// A producer whose command does not launch uv has no environment to check,
// and one the probe cannot reproduce is reported as not checked. Neither
// runs anything.
func TestCheckCandidateProducerEnvironment_NothingToRun(t *testing.T) {
	cases := []struct {
		command    string
		notChecked string
	}{
		{`["Rscript","fetch.R"]`, ""},
		{`["python","fetch.py"]`, ""},
		{`["uvx","fetch-tool"]`, "uvx"},
		{`["uv","run","--script","fetch.py"]`, "--script"},
	}
	for _, c := range cases {
		rt := &scriptedRuntime{}
		f := newProducerCheckFixture(t, rt, c.command)
		result, err := f.check(t)
		if err != nil {
			t.Errorf("%s: %v", c.command, err)
			continue
		}
		if result.Checked || !strings.Contains(result.NotChecked, c.notChecked) || (c.notChecked == "") != (result.NotChecked == "") {
			t.Errorf("%s: result = %+v, want notChecked %q", c.command, result, c.notChecked)
		}
		if len(rt.calls) != 0 {
			t.Errorf("%s: ran %d processes", c.command, len(rt.calls))
		}
	}
}

// The check is planned with the server env vars a launch inherits: a
// working directory in the app's data, passed through by the operator, is
// not checked rather than probed in a bundle that lacks it.
func TestCheckCandidateProducerEnvironment_PlansWithTheInheritedServerEnv(t *testing.T) {
	t.Setenv("SHINYHUB_APP_ENV_ALLOW", "UV_WORKING_DIR")
	t.Setenv("UV_WORKING_DIR", "data/project")
	rt := &scriptedRuntime{}
	f := newProducerCheckFixture(t, rt, `["uv","run","python","fetch.py"]`)
	result, err := f.check(t)
	if err != nil {
		t.Fatal(err)
	}
	if result.Checked || !strings.Contains(result.NotChecked, "UV_WORKING_DIR") {
		t.Fatalf("result = %+v, want not checked for UV_WORKING_DIR", result)
	}
	if len(rt.calls) != 0 {
		t.Fatalf("ran %d processes", len(rt.calls))
	}

	// The app's own value wins, as it does for the launch.
	rt = &scriptedRuntime{}
	f = newProducerCheckFixture(t, rt, `["uv","run","python","fetch.py"]`)
	f.store.envVars = append(f.store.envVars, db.AppEnvVar{Key: "UV_WORKING_DIR", Value: []byte(".")})
	if result, err = f.check(t); err != nil || !result.Checked {
		t.Fatalf("with the app's UV_WORKING_DIR: result = %+v err = %v, want checked", result, err)
	}
}

// A check that outlives the bundle's build budget is not checked rather than
// failed: the budget bounds the deploy's wait, not the producer.
func TestCheckCandidateProducerEnvironment_BuildBudget(t *testing.T) {
	rt := &scriptedRuntime{block: true}
	f := newProducerCheckFixture(t, rt, documentedProducer)
	jobs.SetProducerCheckBudget(t, 50*time.Millisecond)
	result, err := f.check(t)
	if err != nil {
		t.Fatalf("check: %v", err)
	}
	if result.Checked || !strings.Contains(result.NotChecked, "build budget") {
		t.Fatalf("result = %+v, want not checked for the budget", result)
	}
}

// A producer tier whose processes cannot hold the publication locks would
// refuse the run; the check refuses it before anything stops.
func TestCheckCandidateProducerEnvironment_RefusesUnfencedTier(t *testing.T) {
	base := &fakeRuntime{}
	f := newProducerCheckFixture(t, &unfencedRuntime{fakeRuntime: base}, documentedProducer)
	_, err := f.check(t)
	if err == nil || !strings.Contains(err.Error(), "local native tier") {
		t.Fatalf("err = %v, want the tier refusal", err)
	}
	if base.calls != 0 {
		t.Fatalf("ran %d processes", base.calls)
	}
}

// Against real uv on the native runtime: the conflicting pins of the
// incident fail the check, and a satisfiable set passes. --offline keeps it
// off the network; uv proves these pins unsatisfiable without an index.
func TestCheckCandidateProducerEnvironment_RealUV(t *testing.T) {
	if _, err := exec.LookPath("uv"); err != nil {
		t.Skip("uv not on PATH")
	}
	t.Setenv("UV_PYTHON_DOWNLOADS", "never")
	command := `["uv","run","--offline","--no-project","--with-requirements","requirements.txt","python","fetch.py"]`

	f := newProducerCheckFixture(t, process.NewNativeRuntime(), command)
	if err := os.WriteFile(filepath.Join(f.bundleDir, "requirements.txt"), []byte("six==1.16.0\nsix==1.15.0\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := f.check(t)
	var envErr *jobs.ProducerEnvironmentError
	if !errors.As(err, &envErr) || !strings.Contains(envErr.Output, "unsatisfiable") {
		t.Fatalf("conflicting pins: err = %v, want uv's unsatisfiable diagnostic", err)
	}

	g := newProducerCheckFixture(t, process.NewNativeRuntime(), command)
	if err := os.WriteFile(filepath.Join(g.bundleDir, "requirements.txt"), []byte("\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	result, err := g.check(t)
	if err != nil || !result.Checked {
		t.Fatalf("empty requirements: result = %+v err = %v, want checked", result, err)
	}

	// A run of a producer that does not ask for --offline itself builds its
	// environment from the cache and runs the command with --offline, which
	// real uv accepts in the position the run puts it.
	h := newProducerCheckFixture(t, process.NewNativeRuntime(), `["uv","run","--no-project","--with-requirements","requirements.txt","python","fetch.py"]`)
	if err := os.WriteFile(filepath.Join(h.bundleDir, "requirements.txt"), []byte("\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if result, err := h.check(t); err != nil || !result.Checked {
		t.Fatalf("online check: result = %+v err = %v, want checked", result, err)
	}
	status, log, _ := runProducer(t, h)
	if status != "succeeded" || !strings.Contains(log, "fetch") || strings.Contains(log, "package index") {
		t.Fatalf("run: status = %q log = %q, want the command run from the cache", status, log)
	}
}
