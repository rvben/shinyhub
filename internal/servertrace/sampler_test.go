package servertrace

import (
	"context"
	"fmt"
	"testing"

	"github.com/rvben/shinyhub/internal/tracing"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"
)

func TestSamplerCorrelatesOnlyWSCloseRoots(t *testing.T) {
	sampled := trace.NewSpanContext(trace.SpanContextConfig{
		TraceID: trace.TraceID{1}, SpanID: trace.SpanID{1}, TraceFlags: trace.FlagsSampled,
	})
	unsampled := sampled.WithTraceFlags(0)
	for _, ratio := range []float64{0, 1} {
		t.Run(fmt.Sprint(ratio), func(t *testing.T) {
			fallback := sdktrace.Drop
			if ratio == 1 {
				fallback = sdktrace.RecordAndSample
			}
			for _, tc := range []struct {
				name     string
				spanName string
				kind     trace.SpanKind
				links    []trace.Link
				parent   trace.SpanContext
				want     sdktrace.SamplingDecision
			}{
				{"sampled handshake", tracing.WSCloseSpanName, trace.SpanKindInternal, []trace.Link{{SpanContext: sampled}}, trace.SpanContext{}, sdktrace.RecordAndSample},
				{"unsampled handshake", tracing.WSCloseSpanName, trace.SpanKindInternal, []trace.Link{{SpanContext: unsampled}}, trace.SpanContext{}, sdktrace.Drop},
				{"no handshake", tracing.WSCloseSpanName, trace.SpanKindInternal, nil, trace.SpanContext{}, fallback},
				{"invalid handshake", tracing.WSCloseSpanName, trace.SpanKindInternal, []trace.Link{{}}, trace.SpanContext{}, fallback},
				{"other linked root", "other", trace.SpanKindInternal, []trace.Link{{SpanContext: sampled}}, trace.SpanContext{}, fallback},
				{"other kind", tracing.WSCloseSpanName, trace.SpanKindServer, []trace.Link{{SpanContext: sampled}}, trace.SpanContext{}, fallback},
				{"multiple links", tracing.WSCloseSpanName, trace.SpanKindInternal, []trace.Link{{SpanContext: sampled}, {SpanContext: unsampled}}, trace.SpanContext{}, fallback},
				{"sampled parent", tracing.WSCloseSpanName, trace.SpanKindInternal, []trace.Link{{SpanContext: unsampled}}, sampled, sdktrace.RecordAndSample},
				{"unsampled parent", tracing.WSCloseSpanName, trace.SpanKindInternal, []trace.Link{{SpanContext: sampled}}, unsampled, sdktrace.Drop},
			} {
				t.Run(tc.name, func(t *testing.T) {
					got := NewSampler(ratio).ShouldSample(sdktrace.SamplingParameters{
						ParentContext: trace.ContextWithSpanContext(context.Background(), tc.parent),
						TraceID:       trace.TraceID{2}, Name: tc.spanName, Kind: tc.kind, Links: tc.links,
					})
					if got.Decision != tc.want {
						t.Fatalf("decision = %v, want %v", got.Decision, tc.want)
					}
				})
			}
		})
	}
}
