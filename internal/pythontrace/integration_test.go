package pythontrace

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/rvben/shinyhub/internal/config"
	"github.com/rvben/shinyhub/internal/tracing"
)

// Exercise the real SDK and library instrumentors; environment/argv assertions
// alone cannot prove that a child actually exports into its parent's trace.
func TestBootstrapExportsConnectedAWSAndASGISpans(t *testing.T) {
	if testing.Short() {
		t.Skip("uses uv to resolve real Python instrumentation")
	}
	uv, err := exec.LookPath("uv")
	if err != nil {
		t.Skip("uv unavailable")
	}
	python := os.Getenv("PYTHON_VERSION")
	if python == "" {
		python = "3.14+gil"
	}
	overlay := []string{"opentelemetry-distro", "opentelemetry-exporter-otlp", "opentelemetry-instrumentation-starlette", "opentelemetry-instrumentation-httpx", "opentelemetry-instrumentation-botocore", "boto3", "httpx", "starlette"}
	for _, events := range []string{"false", "true"} {
		t.Run("asgi_events_"+events, func(t *testing.T) {
			dir := t.TempDir()
			script := `import sys
import sibling
assert sys.argv[1:] == ["hello world"]
assert sibling.value == 42
import boto3
from botocore.stub import Stubber
client = boto3.client("s3", region_name="eu-west-1", aws_access_key_id="test", aws_secret_access_key="test")
with Stubber(client) as stub:
    stub.add_response("list_buckets", {"Buckets": []})
    client.list_buckets()
from starlette.applications import Starlette
from starlette.routing import Route
from starlette.responses import PlainTextResponse
from starlette.testclient import TestClient
async def hello(request):
    return PlainTextResponse("hello")
with TestClient(Starlette(routes=[Route("/", hello)])) as client:
    assert client.get("/").status_code == 200
`
			for name, body := range map[string]string{"job.py": script, "sibling.py": "value = 42\n"} {
				if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			prefix := []string{uv, "run", "--python", python, "--no-project"}
			for _, pkg := range overlay {
				prefix = append(prefix, "--with", pkg)
			}
			argv, ok := Wrap([]string{"python", "job.py", "hello world"}, overlay, prefix)
			if !ok {
				t.Fatal("job not wrapped")
			}
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
			defer cancel()
			cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
			cmd.Dir = dir
			defaults := tracing.WithDeployment(tracing.EnvFor(config.TracingConfig{
				Enabled: true, OTLPEndpoint: "http://unused:4318", OTLPProtocol: "http/protobuf", SampleRatio: 1,
			}, "test-app", 2), 17, "v1", "")
			defaults = append(defaults, "OTEL_RESOURCE_ATTRIBUTES=team=app,service.version=override")
			defaults, _ = tracing.MergeResourceAttributes(defaults, nil)
			cmd.Env = append(append(os.Environ(), defaults...), "OTEL_SERVICE_NAME=test-app", "OTEL_TRACES_EXPORTER=console", "OTEL_METRICS_EXPORTER=none", "OTEL_LOGS_EXPORTER=none",
				"OTEL_TRACES_SAMPLER=parentbased_traceidratio", "OTEL_TRACES_SAMPLER_ARG=1",
				"TRACEPARENT=00-11111111111111111111111111111111-2222222222222222-01", "TRACESTATE=vendor=test",
				"SHINYHUB_TRACING_ASGI_EVENTS="+events,
			)
			var out, stderr bytes.Buffer
			cmd.Stdout, cmd.Stderr = &out, &stderr
			if err := cmd.Run(); err != nil {
				t.Fatalf("real instrumented job failed: %v\n%s\n%s", err, stderr.String(), out.String())
			}
			dec := json.NewDecoder(&out)
			var root, aws map[string]any
			lowLevel := 0
			server := 0
			for {
				var span map[string]any
				err := dec.Decode(&span)
				if err == io.EOF {
					break
				}
				if err != nil {
					t.Fatalf("console spans: %v", err)
				}
				name, _ := span["name"].(string)
				if name == "process.run" {
					root = span
				}
				if strings.Contains(name, "S3.") {
					aws = span
				}
				if strings.Contains(name, "http send") || strings.Contains(name, "http receive") {
					lowLevel++
				}
				if name == "GET /" || name == "GET" {
					server++
				}
			}
			if root == nil || aws == nil || server == 0 {
				t.Fatalf("missing process/AWS/ASGI server spans: root=%v aws=%v server=%d; stderr=%s", root != nil, aws != nil, server, stderr.String())
			}
			resource := root["resource"].(map[string]any)["attributes"].(map[string]any)
			if resource["service.version"] != "override" || resource["shinyhub.replica"] != "2" || resource["shinyhub.deployment.id"] != "17" || resource["team"] != "app" {
				t.Fatalf("exported resource lost identity or user overrides: %v", resource)
			}
			rc := root["context"].(map[string]any)
			ac := aws["context"].(map[string]any)
			if rc["trace_id"] != "0x11111111111111111111111111111111" || ac["trace_id"] != rc["trace_id"] || root["parent_id"] != "0x2222222222222222" || aws["parent_id"] != rc["span_id"] {
				t.Fatal("AWS/process spans are disconnected from schedule context")
			}
			if events == "false" && lowLevel != 0 || events == "true" && lowLevel == 0 {
				t.Fatalf("ASGI opt-in=%s; send/receive spans=%d", events, lowLevel)
			}
		})
	}
}

func TestBootstrapPreservesExitCodeSamplingAndSingleExecution(t *testing.T) {
	if testing.Short() {
		t.Skip("uses the real Python SDK")
	}
	uv, err := exec.LookPath("uv")
	if err != nil {
		t.Skip("uv unavailable")
	}
	python := os.Getenv("PYTHON_VERSION")
	if python == "" {
		python = "3.14+gil"
	}
	overlay := []string{"opentelemetry-distro", "opentelemetry-exporter-otlp"}
	for _, sampled := range []string{"00", "01"} {
		t.Run("parent_sampled_"+sampled, func(t *testing.T) {
			dir := t.TempDir()
			code := `import pathlib
import __main__
answer = 42
assert __main__.answer == 42
path = pathlib.Path("executions")
path.write_text(path.read_text() + "run\n" if path.exists() else "run\n")
raise SystemExit(7)
`
			prefix := []string{uv, "run", "--python", python, "--no-project"}
			for _, pkg := range overlay {
				prefix = append(prefix, "--with", pkg)
			}
			argv, ok := Wrap([]string{"python", "-c", code}, overlay, prefix)
			if !ok {
				t.Fatal("inline Python not wrapped")
			}
			ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
			defer cancel()
			cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
			cmd.Dir = dir
			cmd.Env = append(os.Environ(), "OTEL_TRACES_EXPORTER=console", "OTEL_METRICS_EXPORTER=none", "OTEL_LOGS_EXPORTER=none",
				"OTEL_TRACES_SAMPLER=parentbased_traceidratio", "OTEL_TRACES_SAMPLER_ARG=1",
				"TRACEPARENT=00-11111111111111111111111111111111-2222222222222222-"+sampled,
			)
			var out, stderr bytes.Buffer
			cmd.Stdout, cmd.Stderr = &out, &stderr
			err := cmd.Run()
			exit, ok := err.(*exec.ExitError)
			if !ok || exit.ExitCode() != 7 {
				t.Fatalf("exit status changed: %v; %s", err, stderr.String())
			}
			executions, err := os.ReadFile(filepath.Join(dir, "executions"))
			if err != nil {
				t.Fatal(err)
			}
			if string(executions) != "run\n" {
				t.Fatalf("command repeated: %q", executions)
			}
			if sampled == "00" {
				if out.Len() != 0 {
					t.Fatal("unsampled parent exported spans")
				}
				return
			}
			var span map[string]any
			if err := json.Unmarshal(out.Bytes(), &span); err != nil {
				t.Fatalf("missing completed span on nonzero exit: %v", err)
			}
			if span["name"] != "process.run" || span["status"].(map[string]any)["status_code"] != "ERROR" {
				t.Fatal("failed process span missing error status")
			}
			if len(span["events"].([]any)) != 0 {
				t.Fatal("process span must not export exception text/stacks")
			}
		})
	}
}
