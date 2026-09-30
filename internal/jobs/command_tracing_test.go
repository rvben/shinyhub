package jobs_test

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/rvben/shinyhub/internal/pythontrace"
)

func TestScheduledPythonCommandsHonorInstrumentationPolicy(t *testing.T) {
	for _, tc := range []struct {
		name          string
		enabled, auto bool
		manifest      string
		command       string
		want          bool
	}{
		{"fleet enabled", true, true, "", `["uv","run","python","helpers/fetch.py"]`, true},
		{"manifest opt-out", true, true, "[tracing]\nauto=false\n", `["uv","run","python","helpers/fetch.py"]`, false},
		{"manifest opt-in", true, false, "[tracing]\nauto=true\n", `["python","helpers/fetch.py"]`, true},
		{"tracing disabled", false, true, "[tracing]\nauto=true\n", `["python","helpers/fetch.py"]`, false},
		{"shell command", true, true, "", `["sh","-c","python helpers/fetch.py"]`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newJobFixture(t, "skip", &fakeRuntime{})
			dir := t.TempDir()
			f.st.deployments[0].BundleDir = dir
			f.sched.CommandJSON = tc.command
			if tc.manifest != "" {
				if err := os.WriteFile(filepath.Join(dir, "shinyhub.toml"), []byte(tc.manifest), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			cfg := enabledTracing
			cfg.Enabled = tc.enabled
			cfg.AutoInstrumentApps = tc.auto
			cfg.AutoInstrumentExtraPackages = []string{"opentelemetry-instrumentation-botocore"}
			tr, rec := newJobsTracer(t)
			f.m.SetTracing(cfg, tr)
			runAndWait(t, f)
			params := lastParams(f.rt)
			if got := slices.Contains(params.Command, pythontrace.Bootstrap); got != tc.want {
				t.Fatalf("instrumented=%v want %v", got, tc.want)
			}
			if tc.want && !slices.Contains(params.Command, "opentelemetry-instrumentation-botocore") {
				t.Fatal("extra instrumentor missing")
			}
			if tc.enabled {
				attrs := lastEnv(params.Env, "OTEL_RESOURCE_ATTRIBUTES")
				if !strings.Contains(attrs, "shinyhub.deployment.id=1") || !strings.Contains(attrs, "service.version=v1") {
					t.Fatalf("job identity: %s", attrs)
				}
				waitEnded(t, rec, 1)
			}
			if f.rt.calls != 1 {
				t.Fatalf("job executed %d times", f.rt.calls)
			}
		})
	}
}
