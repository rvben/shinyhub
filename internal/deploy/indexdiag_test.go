package deploy

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"
)

// Package-index diagnostics must never leak credentials: URL userinfo and
// credential-variable values are masked, list-valued vars are redacted
// per-token, and non-index vars are excluded entirely.
func TestCollectIndexEnv_Redaction(t *testing.T) {
	env := []string{
		"UV_EXTRA_INDEX_URL=https://user:pass@nexus.example.com/repository/pypi/simple",
		"UV_INDEX=https://a:b@one.example.com/simple https://two.example.com/simple",
		"UV_INDEX_CORP_PASSWORD=super-secret",
		"UV_INDEX_CORP_USERNAME=svc-account",
		"RENV_CONFIG_REPOS_OVERRIDE=https://cran.example.com",
		"PATH=/usr/bin",
		"SHINYHUB_AUTH_SECRET=nope",
	}
	got := strings.Join(collectIndexEnv(env), " ")
	for _, want := range []string{
		"UV_EXTRA_INDEX_URL=https://***@nexus.example.com/repository/pypi/simple",
		"UV_INDEX=https://***@one.example.com/simple https://two.example.com/simple",
		"UV_INDEX_CORP_PASSWORD=***",
		"UV_INDEX_CORP_USERNAME=***",
		"RENV_CONFIG_REPOS_OVERRIDE=https://cran.example.com",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in %q", want, got)
		}
	}
	for _, leak := range []string{"pass", "super-secret", "svc-account", "PATH", "SHINYHUB"} {
		if strings.Contains(got, leak) {
			t.Errorf("leaked %q in %q", leak, got)
		}
	}
}

// A uv "not found in the package registry" failure is annotated so an
// operator learns whether any private-index configuration reached the build
// at all - the difference between "package name typo" and "the index var was
// never passed through" (the v0.9.6 regression class took a two-release
// bisect to diagnose without this).
func TestIndexResolutionHint(t *testing.T) {
	notFound := []byte("x  No solution found: because hda-common was not found in the package registry and ...")

	if err := indexResolutionHint(notFound, nil, nil); err != nil {
		t.Errorf("nil error must pass through, got %v", err)
	}
	base := fmt.Errorf("exit status 1")
	if err := indexResolutionHint([]byte("some unrelated failure"), base, nil); err != base {
		t.Errorf("unrelated output must pass the error through unchanged, got %v", err)
	}

	err := indexResolutionHint(notFound, base, []string{"PATH=/usr/bin"})
	if err == nil || !strings.Contains(err.Error(), "no package-index configuration reached this build") ||
		!strings.Contains(err.Error(), "docs/environment.md") {
		t.Errorf("no-index hint missing or wrong: %v", err)
	}

	err = indexResolutionHint(notFound, base, []string{
		"UV_EXTRA_INDEX_URL=https://user:pass@nexus.example.com/simple",
	})
	if err == nil || !strings.Contains(err.Error(), "UV_EXTRA_INDEX_URL=https://***@nexus.example.com/simple") {
		t.Errorf("with-index hint missing redacted config: %v", err)
	}
	if err != nil && strings.Contains(err.Error(), "user:pass") {
		t.Errorf("hint leaked credentials: %v", err)
	}
}

// The hint is wired into the production build step, not just available as a
// helper: a failing build whose output carries the registry-miss signature
// returns an annotated error.
func TestRunSandboxedBuildStep_AnnotatesRegistryMiss(t *testing.T) {
	argv := []string{"sh", "-c", `echo "because hda-common was not found in the package registry"; exit 1`}
	_, err := runSandboxedBuildStep(context.Background(), t.TempDir(), argv, nil)
	if err == nil || !strings.Contains(err.Error(), "no package-index configuration reached this build") {
		t.Errorf("registry-miss failure not annotated: %v", err)
	}
}

// buildEnvironment states the effective package-index configuration up front
// (redacted) when any is present, and stays silent when none is - so the
// build log answers "which indexes did this build see" without exposing
// credentials.
func TestBuildEnvironment_LogsIndexConfiguration(t *testing.T) {
	var mu sync.Mutex
	var msgs []string
	prev := slog.Default()
	slog.SetDefault(slog.New(recordingHandler{mu: &mu, msgs: &msgs}))
	defer slog.SetDefault(prev)

	restore := SetSyncHooksForTest(
		func(context.Context, string, []string) error { return nil },
		func(context.Context, string, []string) error { return nil },
	)
	defer restore()
	defer SetEnsureProjectForTest(func(context.Context, string) error { return nil })()

	p := Params{Slug: "demo", BundleDir: t.TempDir(),
		Manager: managerWithEnv(t, []string{"UV_EXTRA_INDEX_URL=https://nexus.example.com/simple"}, nil, nil)}
	if err := buildEnvironment(p, "python", time.Second); err != nil {
		t.Fatalf("buildEnvironment: %v", err)
	}
	mu.Lock()
	joined := strings.Join(msgs, "\n")
	mu.Unlock()
	if !strings.Contains(joined, "deploy: package index configuration") {
		t.Errorf("missing index-configuration log in:\n%s", joined)
	}

	mu.Lock()
	msgs = nil
	mu.Unlock()
	if err := buildEnvironment(Params{Slug: "plain", BundleDir: t.TempDir()}, "python", time.Second); err != nil {
		t.Fatalf("buildEnvironment: %v", err)
	}
	mu.Lock()
	joined = strings.Join(msgs, "\n")
	mu.Unlock()
	if strings.Contains(joined, "deploy: package index configuration") {
		t.Errorf("index log must be silent with no index config:\n%s", joined)
	}
}
