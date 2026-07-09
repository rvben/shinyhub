package deploy

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/rvben/shinyhub/internal/process"
)

// managerWithEnv returns a Manager whose env resolver serves the given
// non-secret and secret slices, mirroring the production resolver wired in
// cmd/shinyhub (store lookup + appenv.Resolve).
func managerWithEnv(t *testing.T, env, secretEnv []string, resolveErr error) *process.Manager {
	t.Helper()
	m := process.NewManager(t.TempDir(), nil)
	m.SetEnvResolver(func(slug string) ([]string, []string, error) {
		return env, secretEnv, resolveErr
	})
	return m
}

// The host-side dependency build executes deployer-controlled code that must
// see the same per-app env vars (including decrypted secrets, e.g. private
// package-index credentials) the app process receives at start. buildEnvironment
// resolves them through the Params' Manager and threads them into the sync step.
func TestBuildEnvironment_ResolvesAppEnvIntoSync(t *testing.T) {
	var captured []string
	restore := SetSyncHooksForTest(
		func(ctx context.Context, dir string, appEnv []string) error {
			captured = appEnv
			return nil
		},
		func(ctx context.Context, dir string, appEnv []string) error { return nil },
	)
	defer restore()
	defer SetEnsureProjectForTest(func(context.Context, string) error { return nil })()

	p := Params{
		Slug:      "demo",
		BundleDir: t.TempDir(),
		Manager:   managerWithEnv(t, []string{"UV_EXTRA_INDEX_URL=https://nexus.example.com/simple"}, []string{"UV_INDEX_CORP_PASSWORD=shh"}, nil),
	}
	if err := buildEnvironment(p, "python", time.Second); err != nil {
		t.Fatalf("buildEnvironment: %v", err)
	}
	want := []string{"UV_EXTRA_INDEX_URL=https://nexus.example.com/simple", "UV_INDEX_CORP_PASSWORD=shh"}
	if len(captured) != len(want) || captured[0] != want[0] || captured[1] != want[1] {
		t.Errorf("sync received appEnv %v, want %v", captured, want)
	}
}

// A deploy without a Manager (local/test paths) builds with no per-app env,
// and a resolver failure fails the build closed rather than silently building
// without the app's variables (e.g. missing index credentials would otherwise
// surface as a misleading "package not found").
func TestBuildEnvironment_NoManagerAndResolverError(t *testing.T) {
	var captured []string
	calls := 0
	restore := SetSyncHooksForTest(
		func(ctx context.Context, dir string, appEnv []string) error {
			captured = appEnv
			calls++
			return nil
		},
		func(ctx context.Context, dir string, appEnv []string) error { return nil },
	)
	defer restore()
	defer SetEnsureProjectForTest(func(context.Context, string) error { return nil })()

	if err := buildEnvironment(Params{Slug: "x", BundleDir: t.TempDir()}, "python", time.Second); err != nil {
		t.Fatalf("nil-Manager build: %v", err)
	}
	if calls != 1 || captured != nil {
		t.Errorf("nil-Manager build: calls=%d appEnv=%v, want 1 call with nil env", calls, captured)
	}

	p := Params{Slug: "x", BundleDir: t.TempDir(), Manager: managerWithEnv(t, nil, nil, fmt.Errorf("decrypt failed"))}
	if err := buildEnvironment(p, "python", time.Second); err == nil {
		t.Error("resolver error must fail the build closed")
	}
}

// Post-deploy hooks are app-controlled code that must see the same per-app
// env the app process receives (the RunPostDeployHooks contract). The manifest
// hook path resolves the app's stored env through the Params' Manager and
// merges it with any caller-supplied Env (localrun --env), failing closed on a
// resolver error.
func TestManifestHooks_ReceiveAppEnv(t *testing.T) {
	bundle := t.TempDir()
	manifest := "[[hook]]\non = \"post-deploy\"\ncommand = [\"true\"]\n"
	if err := os.WriteFile(filepath.Join(bundle, ManifestFilename), []byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}

	var captured []string
	origRunner := hookRunner
	hookRunner = func(ctx context.Context, dir string, argv []string, env []string, w io.Writer) error {
		captured = env
		return nil
	}
	defer func() { hookRunner = origRunner }()

	p := Params{
		Slug:      "demo",
		BundleDir: bundle,
		Env:       []string{"FROM_RUN=1"},
		Manager:   managerWithEnv(t, []string{"PLAIN=1"}, []string{"SECRET=shh"}, nil),
	}
	skipped, err := runManifestPostDeployHooks(p, true)
	if err != nil || skipped != 0 {
		t.Fatalf("runManifestPostDeployHooks: skipped=%d err=%v", skipped, err)
	}
	want := []string{"FROM_RUN=1", "PLAIN=1", "SECRET=shh"}
	if len(captured) != len(want) || captured[0] != want[0] || captured[1] != want[1] || captured[2] != want[2] {
		t.Errorf("hook received env %v, want %v", captured, want)
	}

	p.Manager = managerWithEnv(t, nil, nil, fmt.Errorf("decrypt failed"))
	if _, err := runManifestPostDeployHooks(p, true); err == nil {
		t.Error("resolver error must fail the hook phase closed")
	}
}

// runSandboxedBuildStep layers env as: sanitized server base, then per-app env,
// then the sandbox's own redirects last. The child process must see the app's
// vars and secrets, must not see server secrets, and an app-env override of an
// inherited server var must win (os/exec last-occurrence-wins).
func TestRunSandboxedBuildStep_AppEnvLayering(t *testing.T) {
	t.Setenv("SHINYHUB_AUTH_SECRET", "server-secret")
	t.Setenv("TZ", "UTC")

	appEnv := []string{"APP_INDEX_CRED=shh", "TZ=Europe/Amsterdam"}
	out, err := runSandboxedBuildStep(context.Background(), t.TempDir(), []string{"sh", "-c", "env"}, appEnv)
	if err != nil {
		t.Fatalf("runSandboxedBuildStep: %v\n%s", err, out)
	}
	got := string(out)
	if !strings.Contains(got, "APP_INDEX_CRED=shh") {
		t.Errorf("child env missing per-app var:\n%s", got)
	}
	if strings.Contains(got, "SHINYHUB_AUTH_SECRET") {
		t.Errorf("server secret leaked into build env:\n%s", got)
	}
	if !strings.Contains(got, "TZ=Europe/Amsterdam") || strings.Contains(got, "TZ=UTC") {
		t.Errorf("per-app override must win over inherited server var:\n%s", got)
	}
}
