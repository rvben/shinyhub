// Package servertrace adds optional OpenTelemetry tracing for the ShinyHub
// server process itself - spans for the control-plane API request handling,
// exported to the same OTLP endpoint the managed apps use.
//
// This is deliberately separate from internal/tracing, which is a dependency-
// free W3C-context propagator on the proxy hot path. The control plane is low
// volume, so the full OTel SDK (batching exporter, background flush) is an
// acceptable cost here. Server spans appear alongside app spans in one backend,
// and an inbound traceparent links a client/edge trace through ShinyHub to the
// app it proxies.
package servertrace

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"net/http"
	"net/url"
	"os"
	"strings"

	"github.com/go-chi/chi/v5"
	chimw "github.com/go-chi/chi/v5/middleware"
	"github.com/rvben/shinyhub/internal/config"
	"github.com/rvben/shinyhub/internal/httproute"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"
)

// instrumentationScope names the tracer in emitted spans.
const instrumentationScope = "github.com/rvben/shinyhub"

// Tracer carries the SDK provider, the W3C propagator, and a tracer handle. Use
// Setup in production or NewFromProvider in tests with an in-memory exporter.
type Tracer struct {
	tp         *sdktrace.TracerProvider
	propagator propagation.TextMapPropagator
	tracer     trace.Tracer
}

// NewFromProvider builds a Tracer around an existing provider and propagator.
func NewFromProvider(tp *sdktrace.TracerProvider, prop propagation.TextMapPropagator) *Tracer {
	return &Tracer{tp: tp, propagator: prop, tracer: tp.Tracer(instrumentationScope)}
}

// Setup builds a Tracer that exports to the configured OTLP endpoint and
// registers it as the global OpenTelemetry provider + propagator. The exporter
// connects lazily, so Setup does not block on the collector being reachable.
func Setup(ctx context.Context, cfg config.TracingConfig, serviceVersion string) (*Tracer, error) {
	exp, err := newExporter(ctx, cfg)
	if err != nil {
		return nil, err
	}
	tp := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(exp),
		sdktrace.WithResource(buildResource(serviceVersion)),
		sdktrace.WithSampler(sdktrace.ParentBased(sdktrace.TraceIDRatioBased(cfg.SampleRatio))),
	)
	prop := propagation.NewCompositeTextMapPropagator(propagation.TraceContext{}, propagation.Baggage{})
	otel.SetTracerProvider(tp)
	otel.SetTextMapPropagator(prop)
	return NewFromProvider(tp, prop), nil
}

// Shutdown flushes pending spans and stops the exporter.
func (t *Tracer) Shutdown(ctx context.Context) error { return t.tp.Shutdown(ctx) }

// Tracer returns the underlying span tracer so other components (e.g. the
// lifecycle watcher) can emit spans into the same provider/exporter.
func (t *Tracer) Tracer() trace.Tracer { return t.tracer }

// Middleware records one server span per request, named by the matched chi
// route PATTERN (collapsing path params and unmatched 404s so span/attribute
// cardinality stays bounded). An inbound W3C traceparent is adopted as the
// parent so upstream traces link through to ShinyHub.
//
// Route pattern resolution uses a two-tier priority: httproute.PatternFromContext
// is checked first. When set (by api.Observe before the inner handler runs), it
// provides an immutable string copy that is safe to read after next.ServeHTTP
// returns even across an http.TimeoutHandler boundary. When it is absent (e.g.
// this middleware is mounted directly inside chi without the Observe wrapper),
// chi.RouteContext is consulted as a fallback so the standard chi-as-middleware
// use case continues to work.
func (t *Tracer) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := t.propagator.Extract(r.Context(), propagation.HeaderCarrier(r.Header))
		ctx, span := t.tracer.Start(ctx, r.Method, trace.WithSpanKind(trace.SpanKindServer))
		defer span.End()

		ww := chimw.NewWrapResponseWriter(w, r.ProtoMajor)
		next.ServeHTTP(ww, r.WithContext(ctx))

		route := httproute.PatternFromContext(ctx)
		if route == "" {
			if rc := chi.RouteContext(ctx); rc != nil {
				route = rc.RoutePattern()
			}
		}
		if route == "" {
			route = "unmatched"
		}
		status := ww.Status()
		if status == 0 {
			status = http.StatusOK
		}
		span.SetName(r.Method + " " + route)
		span.SetAttributes(
			attribute.String("http.request.method", r.Method),
			attribute.String("http.route", route),
			attribute.Int("http.response.status_code", status),
		)
		if status >= 500 {
			span.SetStatus(codes.Error, http.StatusText(status))
		}
	})
}

// buildResource identifies the ShinyHub server process in the trace backend.
// service.instance.id distinguishes multiple instances exporting to the same
// backend; the hostname is used, falling back to a random token when it is
// unavailable so the attribute is never empty.
func buildResource(version string) *resource.Resource {
	return resource.NewSchemaless(
		attribute.String("service.name", "shinyhub"),
		attribute.String("service.version", version),
		attribute.String("service.instance.id", serviceInstanceID()),
	)
}

// serviceInstanceID returns the host's name, or a random hex token when the
// hostname cannot be determined.
func serviceInstanceID() string {
	if h, err := os.Hostname(); err == nil && h != "" {
		return h
	}
	var b [8]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

// newExporter builds the OTLP span exporter for the configured protocol. The
// config layer has already validated OTLPProtocol is one of http/protobuf, grpc.
func newExporter(ctx context.Context, cfg config.TracingConfig) (sdktrace.SpanExporter, error) {
	headers := parseHeaders(cfg.OTLPHeaders)
	if cfg.OTLPProtocol == "grpc" {
		endpoint, insecure := grpcEndpoint(cfg.OTLPEndpoint)
		opts := []otlptracegrpc.Option{otlptracegrpc.WithEndpoint(endpoint)}
		if insecure {
			opts = append(opts, otlptracegrpc.WithInsecure())
		}
		if len(headers) > 0 {
			opts = append(opts, otlptracegrpc.WithHeaders(headers))
		}
		return otlptracegrpc.New(ctx, opts...)
	}
	opts := []otlptracehttp.Option{otlptracehttp.WithEndpointURL(httpTracesEndpoint(cfg.OTLPEndpoint))}
	if len(headers) > 0 {
		opts = append(opts, otlptracehttp.WithHeaders(headers))
	}
	return otlptracehttp.New(ctx, opts...)
}

// httpTracesEndpoint turns a base OTLP endpoint into the per-signal traces URL,
// matching the OTEL_EXPORTER_OTLP_ENDPOINT semantics the managed apps rely on:
// the /v1/traces path is appended to whatever base path the endpoint already
// carries, with exactly one slash between them. A malformed endpoint is
// returned unchanged so the exporter surfaces the original error.
func httpTracesEndpoint(base string) string {
	u, err := url.Parse(base)
	if err != nil {
		return base
	}
	u.Path = strings.TrimSuffix(u.Path, "/") + "/v1/traces"
	return u.String()
}

// grpcEndpoint reduces an endpoint URL to the host:port the gRPC exporter wants
// and reports whether the connection should be insecure (any non-https scheme).
// A bare host:port (no scheme) is treated as insecure.
func grpcEndpoint(raw string) (endpoint string, insecure bool) {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return raw, true
	}
	return u.Host, u.Scheme != "https"
}

// parseHeaders turns an "k=v,k2=v2" string (the OTEL_EXPORTER_OTLP_HEADERS form)
// into a map. Malformed pairs are skipped.
func parseHeaders(s string) map[string]string {
	out := map[string]string{}
	for _, pair := range strings.Split(s, ",") {
		pair = strings.TrimSpace(pair)
		if pair == "" {
			continue
		}
		k, v, ok := strings.Cut(pair, "=")
		if !ok {
			continue
		}
		out[strings.TrimSpace(k)] = strings.TrimSpace(v)
	}
	return out
}
