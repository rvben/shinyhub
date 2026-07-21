package deploy_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rvben/shinyhub/internal/deploy"
	"github.com/rvben/shinyhub/internal/process"
	"github.com/rvben/shinyhub/internal/proxy"
)

// Elastic pools (grouped / per_session) spawn their workers on demand, long
// after the deploy call returns. That makes the deploy the only moment at which
// a declared build step can run exactly once, before any worker serves a
// request, and still fail the deploy when it fails. These tests pin that
// contract: an elastic app must get the same preparation phase (dependency
// build, then post-deploy hooks) that a fixed-replica pool gets.

// writeElasticBundle writes a minimal python bundle whose manifest declares
// grouped isolation plus the supplied extra TOML.
func writeElasticBundle(t *testing.T, extra string) string {
	t.Helper()
	bundle := t.TempDir()
	if err := os.WriteFile(filepath.Join(bundle, "app.py"), []byte(""), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bundle, deploy.ManifestFilename), []byte(extra), 0644); err != nil {
		t.Fatal(err)
	}
	return bundle
}

// stubBundleBuild neutralises the host-side dependency build so a test can
// deploy a bundle without invoking uv or Rscript. Returns a restore func.
func stubBundleBuild(t *testing.T) func() {
	t.Helper()
	restoreSync := deploy.SetSyncHooksForTest(
		func(context.Context, string, []string) error { return nil },
		func(context.Context, string, []string) error { return nil },
	)
	restoreProject := deploy.SetEnsureProjectForTest(func(context.Context, string) error { return nil })
	return func() { restoreProject(); restoreSync() }
}

// elasticParams builds Params that resolve to the grouped elastic mode.
func elasticParams(t *testing.T, slug, bundle string, rt process.Runtime) deploy.Params {
	t.Helper()
	return deploy.Params{
		Slug:              slug,
		BundleDir:         bundle,
		Replicas:          1,
		Manager:           process.NewManager(t.TempDir(), rt),
		Proxy:             proxy.New(),
		WorkerIsolation:   "grouped",
		WorkerGroupedSize: 6,
		WorkerMaxWorkers:  40,
		HealthCheck: func(string, time.Duration, http.RoundTripper) error {
			t.Error("elastic deploy must not boot fixed replicas")
			return nil
		},
	}
}

// TestRun_ElasticRunsPostDeployHooks is the core regression: a grouped app that
// declares a post-deploy hook must actually run it. Before the fix the elastic
// path returned before the hook phase, so the hook silently never ran while the
// deploy reported success and the app served requests without whatever the hook
// was supposed to produce.
func TestRun_ElasticRunsPostDeployHooks(t *testing.T) {
	bundle := writeElasticBundle(t, `
[[hook]]
on = "post-deploy"
command = ["python", "-m", "vendor_assets"]
`)

	var got atomic.Value
	restoreHook := deploy.SetHookRunnerForTest(func(_ context.Context, _ string, argv []string, _ []string, _ io.Writer) error {
		got.Store(argv[len(argv)-1])
		return nil
	})
	defer restoreHook()
	defer stubBundleBuild(t)()

	res, err := deploy.Run(elasticParams(t, "elastic-hook", bundle, process.NewNativeRuntime()))
	if err != nil {
		t.Fatalf("deploy.Run: %v", err)
	}
	if v, _ := got.Load().(string); v != "vendor_assets" {
		t.Fatalf("post-deploy hook did not run for an elastic app (got %q)", v)
	}
	if res.HooksSkipped != 0 {
		t.Errorf("HooksSkipped = %d, want 0 (the hook ran)", res.HooksSkipped)
	}
	if len(res.Replicas) != 0 {
		t.Errorf("elastic deploy must report no fixed replicas, got %d", len(res.Replicas))
	}
}

// TestRun_ElasticHookFailureFailsDeploy: an app whose build step fails must not
// be published. Serving an app whose generated assets are missing is exactly the
// silent-breakage the hook exists to prevent.
func TestRun_ElasticHookFailureFailsDeploy(t *testing.T) {
	bundle := writeElasticBundle(t, `
[[hook]]
on = "post-deploy"
command = ["broken"]
`)

	defer deploy.SetHookRunnerForTest(func(context.Context, string, []string, []string, io.Writer) error {
		return errors.New("asset build crashed")
	})()
	defer stubBundleBuild(t)()

	p := elasticParams(t, "elastic-broken-hook", bundle, process.NewNativeRuntime())
	_, err := deploy.Run(p)
	if err == nil {
		t.Fatal("expected a failing post-deploy hook to fail an elastic deploy")
	}
	if !strings.Contains(err.Error(), "asset build crashed") {
		t.Errorf("expected hook error to propagate, got %v", err)
	}
}

// TestRun_ElasticBuildsEnvironmentBeforeWorkers: elastic workers launch with
// `uv run --frozen --no-sync`, which deliberately performs no dependency work.
// If the deploy does not build the environment, nothing else ever will.
func TestRun_ElasticBuildsEnvironmentBeforeWorkers(t *testing.T) {
	bundle := writeElasticBundle(t, "")

	var synced atomic.Bool
	defer deploy.SetSyncHooksForTest(
		func(context.Context, string, []string) error { synced.Store(true); return nil },
		func(context.Context, string, []string) error { return nil },
	)()
	defer deploy.SetEnsureProjectForTest(func(context.Context, string) error { return nil })()

	if _, err := deploy.Run(elasticParams(t, "elastic-build", bundle, process.NewNativeRuntime())); err != nil {
		t.Fatalf("deploy.Run: %v", err)
	}
	if !synced.Load() {
		t.Fatal("elastic deploy did not build the app environment; first worker would launch against an unbuilt venv")
	}
}

// TestRun_ElasticBuildFailureFailsDeploy: a build failure must surface at deploy
// time rather than as an unexplained worker that never becomes healthy on the
// first user request.
func TestRun_ElasticBuildFailureFailsDeploy(t *testing.T) {
	bundle := writeElasticBundle(t, "")

	defer deploy.SetSyncHooksForTest(
		func(context.Context, string, []string) error { return errors.New("no matching distribution") },
		func(context.Context, string, []string) error { return nil },
	)()
	defer deploy.SetEnsureProjectForTest(func(context.Context, string) error { return nil })()

	_, err := deploy.Run(elasticParams(t, "elastic-build-fail", bundle, process.NewNativeRuntime()))
	if err == nil {
		t.Fatal("expected a failing dependency build to fail an elastic deploy")
	}
	if !strings.Contains(err.Error(), "no matching distribution") {
		t.Errorf("expected build error to propagate, got %v", err)
	}
}

// TestRun_ElasticSkippedHooksSurfacedUnderContainerRuntime: under a container
// runtime the host has no view of the app's environment, so hooks are skipped,
// but the count must reach the developer instead of vanishing, matching the
// fixed-replica path.
func TestRun_ElasticSkippedHooksSurfacedUnderContainerRuntime(t *testing.T) {
	bundle := writeElasticBundle(t, `
[[hook]]
on = "post-deploy"
command = ["should-not-run"]

[[hook]]
on = "post-deploy"
command = ["also-skipped"]
`)

	var called atomic.Bool
	defer deploy.SetHookRunnerForTest(func(context.Context, string, []string, []string, io.Writer) error {
		called.Store(true)
		return nil
	})()

	res, err := deploy.Run(elasticParams(t, "elastic-docker-hook", bundle, &fakeContainerRuntime{}))
	if err != nil {
		t.Fatalf("deploy.Run: %v", err)
	}
	if called.Load() {
		t.Error("post-deploy hook ran under a container runtime; should have been skipped")
	}
	if res.HooksSkipped != 2 {
		t.Errorf("HooksSkipped = %d, want 2", res.HooksSkipped)
	}
}

// TestRun_ElasticDeclaredCommandSkipsBuild: a bundle that declares its own
// launch command owns its environment, so the deploy must not try to build one.
// Hooks still run: they are the app's declared build step either way.
func TestRun_ElasticDeclaredCommandSkipsBuild(t *testing.T) {
	bundle := writeElasticBundle(t, `
[app]
command = ["./serve", "--port", "{port}"]

[[hook]]
on = "post-deploy"
command = ["make", "assets"]
`)

	var synced, hooked atomic.Bool
	defer deploy.SetSyncHooksForTest(
		func(context.Context, string, []string) error { synced.Store(true); return nil },
		func(context.Context, string, []string) error { synced.Store(true); return nil },
	)()
	defer deploy.SetHookRunnerForTest(func(context.Context, string, []string, []string, io.Writer) error {
		hooked.Store(true)
		return nil
	})()

	if _, err := deploy.Run(elasticParams(t, "elastic-declared-cmd", bundle, process.NewNativeRuntime())); err != nil {
		t.Fatalf("deploy.Run: %v", err)
	}
	if synced.Load() {
		t.Error("a bundle with a declared [app] command must not be built by the host")
	}
	if !hooked.Load() {
		t.Error("post-deploy hook must still run for a bundle with a declared command")
	}
}

// TestRun_ElasticUndeployableBundleFailsAtDeploy: a bundle with neither an
// entrypoint nor a declared command cannot launch. Before the fix the elastic
// path never type-detected, so such a deploy reported success and every future
// worker spawn failed at request time instead. Failing at deploy is what makes
// the failure attributable to the deploy that caused it.
func TestRun_ElasticUndeployableBundleFailsAtDeploy(t *testing.T) {
	empty := t.TempDir()
	defer stubBundleBuild(t)()

	_, err := deploy.Run(elasticParams(t, "elastic-empty", empty, process.NewNativeRuntime()))
	if err == nil {
		t.Fatal("expected an elastic deploy of a bundle with no entrypoint to fail")
	}
	if !strings.Contains(err.Error(), "no app.py or app.R found") {
		t.Errorf("expected a missing-entrypoint error, got %v", err)
	}
}

// TestRun_ElasticInvalidDeclaredCommandFailsAtDeploy: an unusable [app] command
// must be rejected once, by the deploy, rather than by each on-demand worker
// spawn long after the developer has moved on.
func TestRun_ElasticInvalidDeclaredCommandFailsAtDeploy(t *testing.T) {
	bundle := writeElasticBundle(t, `
[app]
command = ["./serve", "--port", "{bogus_placeholder}"]
`)
	defer stubBundleBuild(t)()

	_, err := deploy.Run(elasticParams(t, "elastic-bad-cmd", bundle, process.NewNativeRuntime()))
	if err == nil {
		t.Fatal("expected an invalid [app] command to fail an elastic deploy")
	}
	if !strings.Contains(err.Error(), "command") {
		t.Errorf("expected a command-validation error, got %v", err)
	}
}
