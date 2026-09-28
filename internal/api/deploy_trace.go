package api

import (
	"context"

	"go.opentelemetry.io/otel/trace"

	"github.com/rvben/shinyhub/internal/deploy"
	"github.com/rvben/shinyhub/internal/spanerr"
)

// traceDeploy threads the request's server span into deploy Params as a
// parent only: the deploy must never be cancelled by the client's context.
// Callers without a request pass context.Background(), which makes deploy.run a
// root span. With tracing disabled Params is returned unchanged.
func (s *Server) traceDeploy(ctx context.Context, p deploy.Params) deploy.Params {
	if s.tracer == nil {
		return p
	}
	p.Tracer = s.tracer.Tracer()
	p.TraceCtx = trace.ContextWithSpanContext(context.Background(), trace.SpanContextFromContext(ctx))
	return p
}

// phase opens an internal span named name under the span in ctx and returns
// the function that ends it, recording err as the span's error status. The
// span carries err reduced by spanerr (first line, credentials masked,
// bounded); the caller keeps err unchanged. With tracing disabled both are
// no-ops.
func (s *Server) phase(ctx context.Context, name string) func(error) {
	if s.tracer == nil {
		return func(error) {}
	}
	_, span := s.tracer.Tracer().Start(ctx, name, trace.WithSpanKind(trace.SpanKindInternal))
	return func(err error) {
		if err != nil {
			spanerr.Record(span, err)
		}
		span.End()
	}
}
