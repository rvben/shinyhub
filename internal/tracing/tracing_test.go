package tracing

import (
	"strings"
	"testing"
	"time"

	"github.com/rvben/shinyhub/internal/config"
)

func TestParseTraceparent_Valid(t *testing.T) {
	v := "00-0af7651916cd43dd8448eb211c80319c-b7ad6b7169203331-01"
	ctx, ok := ParseTraceparent(v)
	if !ok {
		t.Fatalf("expected ok=true for valid traceparent")
	}
	if got := ctx.TraceIDHex(); got != "0af7651916cd43dd8448eb211c80319c" {
		t.Errorf("trace id mismatch: got %q", got)
	}
	if !ctx.Sampled() {
		t.Errorf("expected sampled flag set")
	}
	if got := ctx.TraceparentHeader(); got != v {
		t.Errorf("round-trip mismatch: got %q, want %q", got, v)
	}
}

func TestParseTraceparent_Invalid(t *testing.T) {
	cases := map[string]string{
		"empty":            "",
		"too short":        "00-abc-def-01",
		"wrong version":    "01-0af7651916cd43dd8448eb211c80319c-b7ad6b7169203331-01",
		"bad trace len":    "00-0af7651916cd43dd-b7ad6b7169203331-01",
		"bad span len":    "00-0af7651916cd43dd8448eb211c80319c-b7ad6b71-01",
		"bad flags len":   "00-0af7651916cd43dd8448eb211c80319c-b7ad6b7169203331-1",
		"non-hex trace":    "00-zz7651916cd43dd8448eb211c80319c0-b7ad6b7169203331-01",
		"zero trace id":   "00-00000000000000000000000000000000-b7ad6b7169203331-01",
		"zero span id":    "00-0af7651916cd43dd8448eb211c80319c-0000000000000000-01",
		"too many parts":  "00-0af7651916cd43dd8448eb211c80319c-b7ad6b7169203331-01-extra",
	}
	for name, v := range cases {
		t.Run(name, func(t *testing.T) {
			if _, ok := ParseTraceparent(v); ok {
				t.Errorf("expected ok=false for %s", name)
			}
		})
	}
}

func TestSampleByTraceID_Boundaries(t *testing.T) {
	// ratio <= 0: never sample
	if SampleByTraceID(NewTraceID(), 0) {
		t.Errorf("ratio=0 should never sample")
	}
	if SampleByTraceID(NewTraceID(), -0.5) {
		t.Errorf("negative ratio should never sample")
	}
	// ratio >= 1: always sample
	if !SampleByTraceID(NewTraceID(), 1) {
		t.Errorf("ratio=1 should always sample")
	}
	if !SampleByTraceID(NewTraceID(), 1.5) {
		t.Errorf("ratio>1 should always sample")
	}

	// Deterministic per-ID: trace IDs whose first byte is 0x00 should fall
	// well below the 10% threshold; trace IDs starting with 0xFF should be
	// well above.
	var lo [16]byte
	lo[15] = 1 // non-zero to satisfy isZero
	if !SampleByTraceID(lo, 0.1) {
		t.Errorf("trace ID with leading 0x00 should sample at ratio=0.1")
	}
	var hi [16]byte
	for i := range hi {
		hi[i] = 0xFF
	}
	if SampleByTraceID(hi, 0.1) {
		t.Errorf("trace ID with leading 0xFF should not sample at ratio=0.1")
	}
}

func TestBuffer_AdmissionRules(t *testing.T) {
	buf := NewBuffer(10, 1*time.Second)
	// Fast, healthy span: dropped.
	buf.Record(Span{AppSlug: "a", Status: 200, DurationMS: 50})
	if got := buf.Snapshot("a"); len(got) != 0 {
		t.Errorf("fast healthy span should be dropped, got %d", len(got))
	}
	// Slow span (>= threshold): admitted.
	buf.Record(Span{AppSlug: "a", Status: 200, DurationMS: 1000})
	// Error span: admitted.
	buf.Record(Span{AppSlug: "a", Status: 502, DurationMS: 10})
	// Span carrying Error string: admitted.
	buf.Record(Span{AppSlug: "a", Status: 200, DurationMS: 10, Error: "boom"})
	if got := buf.Snapshot("a"); len(got) != 3 {
		t.Errorf("expected 3 admitted spans, got %d", len(got))
	}
}

func TestBuffer_EmptySlugDropped(t *testing.T) {
	buf := NewBuffer(5, 1*time.Second)
	buf.Record(Span{AppSlug: "", Status: 500, DurationMS: 5000})
	if got := buf.Snapshot(""); len(got) != 0 {
		t.Errorf("empty slug must not be recorded")
	}
}

func TestBuffer_ZeroSizeIsNoOp(t *testing.T) {
	buf := NewBuffer(0, 1*time.Second)
	buf.Record(Span{AppSlug: "a", Status: 500})
	if got := buf.Snapshot("a"); got != nil {
		t.Errorf("size=0 buffer should never return spans, got %v", got)
	}
}

func TestBuffer_NilReceiver(t *testing.T) {
	var buf *Buffer
	// Must not panic.
	buf.Record(Span{AppSlug: "a", Status: 500})
	if got := buf.Snapshot("a"); got != nil {
		t.Errorf("nil receiver Snapshot should return nil, got %v", got)
	}
}

func TestBuffer_RingEviction(t *testing.T) {
	buf := NewBuffer(3, 1*time.Second)
	for i := 1; i <= 5; i++ {
		buf.Record(Span{AppSlug: "a", Status: 500, DurationMS: int64(i)})
	}
	got := buf.Snapshot("a")
	if len(got) != 3 {
		t.Fatalf("expected 3 retained spans after eviction, got %d", len(got))
	}
	// Newest-first ordering: last inserted (duration=5) is at index 0.
	wantOrder := []int64{5, 4, 3}
	for i, w := range wantOrder {
		if got[i].DurationMS != w {
			t.Errorf("snapshot[%d].DurationMS = %d, want %d", i, got[i].DurationMS, w)
		}
	}
}

func TestBuffer_PerAppIsolation(t *testing.T) {
	buf := NewBuffer(5, 1*time.Second)
	buf.Record(Span{AppSlug: "a", Status: 500, DurationMS: 1})
	buf.Record(Span{AppSlug: "b", Status: 500, DurationMS: 2})
	buf.Record(Span{AppSlug: "a", Status: 500, DurationMS: 3})

	a := buf.Snapshot("a")
	b := buf.Snapshot("b")
	if len(a) != 2 {
		t.Errorf("app a: expected 2 spans, got %d", len(a))
	}
	if len(b) != 1 {
		t.Errorf("app b: expected 1 span, got %d", len(b))
	}
	if got := buf.Snapshot("c"); got != nil {
		t.Errorf("unknown app should yield nil, got %v", got)
	}
}

func TestEnvFor_DisabledReturnsNil(t *testing.T) {
	cfg := config.TracingConfig{Enabled: false, OTLPEndpoint: "http://collector:4318"}
	if got := EnvFor(cfg, "myapp", 0); got != nil {
		t.Errorf("disabled tracing should return nil env, got %v", got)
	}
}

func TestEnvFor_EnabledNoEndpointReturnsNil(t *testing.T) {
	cfg := config.TracingConfig{Enabled: true, OTLPEndpoint: ""}
	if got := EnvFor(cfg, "myapp", 0); got != nil {
		t.Errorf("enabled but endpointless tracing should return nil env, got %v", got)
	}
}

func TestEnvFor_EnabledFull(t *testing.T) {
	cfg := config.TracingConfig{
		Enabled:      true,
		OTLPEndpoint: "http://collector:4318",
		OTLPProtocol: "http/protobuf",
		OTLPHeaders:  "x-key=secret",
		SampleRatio:  0.25,
	}
	env := EnvFor(cfg, "my-app", 2)
	want := map[string]string{
		"OTEL_SERVICE_NAME":           "my-app",
		"OTEL_RESOURCE_ATTRIBUTES":    "shinyhub.app=my-app,shinyhub.replica=2",
		"OTEL_EXPORTER_OTLP_ENDPOINT": "http://collector:4318",
		"OTEL_EXPORTER_OTLP_PROTOCOL": "http/protobuf",
		"OTEL_EXPORTER_OTLP_HEADERS":  "x-key=secret",
		"OTEL_TRACES_SAMPLER":         "parentbased_traceidratio",
		"OTEL_TRACES_SAMPLER_ARG":     "0.25",
	}
	got := envToMap(env)
	for k, v := range want {
		if got[k] != v {
			t.Errorf("env[%s] = %q, want %q", k, got[k], v)
		}
	}
}

func TestEnvFor_OmitsHeadersWhenEmpty(t *testing.T) {
	cfg := config.TracingConfig{
		Enabled:      true,
		OTLPEndpoint: "http://collector:4318",
		OTLPProtocol: "http/protobuf",
		SampleRatio:  0.1,
	}
	env := EnvFor(cfg, "app", 0)
	for _, e := range env {
		if strings.HasPrefix(e, "OTEL_EXPORTER_OTLP_HEADERS=") {
			t.Errorf("expected no OTEL_EXPORTER_OTLP_HEADERS when blank, got %q", e)
		}
	}
}

func TestStartProxySpan_ContinuesUpstreamTrace(t *testing.T) {
	incoming := "00-0af7651916cd43dd8448eb211c80319c-b7ad6b7169203331-01"
	ctx, parent, sampled := StartProxySpan(incoming, config.TracingConfig{SampleRatio: 0})
	if got := ctx.TraceIDHex(); got != "0af7651916cd43dd8448eb211c80319c" {
		t.Errorf("expected trace id continuity, got %q", got)
	}
	if parent != "b7ad6b7169203331" {
		t.Errorf("expected parent span id = upstream span id, got %q", parent)
	}
	if !sampled {
		t.Errorf("expected sampled=true inherited from upstream flags")
	}
	// New span ID, not the upstream's.
	if ctx.TraceparentHeader() == incoming {
		t.Errorf("expected fresh span id, got identical traceparent")
	}
}

func TestStartProxySpan_StartsFreshWhenAbsent(t *testing.T) {
	ctx, parent, _ := StartProxySpan("", config.TracingConfig{SampleRatio: 1})
	if parent != "" {
		t.Errorf("no upstream: expected empty parent, got %q", parent)
	}
	if ctx.TraceIDHex() == "" {
		t.Errorf("expected freshly generated trace id")
	}
	if !ctx.Sampled() {
		t.Errorf("ratio=1: expected sampled=true")
	}
}

func envToMap(env []string) map[string]string {
	m := make(map[string]string, len(env))
	for _, e := range env {
		if i := strings.IndexByte(e, '='); i >= 0 {
			m[e[:i]] = e[i+1:]
		}
	}
	return m
}
