package deploy

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rvben/shinyhub/internal/deployevent"
	"github.com/rvben/shinyhub/internal/deployfail"
)

// recordingHandler captures slog message strings for progress-log assertions.
type recordingHandler struct {
	mu   *sync.Mutex
	msgs *[]string
}

func (h recordingHandler) Enabled(context.Context, slog.Level) bool { return true }
func (h recordingHandler) Handle(_ context.Context, r slog.Record) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	*h.msgs = append(*h.msgs, r.Message)
	return nil
}
func (h recordingHandler) WithAttrs([]slog.Attr) slog.Handler { return h }
func (h recordingHandler) WithGroup(string) slog.Handler      { return h }

// A build that exceeds its budget fails with a single uv sync: prefix and is
// classified build_failed - never readiness_timeout/crashed.
//
// Note: the injected hook returns ctx.Err() directly, so this asserts the
// classification + single-prefix at the buildEnvironment layer; the production
// "build exceeded the build timeout" wording is verified at the process layer by
// TestSync_BuildTimeout (Task 2).
func TestBuildEnvironment_TimesOutAsBuildFailed(t *testing.T) {
	restore := SetSyncHooksForTest(
		func(ctx context.Context, _ string, _ []string) error { <-ctx.Done(); return ctx.Err() },
		func(ctx context.Context, _ string, _ []string) error { <-ctx.Done(); return ctx.Err() },
	)
	defer restore()
	restoreEnsure := SetEnsureProjectForTest(func(context.Context, string) error { return nil })
	defer restoreEnsure()

	err := buildEnvironment(Params{Slug: "x", BundleDir: t.TempDir()}, "python", 30*time.Millisecond)
	if err == nil {
		t.Fatal("a build that blocks past the budget must fail")
	}
	if strings.Count(err.Error(), "uv sync:") != 1 {
		t.Fatalf("want exactly one 'uv sync:' prefix (no double-wrap), got %q", err.Error())
	}
	if got := deployfail.Classify(err); got != deployfail.BuildFailed {
		t.Fatalf("build-timeout must classify build_failed, got %q (%q)", got, err.Error())
	}
}

// Preparation errors must terminate the build before sync or replica startup.
func TestBuildEnvironment_ProjectPreparationFailsInDependencies(t *testing.T) {
	for _, cause := range []error{context.DeadlineExceeded, errors.New("uv add requirements: invalid version specifier")} {
		t.Run(cause.Error(), func(t *testing.T) {
			restore := SetSyncHooksForTest(func(context.Context, string, []string) error {
				t.Fatal("sync ran after failed preparation")
				return nil
			}, nil)
			defer restore()
			restoreEnsure := SetEnsureProjectForTest(func(context.Context, string) error { return cause })
			defer restoreEnsure()
			var events []deployevent.Event
			err := buildEnvironment(Params{Slug: "x", BundleDir: t.TempDir(), Progress: func(e deployevent.Event) { events = append(events, e) }}, "python", time.Second)
			if !errors.Is(err, cause) || deployfail.Classify(err) != deployfail.BuildFailed {
				t.Fatalf("error=%v", err)
			}
			failed := false
			for _, e := range events {
				if e.Phase == "dependencies" && e.Status == deployevent.StatusFailed {
					failed = true
				}
				if e.Status == deployevent.StatusCompleted {
					t.Fatalf("false success event: %+v", e)
				}
			}
			if !failed {
				t.Fatal("missing failed dependency event")
			}
		})
	}
}

func TestResolveBuildTimeout_DefaultAndOverride(t *testing.T) {
	if got := resolveBuildTimeout(nil); got != defaultBuildTimeout {
		t.Fatalf("nil manifest -> %v, want default %v", got, defaultBuildTimeout)
	}
	v := 300
	m := &Manifest{App: AppSettings{BuildTimeoutSeconds: &v}}
	if got := resolveBuildTimeout(m); got != 300*time.Second {
		t.Fatalf("override -> %v, want 300s", got)
	}
}

// buildEnvironment emits start, heartbeat, and completion logs so a long build
// is visibly alive in the server log. buildProgressInterval is a var so the test
// shrinks it to force at least one heartbeat without flakiness.
func TestBuildEnvironment_LogsProgress(t *testing.T) {
	var mu sync.Mutex
	var msgs []string
	prev := slog.Default()
	slog.SetDefault(slog.New(recordingHandler{mu: &mu, msgs: &msgs}))
	defer slog.SetDefault(prev)

	prevInterval := buildProgressInterval
	buildProgressInterval = time.Millisecond
	defer func() { buildProgressInterval = prevInterval }()

	restore := SetSyncHooksForTest(
		func(context.Context, string, []string) error { time.Sleep(10 * time.Millisecond); return nil },
		func(context.Context, string, []string) error { return nil },
	)
	defer restore()
	restoreEnsure := SetEnsureProjectForTest(func(context.Context, string) error { return nil })
	defer restoreEnsure()

	if err := buildEnvironment(Params{Slug: "x", BundleDir: t.TempDir()}, "python", time.Second); err != nil {
		t.Fatalf("build: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	joined := strings.Join(msgs, "\n")
	for _, want := range []string{"deploy: building environment", "deploy: still building environment", "deploy: environment built"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("missing log %q in:\n%s", want, joined)
		}
	}
}
