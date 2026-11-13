package servertrace

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/rvben/shinyhub/internal/config"
	"go.opentelemetry.io/otel"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	coltracepb "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	"google.golang.org/protobuf/proto"
)

type healthTestExporter struct {
	export func(context.Context, []sdktrace.ReadOnlySpan) error
}

func (e healthTestExporter) ExportSpans(ctx context.Context, spans []sdktrace.ReadOnlySpan) error {
	return e.export(ctx, spans)
}
func (healthTestExporter) Shutdown(context.Context) error { return nil }

// Exercise the real SDK queue: the exporter is blocked, one span is queued,
// and a further span is dropped. Once unblocked, a failed batch is followed by
// a successful batch, so both data loss and recovery are observable.
func TestHealthDetectsQueueDropsExportFailureAndRecovery(t *testing.T) {
	t.Setenv("OTEL_GO_X_OBSERVABILITY", "")
	t.Setenv("OTEL_GO_X_SELF_OBSERVABILITY", "")
	previousMP, previousEH, previousLogger := otel.GetMeterProvider(), otel.GetErrorHandler(), slog.Default()
	t.Cleanup(func() {
		otel.SetMeterProvider(previousMP)
		otel.SetErrorHandler(previousEH)
		slog.SetDefault(previousLogger)
	})
	var logs bytes.Buffer
	slog.SetDefault(slog.New(slog.NewJSONHandler(&logs, nil)))
	reg := prometheus.NewRegistry()
	mp, err := newHealthMetrics(reg)
	if err != nil {
		t.Fatal(err)
	}
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	calls := 0
	health, err := newExportHealth(healthTestExporter{export: func(ctx context.Context, _ []sdktrace.ReadOnlySpan) error {
		calls++
		if calls == 1 {
			close(entered)
			select {
			case <-release:
			case <-ctx.Done():
				return ctx.Err()
			}
			return errors.New("failed https://user:secret@example.test/traces?token=secret")
		}
		return nil
	}}, reg)
	if err != nil {
		t.Fatal(err)
	}
	otel.SetErrorHandler(health)
	restoreGate, err := enableSDKProcessorMetrics()
	if err != nil {
		t.Fatal(err)
	}
	tp := sdktrace.NewTracerProvider(sdktrace.WithBatcher(health,
		sdktrace.WithMaxQueueSize(1), sdktrace.WithMaxExportBatchSize(1), sdktrace.WithBatchTimeout(time.Hour)))
	restoreGate()
	t.Cleanup(func() {
		unblock()
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = tp.Shutdown(ctx)
		_ = mp.Shutdown(ctx)
	})
	tr := tp.Tracer("health-test")
	end := func() { _, s := tr.Start(context.Background(), "work"); s.End() }
	end()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("exporter did not start")
	}
	end() // queued
	end() // dropped, never blocks the caller
	families, err := reg.Gather()
	if err != nil {
		t.Fatal(err)
	}
	foundQueue, foundDrop := false, false
	for _, f := range families {
		for _, m := range f.Metric {
			if f.GetName() == "otel_sdk_processor_span_queue_size" && m.GetGauge().GetValue() == 1 {
				foundQueue = true
			}
			for _, label := range m.Label {
				if label.GetName() == "error_type" && label.GetValue() == "queue_full" && m.GetCounter().GetValue() == 1 {
					foundDrop = true
				}
			}
		}
	}
	if !foundQueue || !foundDrop {
		t.Fatalf("missing queue pressure or drop: queue=%v drop=%v", foundQueue, foundDrop)
	}
	unblock()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := tp.ForceFlush(ctx); err != nil {
		t.Fatal(err)
	}
	for _, result := range []string{"failure", "success"} {
		if got := testutil.ToFloat64(health.spans.WithLabelValues(result)); got != 1 {
			t.Fatalf("%s spans=%v", result, got)
		}
		if got := testutil.ToFloat64(health.batches.WithLabelValues(result)); got != 1 {
			t.Fatalf("%s batches=%v", result, got)
		}
	}
	if testutil.ToFloat64(health.lastSuccess) <= 0 {
		t.Fatal("missing last successful export timestamp")
	}
	if !strings.Contains(logs.String(), "trace_export_failed") || !strings.Contains(logs.String(), "trace_export_recovered") || strings.Contains(logs.String(), "secret") || strings.Contains(logs.String(), "example.test") {
		t.Fatalf("unsafe or missing health log: %s", logs.String())
	}
}

func TestExportWarningsAreBoundedWithoutMetrics(t *testing.T) {
	previous := slog.Default()
	t.Cleanup(func() { slog.SetDefault(previous) })
	var logs bytes.Buffer
	slog.SetDefault(slog.New(slog.NewJSONHandler(&logs, nil)))
	h, err := newExportHealth(healthTestExporter{export: func(context.Context, []sdktrace.ReadOnlySpan) error { return context.DeadlineExceeded }}, nil)
	if err != nil {
		t.Fatal(err)
	}
	for range 3 {
		err := h.ExportSpans(context.Background(), []sdktrace.ReadOnlySpan{nil})
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatal("lost error classification")
		}
		h.Handle(err) // SDK reporting must not duplicate the already-logged error.
	}
	if strings.Count(logs.String(), "trace_export_failed") != 1 || !strings.Contains(logs.String(), `"error_type":"timeout"`) {
		t.Fatalf("unbounded or misclassified warnings: %s", logs.String())
	}
}

func TestHealthRespectsSDKMetricsFeatureGates(t *testing.T) {
	for _, tc := range []struct{ key, value string }{
		{"OTEL_GO_X_OBSERVABILITY", "false"}, {"OTEL_GO_X_SELF_OBSERVABILITY", "false"}, {"OTEL_GO_X_SELF_OBSERVABILITY", "true"},
	} {
		t.Run(tc.key+"="+tc.value, func(t *testing.T) {
			t.Setenv("OTEL_GO_X_OBSERVABILITY", "")
			t.Setenv("OTEL_GO_X_SELF_OBSERVABILITY", "")
			t.Setenv(tc.key, tc.value)
			previous := otel.GetMeterProvider()
			t.Cleanup(func() { otel.SetMeterProvider(previous) })
			reg := prometheus.NewRegistry()
			mp, err := newHealthMetrics(reg)
			if err != nil {
				t.Fatal(err)
			}
			restoreGate, err := enableSDKProcessorMetrics()
			if err != nil {
				t.Fatal(err)
			}
			tp := sdktrace.NewTracerProvider(sdktrace.WithBatcher(healthTestExporter{export: func(context.Context, []sdktrace.ReadOnlySpan) error { return nil }}))
			restoreGate()
			t.Cleanup(func() { _ = tp.Shutdown(context.Background()); _ = mp.Shutdown(context.Background()) })
			families, err := reg.Gather()
			if err != nil {
				t.Fatal(err)
			}
			found := false
			for _, f := range families {
				found = found || strings.Contains(f.GetName(), "otel_sdk_processor")
			}
			if found != (tc.value == "true") {
				t.Fatalf("SDK gate value %s, processor metrics present=%v", tc.value, found)
			}
		})
	}
}

func TestSetupExposesExportHealthOnPrivateRegistry(t *testing.T) {
	t.Setenv("OTEL_GO_X_OBSERVABILITY", "")
	t.Setenv("OTEL_GO_X_SELF_OBSERVABILITY", "")
	previousMP, previousEH, previousTP, previousProp := otel.GetMeterProvider(), otel.GetErrorHandler(), otel.GetTracerProvider(), otel.GetTextMapPropagator()
	t.Cleanup(func() {
		otel.SetMeterProvider(previousMP)
		otel.SetErrorHandler(previousEH)
		otel.SetTracerProvider(previousTP)
		otel.SetTextMapPropagator(previousProp)
	})
	collector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" || r.URL.Path != "/v1/traces" {
			t.Errorf("unexpected export request: %s %s", r.Method, r.URL.Path)
		}
		_, _ = io.Copy(io.Discard, r.Body)
		w.WriteHeader(http.StatusOK)
	}))
	defer collector.Close()
	reg := prometheus.NewRegistry()
	tr, err := Setup(context.Background(), config.TracingConfig{Enabled: true, OTLPEndpoint: collector.URL, OTLPProtocol: "http/protobuf", SampleRatio: 1}, "test", "test-instance", WithHealthMetrics(reg))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = tr.Shutdown(context.Background()) })
	if os.Getenv("OTEL_GO_X_OBSERVABILITY") != "" {
		t.Fatal("Setup leaked the SDK feature gate")
	}
	unrelated, err := otel.Meter("unrelated-component").Int64Counter("private.unrelated.metric")
	if err != nil {
		t.Fatal(err)
	}
	unrelated.Add(context.Background(), 1)
	families, err := reg.Gather()
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range families {
		if !strings.HasPrefix(f.GetName(), "shinyhub_trace_export_") && !strings.HasPrefix(f.GetName(), "otel_sdk_processor_span_") {
			t.Fatalf("non-processor metric leaked to the health registry: %s", f.GetName())
		}
	}
	_, span := tr.Tracer().Start(context.Background(), "work")
	span.End()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := tr.tp.ForceFlush(ctx); err != nil {
		t.Fatal(err)
	}
	if err := testutil.GatherAndCompare(reg, strings.NewReader(`
# HELP shinyhub_trace_export_spans_total Server spans in completed OTLP export calls by result; success does not prove backend retention.
# TYPE shinyhub_trace_export_spans_total counter
shinyhub_trace_export_spans_total{result="failure"} 0
shinyhub_trace_export_spans_total{result="success"} 1
`), "shinyhub_trace_export_spans_total"); err != nil {
		t.Fatal(err)
	}
}

func TestSDKAndExportWarningsHaveIndependentLimits(t *testing.T) {
	previous := slog.Default()
	t.Cleanup(func() { slog.SetDefault(previous) })
	var logs bytes.Buffer
	slog.SetDefault(slog.New(slog.NewJSONHandler(&logs, nil)))
	fail := true
	h, err := newExportHealth(healthTestExporter{export: func(context.Context, []sdktrace.ReadOnlySpan) error {
		if fail {
			return errors.New("private endpoint")
		}
		return nil
	}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	for range 3 {
		h.Handle(errors.New("private SDK error"))
	}
	for range 3 {
		_ = h.ExportSpans(context.Background(), []sdktrace.ReadOnlySpan{nil})
	}
	fail = false
	_ = h.ExportSpans(context.Background(), []sdktrace.ReadOnlySpan{nil})
	h.Handle(errors.New("private SDK error")) // recovery must not reset SDK limiter
	if strings.Count(logs.String(), "trace_sdk_error") != 1 || strings.Count(logs.String(), "trace_export_failed") != 1 || strings.Count(logs.String(), "trace_export_recovered") != 1 || !strings.Contains(logs.String(), `"error_type":"sdk"`) || strings.Contains(logs.String(), "private") {
		t.Fatalf("warning isolation or privacy: %s", logs.String())
	}
}

func TestScopedSDKGateDoesNotLeakToChildren(t *testing.T) {
	for _, set := range []bool{false, true} {
		t.Run(fmt.Sprint(set), func(t *testing.T) {
			t.Setenv("OTEL_GO_X_OBSERVABILITY", "")
			t.Setenv("OTEL_GO_X_SELF_OBSERVABILITY", "")
			if !set {
				_ = os.Unsetenv("OTEL_GO_X_OBSERVABILITY")
			}
			restore, err := enableSDKProcessorMetrics()
			if err != nil {
				t.Fatal(err)
			}
			if os.Getenv("OTEL_GO_X_OBSERVABILITY") != "true" {
				t.Fatal("gate not enabled during construction")
			}
			restore()
			if value, exists := os.LookupEnv("OTEL_GO_X_OBSERVABILITY"); value != "" || exists != set {
				t.Fatalf("gate leaked: value=%q set=%v", value, exists)
			}
		})
	}
}

// Verify the installed SDK reports rejected spans as an export error, even
// when the collector returns HTTP 200. We do not infer acceptance from HTTP status.
func TestPartialCollectorAcceptanceCountsAsExportFailure(t *testing.T) {
	previousEH := otel.GetErrorHandler()
	t.Cleanup(func() { otel.SetErrorHandler(previousEH) })
	response, err := proto.Marshal(&coltracepb.ExportTraceServiceResponse{PartialSuccess: &coltracepb.ExportTracePartialSuccess{RejectedSpans: 1, ErrorMessage: "private rejection detail"}})
	if err != nil {
		t.Fatal(err)
	}
	collector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		w.Header().Set("Content-Type", "application/x-protobuf")
		_, _ = w.Write(response)
	}))
	defer collector.Close()
	reg := prometheus.NewRegistry()
	// Exercise the actual exporter with health wrapping, without altering the
	// SDK metric provider (partial acceptance is an export protocol concern).
	exp, err := newExporter(context.Background(), config.TracingConfig{OTLPEndpoint: collector.URL, OTLPProtocol: "http/protobuf"})
	if err != nil {
		t.Fatal(err)
	}
	h, err := newExportHealth(exp, reg)
	if err != nil {
		t.Fatal(err)
	}
	otel.SetErrorHandler(h)
	tp := sdktrace.NewTracerProvider(sdktrace.WithBatcher(h, sdktrace.WithBatchTimeout(time.Hour)))
	t.Cleanup(func() { _ = tp.Shutdown(context.Background()) })
	_, span := tp.Tracer("test").Start(context.Background(), "work")
	span.End()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	err = tp.ForceFlush(ctx)
	var sanitized safeExportError
	if !errors.As(err, &sanitized) || strings.Contains(err.Error(), "private") || !strings.Contains(sanitized.cause.Error(), "private rejection detail") {
		t.Fatalf("expected sanitized partial-acceptance error, got %v", err)
	}
	if got := testutil.ToFloat64(h.batches.WithLabelValues("failure")); got != 1 {
		t.Fatalf("partial acceptance must count as failed export: %v", got)
	}
	if got := testutil.ToFloat64(h.spans.WithLabelValues("failure")); got != 1 {
		t.Fatalf("missing partially accepted batch spans: %v", got)
	}
	if testutil.ToFloat64(h.lastSuccess) != 0 {
		t.Fatal("partial acceptance must not advance last full-success timestamp")
	}
}
