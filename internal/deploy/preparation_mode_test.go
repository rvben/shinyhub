package deploy_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rvben/shinyhub/internal/deploy"
	"github.com/rvben/shinyhub/internal/process"
	"github.com/rvben/shinyhub/internal/proxy"
)

// Promotion and activation are different operations. Promoting a new bundle must
// prepare it and must fail when preparation fails, or the app serves without its
// declared build steps. Activating a bundle that already served must not re-run
// app-controlled hooks, and must not be able to fail: the restore path is an
// unattended safety net whose whole job is getting the app back up.

// prepBundle writes a python bundle declaring a post-deploy hook.
func prepBundle(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "app.py"), []byte(""), 0644); err != nil {
		t.Fatal(err)
	}
	manifest := "[[hook]]\non = \"post-deploy\"\ncommand = [\"make\", \"assets\"]\n"
	if err := os.WriteFile(filepath.Join(dir, deploy.ManifestFilename), []byte(manifest), 0644); err != nil {
		t.Fatal(err)
	}
	return dir
}

// prepProbes stubs the build and hook runners, reporting whether each ran.
// syncErr is returned by the dependency build so a test can force it to fail.
func prepProbes(t *testing.T, syncErr error) (built, hooked *atomic.Bool) {
	t.Helper()
	built, hooked = &atomic.Bool{}, &atomic.Bool{}
	restoreSync := deploy.SetSyncHooksForTest(
		func(context.Context, string, []string) error { built.Store(true); return syncErr },
		func(context.Context, string, []string) error { built.Store(true); return syncErr },
	)
	restoreProject := deploy.SetEnsureProjectForTest(func(context.Context, string) error { return nil })
	restoreHook := deploy.SetHookRunnerForTest(func(context.Context, string, []string, []string, io.Writer) error {
		hooked.Store(true)
		return nil
	})
	t.Cleanup(func() { restoreHook(); restoreProject(); restoreSync() })
	return built, hooked
}

func prepParams(t *testing.T, slug, bundle string, mode deploy.PreparationMode, isolation string) deploy.Params {
	t.Helper()
	return deploy.Params{
		Slug:            slug,
		BundleDir:       bundle,
		Replicas:        1,
		Manager:         process.NewManager(t.TempDir(), process.NewNativeRuntime()),
		Proxy:           proxy.New(),
		WorkerIsolation: isolation,
		Preparation:     mode,
		Command:         []string{"sleep", "30"},
		HealthCheck:     func(string, time.Duration, http.RoundTripper) error { return nil },
	}
}

// TestPrepareSkip_RunsNeitherBuildNorHooks: a deployment recorded as prepared
// has a built environment and already-executed hooks, so activation repeats
// neither.
func TestPrepareSkip_RunsNeitherBuildNorHooks(t *testing.T) {
	for _, isolation := range []string{"grouped", "multiplex"} {
		t.Run(isolation, func(t *testing.T) {
			bundle := prepBundle(t)
			built, hooked := prepProbes(t, nil)
			p := prepParams(t, "skip-"+isolation, bundle, deploy.PrepareSkip, isolation)
			if isolation == "grouped" {
				p.Command = nil // exercise the inferred path, where the build lives
			}
			if _, err := deploy.Run(p); err != nil {
				t.Fatalf("deploy.Run: %v", err)
			}
			if built.Load() {
				t.Error("activation of a prepared deployment must not rebuild")
			}
			if hooked.Load() {
				t.Error("activation must not re-run app-controlled post-deploy hooks")
			}
		})
	}
}

// TestPrepareBestEffort_BuildsButNeverFails: a deployment whose preparation
// state predates the record may never have been built (an elastic bundle from
// before elastic apps were prepared at all), so the build is attempted. A
// failure must not fail the activation - that would let a transient error take
// down an app the safety net was trying to rescue.
func TestPrepareBestEffort_BuildsButNeverFails(t *testing.T) {
	bundle := prepBundle(t)
	built, hooked := prepProbes(t, errors.New("transient index timeout"))

	p := prepParams(t, "besteffort", bundle, deploy.PrepareBestEffort, "grouped")
	p.Command = nil // inferred path so the build is reached

	if _, err := deploy.Run(p); err != nil {
		t.Fatalf("a failed best-effort build must not fail the activation, got: %v", err)
	}
	if !built.Load() {
		t.Error("best-effort activation must attempt the build")
	}
	if hooked.Load() {
		t.Error("activation must not run post-deploy hooks even when rebuilding")
	}
}

// TestPrepareRequired_StillFailsOnBuildAndHook guards the promotion contract
// against the new modes: a real deploy must still refuse to serve a bundle whose
// declared build steps failed. Without this, the activation modes could quietly
// become the default behaviour.
func TestPrepareRequired_StillFailsOnBuildAndHook(t *testing.T) {
	t.Run("build failure is fatal", func(t *testing.T) {
		bundle := prepBundle(t)
		prepProbes(t, errors.New("no matching distribution"))
		p := prepParams(t, "req-build", bundle, deploy.PrepareRequired, "grouped")
		p.Command = nil
		if _, err := deploy.Run(p); err == nil {
			t.Fatal("a promotion must fail when the dependency build fails")
		}
	})

	t.Run("hooks run and a failure is fatal", func(t *testing.T) {
		bundle := prepBundle(t)
		defer deploy.SetSyncHooksForTest(
			func(context.Context, string, []string) error { return nil },
			func(context.Context, string, []string) error { return nil },
		)()
		defer deploy.SetEnsureProjectForTest(func(context.Context, string) error { return nil })()
		defer deploy.SetHookRunnerForTest(func(context.Context, string, []string, []string, io.Writer) error {
			return errors.New("asset build crashed")
		})()
		p := prepParams(t, "req-hook", bundle, deploy.PrepareRequired, "grouped")
		p.Command = nil
		if _, err := deploy.Run(p); err == nil {
			t.Fatal("a promotion must fail when a post-deploy hook fails")
		}
	})
}

// TestPreparationZeroValueIsPromotion: every existing caller constructs Params
// without naming this field, so the zero value has to be the strict mode. If it
// ever became an activation mode, every deploy would silently stop running its
// declared build steps - the exact bug this release fixed.
func TestPreparationZeroValueIsPromotion(t *testing.T) {
	var p deploy.Params
	if p.Preparation != deploy.PrepareRequired {
		t.Fatalf("zero-value Preparation = %v, want PrepareRequired", p.Preparation)
	}
}

// prepProjectBundle writes a python bundle in PROJECT mode (a pyproject.toml),
// which is the shape whose launch is `uv run --frozen --no-sync` and therefore
// depends on a pre-built .venv existing on disk.
func prepProjectBundle(t *testing.T, withVenv bool) string {
	t.Helper()
	dir := t.TempDir()
	for name, body := range map[string]string{
		"app.py":         "",
		"pyproject.toml": "[project]\nname = \"x\"\nversion = \"0\"\n",
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0644); err != nil {
			t.Fatal(err)
		}
	}
	if withVenv {
		if err := os.MkdirAll(filepath.Join(dir, ".venv"), 0755); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// TestPrepareSkip_RebuildsWhenEnvironmentIsMissing: `prepared` records that
// preparation once succeeded, not that its output still exists. A wiped .venv, a
// tmpfs-backed apps dir across a reboot, or a deployment prepared under a
// container runtime and later moved to the native runtime all leave `prepared`
// true with nothing on disk. Launch is `uv run --frozen --no-sync`, which does no
// repair, so skipping would boot against nothing.
func TestPrepareSkip_RebuildsWhenEnvironmentIsMissing(t *testing.T) {
	bundle := prepProjectBundle(t, false) // project mode, no .venv
	built, hooked := prepProbes(t, nil)

	p := prepParams(t, "skip-missing-env", bundle, deploy.PrepareSkip, "multiplex")
	p.Command = nil // inferred path
	if _, err := deploy.Run(p); err != nil {
		t.Fatalf("deploy.Run: %v", err)
	}
	if !built.Load() {
		t.Error("a skipped preparation must still rebuild when the environment is absent")
	}
	if hooked.Load() {
		t.Error("rebuilding a missing environment must not also re-run post-deploy hooks")
	}
}

// TestPrepareSkip_SkipsWhenEnvironmentIsPresent is the other half: the recovery
// path must not defeat the optimization. With the .venv present, nothing is
// rebuilt.
func TestPrepareSkip_SkipsWhenEnvironmentIsPresent(t *testing.T) {
	bundle := prepProjectBundle(t, true) // project mode, .venv exists
	built, _ := prepProbes(t, nil)

	p := prepParams(t, "skip-present-env", bundle, deploy.PrepareSkip, "multiplex")
	p.Command = nil
	if _, err := deploy.Run(p); err != nil {
		t.Fatalf("deploy.Run: %v", err)
	}
	if built.Load() {
		t.Error("a present environment must not be rebuilt; the skip is the whole point")
	}
}

// TestPrepareSkip_MissingEnvironmentRebuildFailureIsNotFatal: the recovery build
// must not become a new way for the unattended restore path to fail. A failure
// is logged and the boot proceeds, where the health check is the real gate.
func TestPrepareSkip_MissingEnvironmentRebuildFailureIsNotFatal(t *testing.T) {
	bundle := prepProjectBundle(t, false)
	prepProbes(t, errors.New("index unreachable"))

	p := prepParams(t, "skip-missing-env-fail", bundle, deploy.PrepareSkip, "multiplex")
	p.Command = nil
	if _, err := deploy.Run(p); err != nil {
		t.Fatalf("a failed recovery build must not fail the activation, got: %v", err)
	}
}

// TestHostEnvironmentPresent_RequirementsBundleNeedsNothing: a requirements.txt
// bundle builds its dependencies at launch (`uv run --with-requirements`), so it
// has no pre-built environment to miss and must not trigger a pointless rebuild.
func TestHostEnvironmentPresent_RequirementsBundleNeedsNothing(t *testing.T) {
	dir := t.TempDir()
	for name, body := range map[string]string{
		"app.py":           "",
		"requirements.txt": "shiny>=1.0\n",
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0644); err != nil {
			t.Fatal(err)
		}
	}
	built, _ := prepProbes(t, nil)

	p := prepParams(t, "skip-requirements", dir, deploy.PrepareSkip, "multiplex")
	p.Command = nil
	if _, err := deploy.Run(p); err != nil {
		t.Fatalf("deploy.Run: %v", err)
	}
	if built.Load() {
		t.Error("a launch-time-resolved bundle has no environment to miss; it must not be rebuilt")
	}
}
