package deploy

import (
	"context"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"

	"github.com/rvben/shinyhub/internal/spanerr"
)

// startPhase opens a child span for one deploy phase and returns Params whose
// TraceCtx is that span, so nested phases parent under it. A nil Tracer makes
// it a no-op. TraceCtx only ever carries parentage; phases derive their own
// cancellation, as before.
func (p Params) startPhase(name string, attrs ...attribute.KeyValue) (Params, func(error)) {
	if p.Tracer == nil {
		return p, func(error) {}
	}
	parent := p.TraceCtx
	if parent == nil {
		parent = context.Background()
	}
	attrs = append(attrs, attribute.String("shinyhub.app.slug", p.Slug))
	ctx, span := p.Tracer.Start(parent, name, trace.WithSpanKind(trace.SpanKindInternal), trace.WithAttributes(attrs...))
	p.TraceCtx = ctx
	return p, func(err error) {
		if err != nil {
			spanerr.Record(span, err)
		}
		span.End()
	}
}

// annotate sets attributes on the span of the innermost phase this Params
// opened. It does nothing without a Tracer, so a caller's own span (carried in
// TraceCtx before any phase opened) is never written to.
func (p Params) annotate(attrs ...attribute.KeyValue) {
	if p.Tracer == nil || p.TraceCtx == nil {
		return
	}
	trace.SpanFromContext(p.TraceCtx).SetAttributes(attrs...)
}

// markBuildReused records on the deploy.run span that an activation launched
// against the environment a previous preparation built, instead of rebuilding.
// Outside Run (a single-replica restart or wake) there is no deploy.run span
// and nothing is recorded.
func (p Params) markBuildReused() {
	if p.runSpan != nil {
		p.runSpan.SetAttributes(attribute.Bool("shinyhub.deploy.build_reused", true))
	}
}
