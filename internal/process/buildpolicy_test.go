package process

import (
	"os"
	"strings"
	"testing"
)

// effectiveEnv returns the value os/exec would use for key: the last occurrence
// wins, matching how a child process resolves duplicate KEY=value entries.
func effectiveEnv(env []string, key string) (string, bool) {
	val, ok := "", false
	for _, e := range env {
		if name, v, cut := strings.Cut(e, "="); cut && name == key {
			val, ok = v, true
		}
	}
	return val, ok
}

// TestWithBuildInterpreterPolicy_AuthoritativeOverPerApp models the deploy build
// layering (SanitizedEnv base + per-app appEnv, then the policy appended last).
// The host `build:` policy must win over an app-set UV_PYTHON_* - the fix for a
// server configured only-system still allowing an app to force managed.
func TestWithBuildInterpreterPolicy_AuthoritativeOverPerApp(t *testing.T) {
	SetBuildInterpreterPolicy([]string{"UV_PYTHON_PREFERENCE=only-system"})
	t.Cleanup(func() { SetBuildInterpreterPolicy(nil) })

	// Base carries a service-env value; the per-app layer overrides it to managed;
	// the policy is applied last, exactly as runSandboxedBuildStep does.
	base := []string{"UV_PYTHON_PREFERENCE=managed", "PATH=/usr/bin"}
	appEnv := []string{"UV_PYTHON_PREFERENCE=managed"}
	env := WithBuildInterpreterPolicy(append(append([]string{}, base...), appEnv...))

	if got, _ := effectiveEnv(env, "UV_PYTHON_PREFERENCE"); got != "only-system" {
		t.Errorf("host policy must win over per-app and service-env: UV_PYTHON_PREFERENCE = %q, want only-system", got)
	}
}

// TestWithBuildInterpreterPolicy_NoOpWhenUnset proves an unconfigured policy
// leaves the env exactly as-is, so a service-env or per-app value still applies.
func TestWithBuildInterpreterPolicy_NoOpWhenUnset(t *testing.T) {
	SetBuildInterpreterPolicy(nil)
	env := []string{"UV_PYTHON=3.11", "PATH=/usr/bin"}
	got := WithBuildInterpreterPolicy(env)
	if len(got) != len(env) {
		t.Fatalf("unconfigured policy must not add entries: got %d, want %d", len(got), len(env))
	}
	if v, _ := effectiveEnv(got, "UV_PYTHON"); v != "3.11" {
		t.Errorf("unconfigured policy must not touch UV_PYTHON: got %q, want 3.11", v)
	}
}

// TestSetBuildInterpreterPolicy_DoesNotMutateOSEnv is the regression guard for
// the zero-downtime re-exec bug: the policy must live only in process memory. If
// it were os.Setenv'd, tableflip would hand the successor os.Environ() carrying
// the value, and an emptied `build:` key could never unset it. Asserting the OS
// env stays clean pins that the value is never written there.
func TestSetBuildInterpreterPolicy_DoesNotMutateOSEnv(t *testing.T) {
	if _, ok := os.LookupEnv("UV_PYTHON"); ok {
		t.Skip("UV_PYTHON already set in the test environment")
	}
	SetBuildInterpreterPolicy([]string{"UV_PYTHON=3.12", "UV_PYTHON_PREFERENCE=only-system"})
	t.Cleanup(func() { SetBuildInterpreterPolicy(nil) })

	if v, ok := os.LookupEnv("UV_PYTHON"); ok {
		t.Errorf("policy must not be exported to the OS environment: os UV_PYTHON = %q", v)
	}
}

// TestNativeChildEnv_AppliesBuildPolicyOverPerApp exercises the real serve-time
// launch env builder: a per-app UV_PYTHON_PREFERENCE must be overridden by the
// host policy so serve-time `uv run` obeys the server config.
func TestNativeChildEnv_AppliesBuildPolicyOverPerApp(t *testing.T) {
	SetBuildInterpreterPolicy([]string{"UV_PYTHON_PREFERENCE=only-system"})
	t.Cleanup(func() { SetBuildInterpreterPolicy(nil) })

	env := nativeChildEnv(StartParams{Env: []string{"UV_PYTHON_PREFERENCE=managed"}})
	if got, _ := effectiveEnv(env, "UV_PYTHON_PREFERENCE"); got != "only-system" {
		t.Errorf("nativeChildEnv must let host policy win over per-app: UV_PYTHON_PREFERENCE = %q, want only-system", got)
	}
}
