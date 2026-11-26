package jobs_test

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/rvben/shinyhub/internal/db"
	"github.com/rvben/shinyhub/internal/process"
)

// isSetup reports whether p is an environment setup rather than the command:
// it runs the server's `true` in place of the command.
func isSetup(t *testing.T, p process.StartParams) bool {
	return len(p.Command) > 0 && p.Command[len(p.Command)-1] == trueExecutable(t)
}

func isOffline(p process.StartParams) bool { return slices.Contains(p.Command, "--offline") }

// runProducer runs the fixture's producer as a deploy runs it and returns the
// run's status, its log, and whether the run was treated as a possible
// partial data write.
func runProducer(t *testing.T, f *producerCheckFixture) (status, log string, unsafe bool) {
	t.Helper()
	var unsafeCalls atomic.Int32
	f.manager.SetUnsafePublicationHandler(func(int64, string, string) { unsafeCalls.Add(1) })
	release := f.manager.AcquireProducerGates([]int64{f.schedule.ID})
	runID, _ := f.manager.RunCandidateProducerLocked(f.schedule, f.app, f.deployment)
	release()
	run, err := f.store.GetScheduleRun(runID)
	if err != nil {
		t.Fatalf("run %d: %v", runID, err)
	}
	raw, err := os.ReadFile(filepath.Join(f.appsDir, f.app.Slug, "schedules", fmt.Sprint(f.schedule.ID), fmt.Sprintf("run-%d.log", runID)))
	if err != nil {
		t.Fatalf("run log: %v", err)
	}
	return run.Status, string(raw), unsafeCalls.Load() > 0
}

// A run whose environment is in uv's cache builds it offline, without the
// package index, and then runs the command offline too, with the run's data
// access that the setup did not get.
func TestRun_BuildsTheEnvironmentFromTheCacheFirst(t *testing.T) {
	noop := trueExecutable(t)
	rt := &scriptedRuntime{}
	f := newProducerCheckFixture(t, rt, documentedProducer)

	status, _, unsafe := runProducer(t, f)
	if status != "succeeded" || unsafe {
		t.Fatalf("status = %q unsafe = %v, want a clean success", status, unsafe)
	}
	if len(rt.calls) != 2 {
		t.Fatalf("RunOnce calls = %d, want setup and command", len(rt.calls))
	}
	setup, command := rt.calls[0], rt.calls[1]
	if got, want := strings.Join(setup.Command, " "), "uv run --offline --with-requirements requirements.txt -- "+noop; got != want {
		t.Errorf("setup = %q, want %q", got, want)
	}
	if setup.AppDataPath != "" || len(setup.SharedMounts) != 0 || len(setup.LifetimeFiles) != 0 || setup.AppCachePath != "" {
		t.Errorf("setup was given data access: %+v", setup)
	}
	if !setup.EnvironmentOnly || command.EnvironmentOnly {
		t.Errorf("environmentOnly: setup = %v command = %v, want only the setup", setup.EnvironmentOnly, command.EnvironmentOnly)
	}
	if setup.Dir != f.bundleDir || !contains(setup.SecretEnv, "INDEX_TOKEN=index-token-1234") {
		t.Errorf("setup dir = %q secretEnv = %v, want the run's", setup.Dir, setup.SecretEnv)
	}
	if got, want := strings.Join(command.Command, " "), "uv run --offline --with-requirements requirements.txt python fetch.py"; got != want {
		t.Errorf("command = %q, want %q", got, want)
	}
	if command.AppDataPath == "" || len(command.LifetimeFiles) == 0 {
		t.Errorf("command lacks the run's data access: %+v", command)
	}
}

// cacheRuntime answers like uv with a cache: an offline setup succeeds once
// an online one has run, unless keepsNothing models a uv configured not to
// cache.
func cacheRuntime(t *testing.T, keepsNothing bool) *scriptedRuntime {
	cached := false
	return &scriptedRuntime{answer: func(p process.StartParams) (string, process.ExitInfo, error) {
		switch {
		case isSetup(t, p) && !isOffline(p):
			cached = !keepsNothing
		case isSetup(t, p) && !cached:
			return "offline miss\n", process.ExitInfo{Code: 1}, nil
		}
		return "", process.ExitInfo{}, nil
	}}
}

// When the cache lacks the environment, the setup builds it through the
// package index, and once an offline setup confirms the cache now serves it,
// the command runs offline.
func TestRun_BuildsTheEnvironmentThroughTheIndexWhenTheCacheLacksIt(t *testing.T) {
	rt := cacheRuntime(t, false)
	f := newProducerCheckFixture(t, rt, documentedProducer)

	status, log, unsafe := runProducer(t, f)
	if status != "succeeded" || unsafe {
		t.Fatalf("status = %q unsafe = %v, want a clean success", status, unsafe)
	}
	got := commands(rt.calls)
	if len(rt.calls) != 4 || !isSetup(t, rt.calls[0]) || !isOffline(rt.calls[0]) ||
		!isSetup(t, rt.calls[1]) || isOffline(rt.calls[1]) || !isSetup(t, rt.calls[2]) || !isOffline(rt.calls[2]) ||
		isSetup(t, rt.calls[3]) || !isOffline(rt.calls[3]) {
		t.Fatalf("calls = %v, want offline setup, online setup, offline setup, offline command", got)
	}
	if !strings.Contains(log, "through the package index") || strings.Contains(log, "offline miss") {
		t.Errorf("log = %q, want the fallback noted and the offline attempts' output dropped", log)
	}
}

// A uv that keeps nothing in its cache (no-cache in uv.toml, say) builds the
// environment through the index and still cannot serve it offline, so the
// command runs as declared rather than fail offline after a good setup.
func TestRun_CommandTheCacheCannotServeRunsAsDeclared(t *testing.T) {
	rt := cacheRuntime(t, true)
	f := newProducerCheckFixture(t, rt, documentedProducer)

	status, log, unsafe := runProducer(t, f)
	if status != "succeeded" || unsafe {
		t.Fatalf("status = %q unsafe = %v, want a clean success", status, unsafe)
	}
	last := rt.calls[len(rt.calls)-1]
	if len(rt.calls) != 4 || isSetup(t, last) || isOffline(last) {
		t.Fatalf("calls = %v, want three setups and the command as declared", commands(rt.calls))
	}
	if !strings.Contains(log, "running the command as declared") || strings.Contains(log, "offline miss") {
		t.Errorf("log = %q, want the declared run noted and the offline attempts' output dropped", log)
	}
}

// An environment that cannot be built fails the run before its command
// starts: it wrote nothing, so the run does not count as a possible partial
// data write. A command that starts and fails does, which is the control
// proving the run path reaches the handler.
func TestRun_EnvironmentFailureIsNotADataWrite(t *testing.T) {
	rt := &scriptedRuntime{answer: func(p process.StartParams) (string, process.ExitInfo, error) {
		if isSetup(t, p) {
			return "No solution found: python-dateutil==4.0.0 is unsatisfiable\n", process.ExitInfo{Code: 1}, nil
		}
		return "", process.ExitInfo{}, nil
	}}
	f := newProducerCheckFixture(t, rt, documentedProducer)

	status, log, unsafe := runProducer(t, f)
	if status != "failed" || unsafe {
		t.Fatalf("status = %q unsafe = %v, want failed without a data write", status, unsafe)
	}
	if len(rt.calls) != 2 || !isSetup(t, rt.calls[0]) || !isSetup(t, rt.calls[1]) {
		t.Fatalf("calls = %v, want two setups and no command", commands(rt.calls))
	}
	if !strings.Contains(log, "unsatisfiable") || !strings.Contains(log, "the command was not started") {
		t.Errorf("log = %q, want uv's diagnostic and that the command did not start", log)
	}

	control := &scriptedRuntime{answer: func(p process.StartParams) (string, process.ExitInfo, error) {
		if isSetup(t, p) {
			return "", process.ExitInfo{}, nil
		}
		return "", process.ExitInfo{Code: 1}, nil
	}}
	g := newProducerCheckFixture(t, control, documentedProducer)
	if status, _, unsafe := runProducer(t, g); status != "failed" || !unsafe {
		t.Fatalf("control: status = %q unsafe = %v, want a failed command treated as a possible write", status, unsafe)
	}
}

// A runtime that prepares dependencies in its container keeps no uv cache
// across runs, so an offline command would always miss: the command runs as
// declared, in one step.
func TestRun_ContainerRuntimeRunsTheCommandAsDeclared(t *testing.T) {
	rt := &scriptedRuntime{containerDeps: true}
	f := newProducerCheckFixture(t, rt, documentedProducer)
	if status, _, _ := runProducer(t, f); status != "succeeded" {
		t.Fatalf("status = %q", status)
	}
	if len(rt.calls) != 1 || isOffline(rt.calls[0]) || isSetup(t, rt.calls[0]) {
		t.Fatalf("calls = %v, want the command alone, as declared", commands(rt.calls))
	}
}

// A command that does not launch uv, or one whose environment the probe
// cannot reproduce, runs as declared, in one step.
func TestRun_OtherCommandsRunAsDeclared(t *testing.T) {
	for _, command := range []string{`["python","fetch.py"]`, `["uv","run","--script","fetch.py"]`} {
		rt := &scriptedRuntime{}
		f := newProducerCheckFixture(t, rt, command)
		if status, _, _ := runProducer(t, f); status != "succeeded" {
			t.Fatalf("%s: status = %q", command, status)
		}
		if len(rt.calls) != 1 || isOffline(rt.calls[0]) {
			t.Fatalf("%s: calls = %v, want the command alone, as declared", command, commands(rt.calls))
		}
	}
}

// A command that disables uv's cache leaves nothing an offline command could
// run from, so it runs as declared, in one step: by flag, by app env var, or
// by a server env var the launch inherits.
func TestRun_CommandWithoutUVsCacheRunsAsDeclared(t *testing.T) {
	for _, tc := range []struct {
		name, command string
		noCacheEnv    bool
		serverEnv     bool
	}{
		{"flag", `["uv","run","--no-cache","--with-requirements","requirements.txt","python","fetch.py"]`, false, false},
		{"env", documentedProducer, true, false},
		{"server env", documentedProducer, false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rt := &scriptedRuntime{}
			f := newProducerCheckFixture(t, rt, tc.command)
			if tc.noCacheEnv {
				f.store.envVars = append(f.store.envVars, db.AppEnvVar{Key: "UV_NO_CACHE", Value: []byte("1")})
			}
			if tc.serverEnv {
				t.Setenv("SHINYHUB_APP_ENV_ALLOW", "UV_NO_CACHE")
				t.Setenv("UV_NO_CACHE", "1")
			}
			if status, _, _ := runProducer(t, f); status != "succeeded" {
				t.Fatalf("status = %q", status)
			}
			if len(rt.calls) != 1 || isOffline(rt.calls[0]) || isSetup(t, rt.calls[0]) {
				t.Fatalf("calls = %v, want the command alone, as declared", commands(rt.calls))
			}
		})
	}
}

func commands(calls []process.StartParams) []string {
	out := make([]string, 0, len(calls))
	for _, c := range calls {
		out = append(out, strings.Join(c.Command, " "))
	}
	return out
}
