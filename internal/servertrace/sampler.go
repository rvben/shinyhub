package servertrace

import (
	"github.com/rvben/shinyhub/internal/tracing"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"
)

// NewSampler preserves parent-based ratio sampling, with correlated sampling
// for WebSocket close roots. Those roots link to a handshake that may have
// finished hours earlier; honoring its decision keeps both ends observable
// without adding late spans to the same trace. Other linked roots are unaffected.
func NewSampler(ratio float64) sdktrace.Sampler {
	return sdktrace.ParentBased(wsCloseSampler{fallback: sdktrace.TraceIDRatioBased(ratio)})
}

type wsCloseSampler struct{ fallback sdktrace.Sampler }

func (s wsCloseSampler) ShouldSample(p sdktrace.SamplingParameters) sdktrace.SamplingResult {
	if p.Name == tracing.WSCloseSpanName && p.Kind == trace.SpanKindInternal &&
		len(p.Links) == 1 && p.Links[0].SpanContext.IsValid() {
		decision := sdktrace.Drop
		if p.Links[0].SpanContext.IsSampled() {
			decision = sdktrace.RecordAndSample
		}
		return sdktrace.SamplingResult{Decision: decision}
	}
	return s.fallback.ShouldSample(p)
}

func (s wsCloseSampler) Description() string {
	return "WSCloseLinkedHandshake(" + s.fallback.Description() + ")"
}
