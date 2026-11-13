package servertrace

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"go.opentelemetry.io/otel"
	otelprom "go.opentelemetry.io/otel/exporters/prometheus"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
)

// SetupOption configures server tracing at startup.
type SetupOption func(*setupOptions)
type setupOptions struct{ registerer prometheus.Registerer }

// WithHealthMetrics exposes server SDK queue/drop metrics and export results
// through the server's private Prometheus registry. Call only during startup.
func WithHealthMetrics(r prometheus.Registerer) SetupOption {
	return func(o *setupOptions) { o.registerer = r }
}

func newHealthMetrics(r prometheus.Registerer) (*sdkmetric.MeterProvider, error) {
	exp, err := otelprom.New(otelprom.WithRegisterer(r), otelprom.WithoutTargetInfo())
	if err != nil {
		return nil, err
	}
	mp := sdkmetric.NewMeterProvider(sdkmetric.WithReader(exp), sdkmetric.WithView(func(i sdkmetric.Instrument) (sdkmetric.Stream, bool) {
		if strings.HasPrefix(i.Name, "otel.sdk.processor.span.") {
			return sdkmetric.Stream{}, false
		}
		return sdkmetric.Stream{Aggregation: sdkmetric.AggregationDrop{}}, true
	}))
	otel.SetMeterProvider(mp)
	return mp, nil
}

type exportHealth struct {
	sdktrace.SpanExporter
	spans             *prometheus.CounterVec
	batches           *prometheus.CounterVec
	lastSuccess       prometheus.Gauge
	mu                sync.Mutex
	lastExportWarning time.Time
	lastSDKWarning    time.Time
	failing           bool
}

func newExportHealth(exp sdktrace.SpanExporter, r prometheus.Registerer) (*exportHealth, error) {
	h := &exportHealth{SpanExporter: exp}
	if r == nil {
		return h, nil
	}
	h.spans = prometheus.NewCounterVec(prometheus.CounterOpts{Name: "shinyhub_trace_export_spans_total", Help: "Server spans in completed OTLP export calls by result; success does not prove backend retention."}, []string{"result"})
	h.batches = prometheus.NewCounterVec(prometheus.CounterOpts{Name: "shinyhub_trace_export_batches_total", Help: "Completed server OTLP export calls by result, after exporter retries."}, []string{"result"})
	h.lastSuccess = prometheus.NewGauge(prometheus.GaugeOpts{Name: "shinyhub_trace_export_last_success_timestamp_seconds", Help: "Unix timestamp of the last successful server trace export; zero before the first success."})
	for _, result := range []string{"success", "failure"} {
		h.spans.WithLabelValues(result)
		h.batches.WithLabelValues(result)
	}
	var registered []prometheus.Collector
	for _, c := range []prometheus.Collector{h.spans, h.batches, h.lastSuccess} {
		if err := r.Register(c); err != nil {
			for _, prev := range registered {
				r.Unregister(prev)
			}
			return nil, err
		}
		registered = append(registered, c)
	}
	return h, nil
}

// ExportSpans preserves normal SDK batching, retry and timeout behavior. Health
// logging uses fixed classifications so endpoints, credentials and payloads
// from exporter errors cannot leak through the SDK's default error handler.
func (h *exportHealth) ExportSpans(ctx context.Context, spans []sdktrace.ReadOnlySpan) error {
	err := h.SpanExporter.ExportSpans(ctx, spans)
	if len(spans) == 0 {
		return err
	}
	result := "success"
	if err != nil {
		result = "failure"
	}
	if h.spans != nil {
		h.spans.WithLabelValues(result).Add(float64(len(spans)))
		h.batches.WithLabelValues(result).Inc()
		if err == nil {
			h.lastSuccess.SetToCurrentTime()
		}
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if err != nil {
		h.failing = true
		if h.lastExportWarning.IsZero() || time.Since(h.lastExportWarning) >= time.Minute {
			h.lastExportWarning = time.Now()
			slog.Warn("trace_export_failed", "error_type", exportErrorKind(err), "spans", len(spans))
		}
		return safeExportError{cause: err}
	}
	if h.failing {
		slog.Info("trace_export_recovered")
		h.failing = false
		h.lastExportWarning = time.Time{}
	}
	return nil
}

type safeExportError struct{ cause error }

func (e safeExportError) Error() string {
	return fmt.Sprintf("trace export failed (%s)", exportErrorKind(e.cause))
}
func (e safeExportError) Unwrap() error { return e.cause }
func exportErrorKind(err error) string {
	if errors.Is(err, context.DeadlineExceeded) {
		return "timeout"
	}
	if errors.Is(err, context.Canceled) {
		return "canceled"
	}
	return "export"
}

// Handle integrates SDK errors with structured logging, avoiding duplicate
// reporting of export errors already accounted for above.
func (h *exportHealth) Handle(err error) {
	if err == nil {
		return
	}
	var reported safeExportError
	if errors.As(err, &reported) {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.lastSDKWarning.IsZero() || time.Since(h.lastSDKWarning) >= time.Minute {
		h.lastSDKWarning = time.Now()
		slog.Warn("trace_sdk_error", "error_type", "sdk")
	}
}

// The SDK reads this gate at processor construction. Scope our default to
// startup so child processes and subsequent instrumentation do not inherit it.
func enableSDKProcessorMetrics() (func(), error) {
	if os.Getenv("OTEL_GO_X_OBSERVABILITY") != "" || os.Getenv("OTEL_GO_X_SELF_OBSERVABILITY") != "" {
		return func() {}, nil
	}
	previous, set := os.LookupEnv("OTEL_GO_X_OBSERVABILITY")
	if err := os.Setenv("OTEL_GO_X_OBSERVABILITY", "true"); err != nil {
		return nil, err
	}
	return func() {
		if set {
			_ = os.Setenv("OTEL_GO_X_OBSERVABILITY", previous)
		} else {
			_ = os.Unsetenv("OTEL_GO_X_OBSERVABILITY")
		}
	}, nil
}
