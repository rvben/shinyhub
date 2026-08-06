package deploy_test

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/rvben/shinyhub/internal/deploy"
	"github.com/rvben/shinyhub/internal/process"
	"github.com/rvben/shinyhub/internal/proxy"
)

const renvActivateProfile = `source("renv/activate.R")` + "\n"

// bareLockfileBundle is the layout the dashboard's empty state advertises:
// app.R plus renv.lock, with none of renv's activation files. Nothing in it
// puts a writable library on .libPaths(), so before the interposed library
// existed the restore targeted a system library and failed outright under any
// hardened confinement.
func bareLockfileBundle(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	writeFiles(t, dir, map[string]string{
		"app.R":     "shiny::shinyApp(ui, server)\n",
		"renv.lock": `{"R":{"Version":"4.6.1"},"Packages":{}}`,
	})
	return dir
}

// renvActivatedBundle is what renv::init() produces: the project library is
// renv's own, and ShinyHub must not interpose on it.
func renvActivatedBundle(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	writeFiles(t, dir, map[string]string{
		"app.R":           "shiny::shinyApp(ui, server)\n",
		"renv.lock":       `{"R":{"Version":"4.6.1"},"Packages":{}}`,
		".Rprofile":       renvActivateProfile,
		"renv/activate.R": "# renv bootstrap\n",
	})
	return dir
}

func writeFiles(t *testing.T, dir string, files map[string]string) {
	t.Helper()
	for name, body := range files {
		p := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func joinCmd(cmd []string) string { return strings.Join(cmd, " ") }

// rExpr returns the expression an Rscript argv passes to -e.
func rExpr(t *testing.T, cmd []string) string {
	t.Helper()
	for i, a := range cmd {
		if a == "-e" && i+1 < len(cmd) {
			return cmd[i+1]
		}
	}
	t.Fatalf("no -e expression in %q", cmd)
	return ""
}

// TestResolveLaunch_BareLockfileR_SeesTheRestoredLibrary is the regression for
// the launch half of the bare-lockfile defect: restoring into the interposed
// library is worthless if the app process cannot see it.
func TestResolveLaunch_BareLockfileR_SeesTheRestoredLibrary(t *testing.T) {
	for _, reload := range []bool{false, true} {
		name := "plain"
		if reload {
			name = "reload"
		}
		t.Run(name, func(t *testing.T) {
			plan, err := deploy.ResolveLaunch(bareLockfileBundle(t), deploy.LaunchOptions{
				Port: 9300, BindHost: "127.0.0.1", Reload: reload,
			})
			if err != nil {
				t.Fatalf("ResolveLaunch: %v", err)
			}
			expr := rExpr(t, plan.Command)
			libPaths := strings.Index(expr, ".libPaths(")
			runApp := strings.Index(expr, "shiny::runApp(")
			switch {
			case libPaths < 0:
				t.Fatalf("launch does not put the interposed library on the search path: %s", expr)
			case runApp < 0:
				t.Fatalf("launch does not call shiny::runApp: %s", expr)
			case libPaths > runApp:
				// runApp never returns, so a later .libPaths() call never runs.
				t.Fatalf(".libPaths() comes after shiny::runApp: %s", expr)
			}
			if !strings.Contains(expr, "'"+process.RProjectLibraryDir+"', .libPaths()") {
				t.Fatalf("the interposed library is not first on the search path: %s", expr)
			}
		})
	}
}

// TestResolveLaunch_RenvActivatedR_IsUnchanged pins that the working path stays
// exactly as it was: renv's activation already selects the project library, and
// prepending another one would shadow it with an empty directory.
func TestResolveLaunch_RenvActivatedR_IsUnchanged(t *testing.T) {
	plan, err := deploy.ResolveLaunch(renvActivatedBundle(t), deploy.LaunchOptions{
		Port: 9301, BindHost: "127.0.0.1",
	})
	if err != nil {
		t.Fatalf("ResolveLaunch: %v", err)
	}
	if expr := rExpr(t, plan.Command); strings.Contains(expr, ".libPaths(") {
		t.Fatalf("interposed on an renv-activated bundle: %s", expr)
	}
}

// TestResolveLaunch_R_CarriesTheRenvPolicy pins the sandbox setting onto the
// launch plan. It has to be environment, not an options() call: renv reads its
// configuration while R sources the project profile, before any -e expression
// runs.
func TestResolveLaunch_R_CarriesTheRenvPolicy(t *testing.T) {
	plan, err := deploy.ResolveLaunch(renvActivatedBundle(t), deploy.LaunchOptions{
		Port: 9302, BindHost: "127.0.0.1",
	})
	if err != nil {
		t.Fatalf("ResolveLaunch: %v", err)
	}
	for _, want := range process.RenvPolicyEnv() {
		if !containsString(plan.Env, want) {
			t.Fatalf("plan.Env = %q, want %q", plan.Env, want)
		}
	}
	if !containsString(plan.Env, "PORT=9302") {
		t.Fatalf("plan.Env = %q, want the allocated PORT preserved", plan.Env)
	}
}

// TestResolveLaunch_ManifestCommandR_CarriesTheRenvPolicy covers the path that
// never reaches type detection. A bundle declaring its own [app] command (a
// plumber API, a custom Rscript entrypoint) returns from ResolveLaunch before
// the R branch, but renv still activates at R startup and still builds the
// read-only sandbox that made the app undeletable - so the policy has to be
// decided by the bundle, not by the resolved app type.
func TestResolveLaunch_ManifestCommandR_CarriesTheRenvPolicy(t *testing.T) {
	dir := t.TempDir()
	writeFiles(t, dir, map[string]string{
		"plumber.R":     "#* @get /\nfunction() 'ok'\n",
		"renv.lock":     `{"R":{"Version":"4.6.1"},"Packages":{}}`,
		"shinyhub.toml": "[app]\ncommand = [\"Rscript\", \"api.R\", \"{port}\"]\n",
	})
	plan, err := deploy.ResolveLaunch(dir, deploy.LaunchOptions{Port: 9304, BindHost: "127.0.0.1"})
	if err != nil {
		t.Fatalf("ResolveLaunch: %v", err)
	}
	if joined := joinCmd(plan.Command); !strings.Contains(joined, "api.R") {
		t.Fatalf("manifest command was not used: %s", joined)
	}
	for _, want := range process.RenvPolicyEnv() {
		if !containsString(plan.Env, want) {
			t.Fatalf("plan.Env = %q, want %q", plan.Env, want)
		}
	}
}

// TestResolveLaunch_RWithoutRenv_HasNoPolicy keeps the policy off a bundle that
// cannot activate renv at all: with no lockfile and no activation script there
// is no sandbox to suppress.
func TestResolveLaunch_RWithoutRenv_HasNoPolicy(t *testing.T) {
	dir := t.TempDir()
	writeFiles(t, dir, map[string]string{"app.R": "shiny::shinyApp(ui, server)\n"})
	plan, err := deploy.ResolveLaunch(dir, deploy.LaunchOptions{Port: 9305, BindHost: "127.0.0.1"})
	if err != nil {
		t.Fatalf("ResolveLaunch: %v", err)
	}
	for _, kv := range plan.Env {
		if strings.HasPrefix(kv, "RENV_") {
			t.Fatalf("plan carries an renv setting for a bundle with no renv: %s", kv)
		}
	}
}

// TestResolveLaunch_Python_HasNoRenvPolicy keeps the policy scoped to the app
// type that needs it.
func TestResolveLaunch_Python_HasNoRenvPolicy(t *testing.T) {
	dir := t.TempDir()
	writeFiles(t, dir, map[string]string{"app.py": "app = 1\n"})
	plan, err := deploy.ResolveLaunch(dir, deploy.LaunchOptions{Port: 9303, BindHost: "127.0.0.1"})
	if err != nil {
		t.Fatalf("ResolveLaunch: %v", err)
	}
	for _, kv := range plan.Env {
		if strings.HasPrefix(kv, "RENV_") {
			t.Fatalf("python plan carries an renv setting: %s", kv)
		}
	}
}

// TestRun_RReplicaReceivesTheLaunchPlanEnv drives the real server boot path and
// asserts the environment the plan resolved actually reaches the process. The
// boot path used to rebuild the launch env from PORT alone, so anything else
// the plan resolved (here, the renv policy that keeps the app deletable) was
// silently dropped between resolution and Start - while `shinyhub run` and the
// elastic spawner, which consume plan.Env directly, got it.
func TestRun_RReplicaReceivesTheLaunchPlanEnv(t *testing.T) {
	bundle := renvActivatedBundle(t)
	rt := &recordingRuntime{}
	mgr := process.NewManager(t.TempDir(), rt)
	defer mgr.Stop("r-env")

	if _, err := deploy.Run(deploy.Params{
		Slug:        "r-env",
		BundleDir:   bundle,
		Replicas:    1,
		Manager:     mgr,
		Proxy:       proxy.New(),
		HealthCheck: func(string, time.Duration, http.RoundTripper) error { return nil },
	}); err != nil {
		t.Fatalf("deploy.Run: %v", err)
	}

	starts := rt.recorded()
	if len(starts) != 1 {
		t.Fatalf("want 1 recorded Start, got %d", len(starts))
	}
	for _, want := range process.RenvPolicyEnv() {
		if !containsString(starts[0].Env, want) {
			t.Fatalf("StartParams.Env = %q, want %q", starts[0].Env, want)
		}
	}
	// The port the plan allocated must still be the one the replica is told to
	// use, or replacing the hand-rolled PORT with plan.Env broke the launch.
	if !containsString(starts[0].Env, "PORT="+itoa(starts[0].Port)) {
		t.Fatalf("StartParams.Env = %q, want PORT=%d", starts[0].Env, starts[0].Port)
	}
}

// TestRun_BareLockfileRReplicaSeesTheInterposedLibrary is the same end-to-end
// check for the command half: the argv the server actually launches.
func TestRun_BareLockfileRReplicaSeesTheInterposedLibrary(t *testing.T) {
	rt := &recordingRuntime{}
	mgr := process.NewManager(t.TempDir(), rt)
	defer mgr.Stop("r-lib")

	if _, err := deploy.Run(deploy.Params{
		Slug:        "r-lib",
		BundleDir:   bareLockfileBundle(t),
		Replicas:    1,
		Manager:     mgr,
		Proxy:       proxy.New(),
		HealthCheck: func(string, time.Duration, http.RoundTripper) error { return nil },
	}); err != nil {
		t.Fatalf("deploy.Run: %v", err)
	}
	starts := rt.recorded()
	if len(starts) != 1 {
		t.Fatalf("want 1 recorded Start, got %d", len(starts))
	}
	if cmd := joinCmd(starts[0].Command); !strings.Contains(cmd, process.RProjectLibraryDir) {
		t.Fatalf("launched command does not reference the interposed library: %s", cmd)
	}
}

// TestHostEnvironmentReady_BareLockfileChecksTheInterposedLibrary pins that the
// "can this bundle start without building" check looks where the packages
// actually are. Checking renv/library for a bare-lockfile bundle answers no
// forever, so every elastic worker spawn is refused for an app that is in fact
// fully built.
func TestHostEnvironmentReady_BareLockfileChecksTheInterposedLibrary(t *testing.T) {
	dir := bareLockfileBundle(t)
	if deploy.HostEnvironmentReady(dir, "r") {
		t.Fatal("reported ready before the library was restored")
	}
	if err := os.MkdirAll(filepath.Join(dir, process.RProjectLibraryDir), 0o755); err != nil {
		t.Fatal(err)
	}
	if !deploy.HostEnvironmentReady(dir, "r") {
		t.Fatal("reported not ready with the interposed library present")
	}
	// A restored renv/library must not satisfy a bundle whose packages live in
	// the interposed one, and vice versa.
	other := renvActivatedBundle(t)
	if err := os.MkdirAll(filepath.Join(other, process.RProjectLibraryDir), 0o755); err != nil {
		t.Fatal(err)
	}
	if deploy.HostEnvironmentReady(other, "r") {
		t.Fatal("an renv-activated bundle was satisfied by the interposed library")
	}
	if err := os.MkdirAll(filepath.Join(other, "renv", "library"), 0o755); err != nil {
		t.Fatal(err)
	}
	if !deploy.HostEnvironmentReady(other, "r") {
		t.Fatal("an renv-activated bundle with renv/library was reported not ready")
	}
}

func containsString(hay []string, needle string) bool {
	for _, s := range hay {
		if s == needle {
			return true
		}
	}
	return false
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}
