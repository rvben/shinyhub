package main

import (
	"context"
	"testing"
	"time"

	"go.opentelemetry.io/otel/propagation"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"

	"github.com/rvben/shinyhub/internal/deploy"
	"github.com/rvben/shinyhub/internal/servertrace"
)

// TestTraceReplica_NilTracerLeavesParamsUnchanged pins that tracing disabled
// adds nothing to a lifecycle replica boot.
func TestTraceReplica_NilTracerLeavesParamsUnchanged(t *testing.T) {
	tp := sdktrace.NewTracerProvider(sdktrace.WithSampler(sdktrace.AlwaysSample()))
	t.Cleanup(func() { _ = tp.Shutdown(context.Background()) })
	ctx, span := tp.Tracer("t").Start(context.Background(), "lifecycle.wake")
	defer span.End()

	got := traceReplica(ctx, nil, deploy.Params{Slug: "demo"})

	if got.Tracer != nil || got.TraceCtx != nil {
		t.Fatalf("nil tracer set Tracer=%v TraceCtx=%v, want both unset", got.Tracer, got.TraceCtx)
	}
	if got.Slug != "demo" {
		t.Fatalf("Slug = %q, want demo", got.Slug)
	}
}

// TestTraceReplica_KeepsSpanDropsCancellation proves the replica boot is
// parented under the lifecycle span in ctx while never inheriting that
// context's cancellation or deadline.
func TestTraceReplica_KeepsSpanDropsCancellation(t *testing.T) {
	tp := sdktrace.NewTracerProvider(sdktrace.WithSampler(sdktrace.AlwaysSample()))
	t.Cleanup(func() { _ = tp.Shutdown(context.Background()) })
	tracer := servertrace.NewFromProvider(tp, propagation.TraceContext{})

	spanCtx, span := tp.Tracer("t").Start(context.Background(), "lifecycle.restart")
	defer span.End()
	deadlined, cancelDeadline := context.WithTimeout(spanCtx, time.Hour)
	defer cancelDeadline()
	cancelled, cancel := context.WithCancel(deadlined)
	cancel()

	got := traceReplica(cancelled, tracer, deploy.Params{Slug: "demo"})

	if got.Tracer == nil {
		t.Fatal("Tracer unset with tracing enabled")
	}
	if sc := trace.SpanContextFromContext(got.TraceCtx); !sc.Equal(span.SpanContext()) {
		t.Fatalf("TraceCtx span %v, want %v", sc, span.SpanContext())
	}
	if err := got.TraceCtx.Err(); err != nil {
		t.Fatalf("TraceCtx kept the caller's cancellation: %v", err)
	}
	if _, ok := got.TraceCtx.Deadline(); ok {
		t.Fatal("TraceCtx kept the caller's deadline")
	}
}
