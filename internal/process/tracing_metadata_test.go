package process_test

import (
	"strings"
	"testing"

	"github.com/rvben/shinyhub/internal/config"
	"github.com/rvben/shinyhub/internal/process"
	"github.com/rvben/shinyhub/internal/tracing"
)

func TestStartTracingUsesLaunchedDeploymentAcrossGenerations(t *testing.T) {
	rt := newFakeRuntime()
	m := process.NewManager(t.TempDir(), rt)
	t.Cleanup(func() { _ = m.StopAll() })
	cfg := config.TracingConfig{Enabled: true, OTLPEndpoint: "http://collector:4318"}
	m.SetPlatformDefaultEnvResolver(func(slug string, replica int) []string { return tracing.EnvFor(cfg, slug, replica) })
	for _, tc := range []struct {
		id      int64
		version string
	}{{17, "v1"}, {18, "v2"}, {16, "rollback"}} {
		p := process.StartParams{Slug: "app", Index: 2, Dir: t.TempDir(), Command: []string{"fake"}, DeploymentID: tc.id, AppVersion: tc.version, GenerationScoped: true}
		if _, err := m.Start(p); err != nil {
			t.Fatal(err)
		}
		attrs := lastValue(rt.lastEnv, "OTEL_RESOURCE_ATTRIBUTES")
		if !strings.Contains(attrs, "service.version="+tc.version) || !strings.Contains(attrs, "shinyhub.replica=2") {
			t.Fatalf("wrong launch identity: %s", attrs)
		}
		if strings.Contains(attrs, "service.instance.id") {
			t.Fatal("instance IDs must remain process-specific")
		}
	}
}
