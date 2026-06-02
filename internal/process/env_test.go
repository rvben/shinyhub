package process

import (
	"context"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"syscall"
	"testing"
)

// captureRuntime is a fake Runtime that records the StartParams it received
// and immediately returns a no-op handle. Processes never actually run.
type captureRuntime struct {
	captured *StartParams
}

func (r *captureRuntime) Start(_ context.Context, p StartParams, _ io.Writer) (ReplicaEndpoint, error) {
	// Mirror NativeRuntime: the full child env is filteredEnv() + p.Env, with
	// p.Env appended last so it wins on duplicate keys.
	full := append(filteredEnv(), p.Env...)
	p.Env = full
	r.captured = &p
	// Return a non-zero PID so the manager accepts the handle as valid.
	return ReplicaEndpoint{
		URL:      fmt.Sprintf("http://127.0.0.1:%d", p.Port),
		Provider: "native",
		WorkerID: "1",
		Handle:   RunHandle{PID: 1},
	}, nil
}

func (r *captureRuntime) Signal(_ RunHandle, _ syscall.Signal) error { return nil }

func (r *captureRuntime) Wait(_ context.Context, _ RunHandle) error {
	// Block until the test is done; we never unblock intentionally, so
	// the manager's exit-monitoring goroutine just leaks — acceptable in tests.
	select {}
}

func (r *captureRuntime) Stats(_ context.Context, _ RunHandle) (float64, uint64, error) {
	return 0, 0, nil
}

func (r *captureRuntime) RunOnce(_ context.Context, _ StartParams, _ io.Writer) (ExitInfo, error) {
	return ExitInfo{}, nil
}

func (r *captureRuntime) HostPreparesDeps() bool    { return true }
func (r *captureRuntime) AppBindHost() string       { return "127.0.0.1" }
func (r *captureRuntime) HostProvidesAppData() bool { return true }

func TestFilteredEnvStripsShinyHubVars(t *testing.T) {
	t.Setenv("SHINYHUB_AUTH_SECRET", "super-secret")
	t.Setenv("SHINYHUB_GITHUB_CLIENT_SECRET", "github-secret")
	t.Setenv("SHINYHUB_OIDC_CLIENT_SECRET", "oidc-secret")
	t.Setenv("PATH", "/usr/bin:/bin") // should be preserved

	env := filteredEnv()

	for _, e := range env {
		if strings.HasPrefix(e, "SHINYHUB_") {
			t.Errorf("SHINYHUB_ var leaked into child env: %s", e)
		}
	}

	found := false
	for _, e := range env {
		if strings.HasPrefix(e, "PATH=") {
			found = true
		}
	}
	if !found {
		t.Error("PATH was unexpectedly stripped from child env")
	}
}

func TestFilteredEnvPreservesNonShinyHubVars(t *testing.T) {
	t.Setenv("SHINYHUB_AUTH_SECRET", "should-be-stripped")
	t.Setenv("MY_APP_SECRET", "should-be-kept")

	env := filteredEnv()

	keptFound := false
	for _, e := range env {
		if e == "MY_APP_SECRET=should-be-kept" {
			keptFound = true
		}
		if strings.HasPrefix(e, "SHINYHUB_") {
			t.Errorf("SHINYHUB_ var present in filtered env: %s", e)
		}
	}
	if !keptFound {
		t.Error("expected MY_APP_SECRET to be preserved in filtered env")
	}
}

// TestDependencySetupCmdsScrubServerSecrets is the P0 regression for the
// dependency-install path: uv and renv run deployer-controlled code (build
// backends, renv profiles) and must never inherit SHINYHUB_* server secrets,
// while still keeping PATH so the tools resolve.
func TestDependencySetupCmdsScrubServerSecrets(t *testing.T) {
	t.Setenv("SHINYHUB_AUTH_SECRET", "must-not-leak")
	t.Setenv("SHINYHUB_DEPLOY_TOKEN", "must-not-leak")
	t.Setenv("PATH", "/usr/bin:/bin")

	cmds := map[string]*exec.Cmd{
		"uv sync":           uvSyncCmd(t.TempDir()),
		"uv python install": uvPythonInstallCmd("3.12"),
		"renv::restore":     renvRestoreCmd(t.TempDir()),
	}
	for name, cmd := range cmds {
		if cmd.Env == nil {
			t.Errorf("%s: cmd.Env is nil — inherits full server env including secrets", name)
			continue
		}
		var hasPath bool
		for _, e := range cmd.Env {
			if strings.HasPrefix(e, "SHINYHUB_") {
				t.Errorf("%s: SHINYHUB_ var leaked: %s", name, e)
			}
			if strings.HasPrefix(e, "PATH=") {
				hasPath = true
			}
		}
		if !hasPath {
			t.Errorf("%s: PATH missing from scrubbed env", name)
		}
	}
}

// TestNativeChildEnvIncludesSecretEnv verifies the native runtime concatenates
// SecretEnv into the child process environment after Env, so secret env vars
// still reach the app exactly as plaintext env (no behavior change vs a single
// flat Env slice).
func TestNativeChildEnvIncludesSecretEnv(t *testing.T) {
	p := StartParams{
		Env:       []string{"PLAIN=1"},
		SecretEnv: []string{"SECRET=shh"},
	}
	env := nativeChildEnv(p)

	var plainOK, secretOK bool
	for _, e := range env {
		switch e {
		case "PLAIN=1":
			plainOK = true
		case "SECRET=shh":
			secretOK = true
		}
	}
	if !plainOK {
		t.Errorf("PLAIN=1 missing from native child env: %v", env)
	}
	if !secretOK {
		t.Errorf("SECRET=shh (from SecretEnv) missing from native child env: %v", env)
	}
}

// TestDockerChildEnvIncludesSecretEnv verifies the Docker runtime concatenates
// SecretEnv into the container environment after Env, matching the native
// runtime: a Docker container shares the host trust boundary the same way, so
// secret env is injected as plaintext with no behavior change.
func TestDockerChildEnvIncludesSecretEnv(t *testing.T) {
	t.Setenv("SHINYHUB_AUTH_SECRET", "must-not-leak")
	p := StartParams{
		Env:       []string{"PLAIN=1"},
		SecretEnv: []string{"SECRET=shh"},
	}
	env := dockerChildEnv(p)

	var plainOK, secretOK bool
	for _, e := range env {
		switch e {
		case "PLAIN=1":
			plainOK = true
		case "SECRET=shh":
			secretOK = true
		}
		if strings.HasPrefix(e, "SHINYHUB_") {
			t.Errorf("SHINYHUB_ server secret leaked into container env: %s", e)
		}
	}
	if !plainOK {
		t.Errorf("PLAIN=1 missing from docker child env: %v", env)
	}
	if !secretOK {
		t.Errorf("SECRET=shh (from SecretEnv) missing from docker child env: %v", env)
	}
}

// TestStart_PopulatesSecretEnvFromResolver verifies the Manager routes the
// resolver's secret env into StartParams.SecretEnv (kept separate from Env) so
// downstream runtimes can deliver secrets out of band.
func TestStart_PopulatesSecretEnvFromResolver(t *testing.T) {
	rt := &captureRuntime{}
	m := NewManager(t.TempDir(), rt)
	m.SetEnvResolver(func(slug string) ([]string, []string, error) {
		return []string{"PLAIN=1"}, []string{"SECRET=shh"}, nil
	})

	_, err := m.Start(StartParams{
		Slug:    "test-secret-env",
		Dir:     t.TempDir(),
		Command: []string{"true"},
		Port:    19902,
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if rt.captured == nil {
		t.Fatal("runtime.Start was never called")
	}

	var secretFound bool
	for _, e := range rt.captured.SecretEnv {
		if e == "SECRET=shh" {
			secretFound = true
		}
	}
	if !secretFound {
		t.Errorf("SECRET=shh not found in captured SecretEnv: %v", rt.captured.SecretEnv)
	}
	// The secret must NOT leak into the plaintext Env slice.
	for _, e := range rt.captured.Env {
		if e == "SECRET=shh" {
			t.Errorf("secret value leaked into plaintext Env: %v", rt.captured.Env)
		}
	}
}

// TestStart_DeploySuppliedEnvWinsOverSecretEnv guards the precedence contract
// for an authoritative deploy/platform-supplied key (e.g. the allocated PORT).
// Such keys arrive in StartParams.Env and must win over per-app env. Because the
// runtimes inject SecretEnv after Env, a per-app SECRET keyed the same as a
// deploy-supplied var would otherwise shadow it; the Manager must drop the
// colliding secret so the deploy value still wins (matching the prior behavior
// where the decrypted secret lived in the user env that deploy env beat).
func TestStart_DeploySuppliedEnvWinsOverSecretEnv(t *testing.T) {
	rt := &captureRuntime{}
	m := NewManager(t.TempDir(), rt)
	m.SetEnvResolver(func(slug string) ([]string, []string, error) {
		// A per-app secret that collides with the allocated PORT.
		return nil, []string{"PORT=66666"}, nil
	})

	_, err := m.Start(StartParams{
		Slug:    "test-port-collision",
		Dir:     t.TempDir(),
		Command: []string{"true"},
		Port:    19903,
		Env:     []string{"PORT=19903"}, // deploy-supplied, authoritative
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if rt.captured == nil {
		t.Fatal("runtime.Start was never called")
	}

	// The colliding secret must be dropped from SecretEnv so it can't shadow
	// the deploy-supplied PORT in the child env.
	for _, e := range rt.captured.SecretEnv {
		if strings.HasPrefix(e, "PORT=") {
			t.Errorf("secret PORT must be dropped (collides with deploy env), got SecretEnv=%v", rt.captured.SecretEnv)
		}
	}
	// The deploy-supplied PORT must still be present in the plaintext env. The
	// effective child env is filteredEnv()+Env+SecretEnv; with the secret PORT
	// dropped, the only PORT entry is the deploy value.
	var portVal string
	var portCount int
	for _, e := range append(append([]string{}, rt.captured.Env...), rt.captured.SecretEnv...) {
		if strings.HasPrefix(e, "PORT=") {
			portVal = strings.TrimPrefix(e, "PORT=")
			portCount++
		}
	}
	if portCount != 1 || portVal != "19903" {
		t.Errorf("PORT in child env = %q (count %d), want exactly one PORT=19903", portVal, portCount)
	}
}

// TestStart_AppliesResolverEnvAfterInherited verifies that the EnvResolver's
// output is appended after the inherited env, so resolver values win on
// last-wins key collision (e.g. for shells that process env in order).
func TestStart_AppliesResolverEnvAfterInherited(t *testing.T) {
	t.Setenv("INHERITED_VAR", "from-parent")
	t.Setenv("SHINYHUB_AUTH_SECRET", "must-not-leak")

	rt := &captureRuntime{}
	m := NewManager(t.TempDir(), rt)
	m.SetEnvResolver(func(slug string) ([]string, []string, error) {
		return []string{"APP_VAR=from-app", "INHERITED_VAR=overridden"}, nil, nil
	})

	_, err := m.Start(StartParams{
		Slug:    "test-resolver",
		Dir:     t.TempDir(),
		Command: []string{"true"},
		Port:    19900,
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if rt.captured == nil {
		t.Fatal("runtime.Start was never called")
	}

	env := rt.captured.Env

	// APP_VAR must be present.
	appVarFound := false
	for _, e := range env {
		if e == "APP_VAR=from-app" {
			appVarFound = true
		}
	}
	if !appVarFound {
		t.Error("APP_VAR=from-app not found in captured env")
	}

	// INHERITED_VAR=overridden must appear after INHERITED_VAR=from-parent.
	firstIdx, lastIdx := -1, -1
	for i, e := range env {
		if e == "INHERITED_VAR=from-parent" {
			firstIdx = i
		}
		if e == "INHERITED_VAR=overridden" {
			lastIdx = i
		}
	}
	if lastIdx == -1 {
		t.Error("INHERITED_VAR=overridden not found in captured env")
	}
	if firstIdx == -1 {
		t.Error("INHERITED_VAR=from-parent not found in captured env")
	}
	if firstIdx != -1 && lastIdx != -1 && lastIdx <= firstIdx {
		t.Errorf("resolver value (idx %d) must appear after inherited value (idx %d) to win on last-wins semantics", lastIdx, firstIdx)
	}

	// No SHINYHUB_* secrets must leak.
	for _, e := range env {
		if strings.HasPrefix(e, "SHINYHUB_") {
			t.Errorf("SHINYHUB_ var leaked into captured env: %s", e)
		}
	}
}

// TestStart_ResolverErrorAborts verifies that a resolver returning an error
// causes Start to fail without launching any process.
func TestStart_ResolverErrorAborts(t *testing.T) {
	rt := &captureRuntime{}
	m := NewManager(t.TempDir(), rt)
	m.SetEnvResolver(func(slug string) ([]string, []string, error) {
		return nil, nil, fmt.Errorf("decrypt failed: bad key")
	})

	_, err := m.Start(StartParams{
		Slug:    "test-resolver-error",
		Dir:     t.TempDir(),
		Command: []string{"true"},
		Port:    19901,
	})
	if err == nil {
		t.Fatal("expected Start to return an error when resolver fails, got nil")
	}
	if rt.captured != nil {
		t.Error("runtime.Start must not be called when resolver fails")
	}
}
