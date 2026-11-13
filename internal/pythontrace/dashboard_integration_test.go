package pythontrace

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/rvben/shinyhub/internal/config"
	"github.com/rvben/shinyhub/internal/tracing"
)

// Exercise Shiny's actual input/output protocol through the production overlay.
// The fixture asserts completed reactive/output/HTTP spans while the same
// WebSocket stays connected, catching accidental session-length parenting.
func TestBootstrapTracesDashboardInteractions(t *testing.T) {
	if testing.Short() {
		t.Skip("uses real Shiny and Python instrumentation")
	}
	uv, err := exec.LookPath("uv")
	if err != nil {
		t.Skip("uv unavailable")
	}
	python := os.Getenv("PYTHON_VERSION")
	if python == "" {
		python = "3.14+gil"
	}
	fixture, err := filepath.Abs("testdata/dashboard_interactions.py")
	if err != nil {
		t.Fatal(err)
	}
	args := []string{"run", "--python", python, "--no-project"}
	for _, pkg := range []string{"shiny>=1.8,<2", "httpx", "opentelemetry-distro", "opentelemetry-exporter-otlp", "opentelemetry-instrumentation-starlette", "opentelemetry-instrumentation-httpx"} {
		args = append(args, "--with", pkg)
	}
	prefix := slices.Clone(ModulePrefix())
	prefix[len(prefix)-1] = "app-script"
	args = append(args, prefix...)
	args = append(args, fixture)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, uv, args...)
	cmd.Env = append(os.Environ(), tracing.EnvFor(config.TracingConfig{Enabled: true, OTLPEndpoint: "http://unused:4318", OTLPProtocol: "http/protobuf", SampleRatio: 1}, "dashboard-test", 0)...)
	cmd.Env = append(cmd.Env, "OTEL_TRACES_EXPORTER=console", "OTEL_LOGS_EXPORTER=none", "OTEL_METRICS_EXPORTER=none", "SHINY_OTEL_COLLECT=reactivity")
	var out, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("dashboard tracing: %v\n%s\n%s", err, stderr.String(), out.String())
	}
	dec := json.NewDecoder(&out)
	loads := 0
	updates := 0
	for {
		var span struct {
			Name string `json:"name"`
		}
		if err := dec.Decode(&span); err == io.EOF {
			break
		} else if err != nil {
			t.Fatalf("console spans: %v", err)
		}
		if span.Name == "dashboard.load_data" {
			loads++
		}
		if span.Name == "reactive_update" {
			updates++
		}
	}
	if loads != 3 || updates < 3 {
		t.Fatalf("missing exported dashboard work: loads=%d updates=%d", loads, updates)
	}
}
