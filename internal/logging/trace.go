package logging

import (
	"context"
	"log/slog"

	"github.com/rvben/shinyhub/internal/tracing"
	"go.opentelemetry.io/otel/trace"
)

// CorrelationValues uses the same field names for contextual and lifecycle logs.
func CorrelationValues(c tracing.Correlation) []any {
	if c.TraceID == "" || c.SpanID == "" {
		return nil
	}
	return []any{"trace_id", c.TraceID, "span_id", c.SpanID, "trace_sampled", c.Sampled}
}

// SpanCorrelation snapshots identity without keeping a span or context alive.
func SpanCorrelation(sc trace.SpanContext) tracing.Correlation {
	if !sc.IsValid() {
		return tracing.Correlation{}
	}
	return tracing.Correlation{TraceID: sc.TraceID().String(), SpanID: sc.SpanID().String(), Sampled: sc.IsSampled()}
}

// WithTraceContext enriches InfoContext, WarnContext and other contextual logs.
// It never starts spans or adds invalid all-zero IDs. Explicit correlation
// fields are preserved, allowing lifecycle logs to refer to a completed span.
// Fields follow slog grouping; ShinyHub request loggers use the root group.
func WithTraceContext(next slog.Handler) slog.Handler { return traceHandler{next: next} }

type traceHandler struct {
	next     slog.Handler
	explicit bool
}

func (h traceHandler) Enabled(ctx context.Context, level slog.Level) bool {
	return h.next.Enabled(ctx, level)
}
func (h traceHandler) Handle(ctx context.Context, r slog.Record) error {
	sc := trace.SpanContextFromContext(ctx)
	if sc.IsValid() {
		explicit := h.explicit
		r.Attrs(func(a slog.Attr) bool { explicit = explicit || correlationKey(a.Key); return true })
		if !explicit {
			r = r.Clone()
			r.AddAttrs(slog.String("trace_id", sc.TraceID().String()), slog.String("span_id", sc.SpanID().String()), slog.Bool("trace_sampled", sc.IsSampled()))
		}
	}
	return h.next.Handle(ctx, r)
}
func (h traceHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	explicit := h.explicit
	for _, a := range attrs {
		explicit = explicit || correlationKey(a.Key)
	}
	return traceHandler{next: h.next.WithAttrs(attrs), explicit: explicit}
}
func (h traceHandler) WithGroup(name string) slog.Handler {
	return traceHandler{next: h.next.WithGroup(name), explicit: h.explicit}
}

func correlationKey(key string) bool {
	return key == "trace_id" || key == "span_id" || key == "trace_sampled"
}
