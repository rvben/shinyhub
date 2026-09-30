package deploy

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/rvben/shinyhub/internal/config"
	"github.com/rvben/shinyhub/internal/pythontrace"
	"github.com/rvben/shinyhub/internal/tracing"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

func TestHookTracingSharesPhaseContextAndNeverRetries(t *testing.T) {
	dir := t.TempDir()
	writeManifest(t, dir, "[[hook]]\non='post-deploy'\ncommand=['uv','run','python','refresh.py']\n")
	rec := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(rec))
	t.Cleanup(func() { _ = tp.Shutdown(context.Background()) })
	tr := tp.Tracer("test")
	ctx, parent := tr.Start(context.Background(), "deploy")
	defer parent.End()
	cfg := config.TracingConfig{Enabled: true, OTLPEndpoint: "http://collector:4318"}
	m := managerWithEnv(t, []string{"TRACEPARENT=foreign", "OTEL_EXPORTER_OTLP_ENDPOINT=http://override:4318"}, nil, nil)
	m.SetPlatformDefaultEnvResolver(func(slug string, index int) []string { return tracing.EnvFor(cfg, slug, index) })
	m.SetAutoInstrumentAppsDefault(true)
	m.SetAutoInstrumentExtraPackages([]string{"opentelemetry-instrumentation-botocore"})
	calls := 0
	old := hookRunner
	t.Cleanup(func() { hookRunner = old })
	hookRunner = func(_ context.Context, _ string, argv, env []string, _ io.Writer) error {
		calls++
		if !slices.Contains(argv, pythontrace.Bootstrap) || !slices.Contains(argv, "opentelemetry-instrumentation-botocore") {
			t.Fatal("hook lacks shared bootstrap/extra packages")
		}
		values := map[string]string{}
		for _, kv := range env {
			k, v, _ := strings.Cut(kv, "=")
			values[k] = v
		}
		started := rec.Started()
		phase := started[len(started)-1]
		sc := phase.SpanContext()
		if phase.Name() != "deploy.post_deploy_hooks" || values["TRACEPARENT"] != fmt.Sprintf("00-%s-%s-01", sc.TraceID(), sc.SpanID()) {
			t.Fatal("hook disconnected from phase span")
		}
		if values["OTEL_EXPORTER_OTLP_ENDPOINT"] != "http://override:4318" {
			t.Fatal("app exporter override lost")
		}
		attrs := values["OTEL_RESOURCE_ATTRIBUTES"]
		if !strings.Contains(attrs, "shinyhub.deployment.id=17") || !strings.Contains(attrs, "service.version=v1") || strings.Contains(attrs, "shinyhub.replica") {
			t.Fatalf("hook identity: %s", attrs)
		}
		return errors.New("failure after app code could have run")
	}
	_, _, _, err := runManifestPostDeployHooks(Params{Slug: "app", BundleDir: dir, Manager: m, Tracer: tr, TraceCtx: ctx, DeploymentID: 17, AppVersion: "v1"}, true)
	if err == nil || calls != 1 {
		t.Fatalf("hook must fail without retry: calls=%d err=%v", calls, err)
	}
	log, err := os.ReadFile(filepath.Join(dir, "deploy-hooks.log"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(log), pythontrace.Bootstrap) {
		t.Fatal("bootstrap implementation leaked into hook logs")
	}
}
