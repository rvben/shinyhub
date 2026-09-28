package main

import (
	"context"

	"go.opentelemetry.io/otel/trace"

	"github.com/rvben/shinyhub/internal/deploy"
	"github.com/rvben/shinyhub/internal/servertrace"
)

// traceReplica threads the lifecycle span in ctx (lifecycle.wake or
// lifecycle.restart) into replica deploy Params so the deploy.replica span
// nests under it. Only the span context is kept: a replica boot must never be
// cut short by the cancellation or deadline of whatever triggered it. With
// tracing disabled (nil tracer) Params is returned unchanged.
func traceReplica(ctx context.Context, tracer *servertrace.Tracer, p deploy.Params) deploy.Params {
	if tracer == nil {
		return p
	}
	p.Tracer = tracer.Tracer()
	p.TraceCtx = trace.ContextWithSpanContext(context.Background(), trace.SpanContextFromContext(ctx))
	return p
}
