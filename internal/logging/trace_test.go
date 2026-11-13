package logging

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"

	"go.opentelemetry.io/otel/trace"
)

func TestContextualLogCorrelation(t *testing.T) {
	for _, sampled := range []bool{false, true} {
		var buf bytes.Buffer
		logger := slog.New(WithTraceContext(slog.NewJSONHandler(&buf, nil)))
		flags := trace.TraceFlags(0)
		if sampled {
			flags = trace.FlagsSampled
		}
		sc := trace.NewSpanContext(trace.SpanContextConfig{TraceID: trace.TraceID{1}, SpanID: trace.SpanID{2}, TraceFlags: flags})
		ctx := trace.ContextWithSpanContext(context.Background(), sc)
		logger.With("app", "demo").InfoContext(ctx, "active")
		var rec map[string]any
		if err := json.Unmarshal(buf.Bytes(), &rec); err != nil {
			t.Fatal(err)
		}
		if rec["trace_id"] != sc.TraceID().String() || rec["span_id"] != sc.SpanID().String() || rec["trace_sampled"] != sampled || rec["app"] != "demo" {
			t.Fatalf("correlation: %v", rec)
		}
		buf.Reset()
		logger.InfoContext(context.Background(), "background")
		if strings.Contains(buf.String(), "trace_id") || strings.Contains(buf.String(), "span_id") {
			t.Fatal("background logs must not invent identities")
		}
	}
}

func TestExplicitLifecycleIdentityIsNotMixedWithCurrentSpan(t *testing.T) {
	ctx := trace.ContextWithSpanContext(context.Background(), trace.NewSpanContext(trace.SpanContextConfig{TraceID: trace.TraceID{1}, SpanID: trace.SpanID{2}}))
	for _, bound := range []bool{false, true} {
		var buf bytes.Buffer
		logger := slog.New(WithTraceContext(slog.NewJSONHandler(&buf, nil)))
		if bound {
			logger = logger.With("trace_id", "completed")
			logger.InfoContext(ctx, "lifecycle")
		} else {
			logger.InfoContext(ctx, "lifecycle", "trace_id", "completed")
		}
		if strings.Count(buf.String(), `"trace_id"`) != 1 || strings.Contains(buf.String(), "span_id") || strings.Contains(buf.String(), "trace_sampled") {
			t.Fatalf("mixed or duplicate identities: %s", buf.String())
		}
	}
}
