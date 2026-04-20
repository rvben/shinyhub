package process

import (
	"context"
	"fmt"
	"io"
	"strings"
	"syscall"
	"testing"
)

// captureRuntime is a fake Runtime that records the StartParams it received
// and immediately returns a no-op handle. Processes never actually run.
type captureRuntime struct {
	captured *StartParams
}

func (r *captureRuntime) Start(_ context.Context, p StartParams, _ io.Writer) (RunHandle, error) {
	// Mirror NativeRuntime: the full child env is filteredEnv() + p.Env, with
	// p.Env appended last so it wins on duplicate keys.
	full := append(filteredEnv(), p.Env...)
	p.Env = full
	r.captured = &p
	// Return a non-zero PID so the manager accepts the handle as valid.
	return RunHandle{PID: 1}, nil
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

// TestStart_AppliesResolverEnvAfterInherited verifies that the EnvResolver's
// output is appended after the inherited env, so resolver values win on
// last-wins key collision (e.g. for shells that process env in order).
func TestStart_AppliesResolverEnvAfterInherited(t *testing.T) {
	t.Setenv("INHERITED_VAR", "from-parent")
	t.Setenv("SHINYHUB_AUTH_SECRET", "must-not-leak")

	rt := &captureRuntime{}
	m := NewManager(t.TempDir(), rt)
	m.SetEnvResolver(func(slug string) ([]string, error) {
		return []string{"APP_VAR=from-app", "INHERITED_VAR=overridden"}, nil
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
	m.SetEnvResolver(func(slug string) ([]string, error) {
		return nil, fmt.Errorf("decrypt failed: bad key")
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
