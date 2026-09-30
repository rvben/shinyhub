package tracing

import (
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/rvben/shinyhub/internal/config"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"
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
		"empty":          "",
		"too short":      "00-abc-def-01",
		"wrong version":  "01-0af7651916cd43dd8448eb211c80319c-b7ad6b7169203331-01",
		"bad trace len":  "00-0af7651916cd43dd-b7ad6b7169203331-01",
		"bad span len":   "00-0af7651916cd43dd8448eb211c80319c-b7ad6b71-01",
		"bad flags len":  "00-0af7651916cd43dd8448eb211c80319c-b7ad6b7169203331-1",
		"non-hex trace":  "00-zz7651916cd43dd8448eb211c80319c0-b7ad6b7169203331-01",
		"zero trace id":  "00-00000000000000000000000000000000-b7ad6b7169203331-01",
		"zero span id":   "00-0af7651916cd43dd8448eb211c80319c-0000000000000000-01",
		"too many parts": "00-0af7651916cd43dd8448eb211c80319c-b7ad6b7169203331-01-extra",
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

	// Deterministic per-ID: the decision is made from the low 8 bytes (as the
	// OTel SDK's TraceIDRatioBased does), not the high 8 bytes. An ID with a
	// high first half and a zero second half must sample; its byte-reversal
	// must not. The old first-bytes algorithm got both of these backwards.
	var lowHalfZero [16]byte
	for i := 0; i < 8; i++ {
		lowHalfZero[i] = 0xFF
	}
	if !SampleByTraceID(lowHalfZero, 0.5) {
		t.Errorf("trace ID with zero low half should sample at ratio=0.5")
	}
	var highHalfZero [16]byte
	for i := 8; i < 16; i++ {
		highHalfZero[i] = 0xFF
	}
	if SampleByTraceID(highHalfZero, 0.5) {
		t.Errorf("trace ID with 0xFF low half should not sample at ratio=0.5")
	}
}

// TestSampleByTraceID_MatchesSDKTraceIDRatioBased pins agreement with the
// OTel SDK's TraceIDRatioBased decision, which the server tracer uses, so the
// proxy fallback path and the server tracer agree on every trace ID.
func TestSampleByTraceID_MatchesSDKTraceIDRatioBased(t *testing.T) {
	for _, ratio := range []float64{0.1, 0.5, 0.9} {
		sdk := sdktrace.TraceIDRatioBased(ratio)
		var agreeTrue, agreeFalse int
		for i := 0; i < 5000; i++ {
			id := NewTraceID()
			want := sdk.ShouldSample(sdktrace.SamplingParameters{TraceID: trace.TraceID(id)}).Decision == sdktrace.RecordAndSample
			if got := SampleByTraceID(id, ratio); got != want {
				t.Fatalf("ratio %g id %x: proxy=%v sdk=%v", ratio, id, got, want)
			}
			if want {
				agreeTrue++
			} else {
				agreeFalse++
			}
		}
		if agreeTrue == 0 || agreeFalse == 0 {
			t.Fatalf("ratio %g: degenerate sample (%d/%d), test proves nothing", ratio, agreeTrue, agreeFalse)
		}
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

func TestBuffer_ForgetRemovesOnlyThatApp(t *testing.T) {
	buf := NewBuffer(5, 1*time.Second)
	buf.Record(Span{AppSlug: "a", Status: 500, DurationMS: 1})
	buf.Record(Span{AppSlug: "b", Status: 500, DurationMS: 2})

	buf.Forget("a")

	if got := buf.Snapshot("a"); got != nil {
		t.Errorf("forgotten app should yield nil, got %v", got)
	}
	if got := buf.Snapshot("b"); len(got) != 1 {
		t.Errorf("app b: expected 1 span to survive Forget(\"a\"), got %d", len(got))
	}

	// A deleted app's slug can be redeployed later; it must start with a clean
	// ring, not resume mid-way through the old one's wraparound state.
	buf.Record(Span{AppSlug: "a", Status: 500, DurationMS: 99})
	got := buf.Snapshot("a")
	if len(got) != 1 || got[0].DurationMS != 99 {
		t.Errorf("app a after redeploy: got %v, want a single fresh span", got)
	}
}

func TestBuffer_ForgetUnknownSlugIsNoop(t *testing.T) {
	buf := NewBuffer(5, 1*time.Second)
	buf.Record(Span{AppSlug: "a", Status: 500, DurationMS: 1})
	buf.Forget("never-recorded")
	if got := buf.Snapshot("a"); len(got) != 1 {
		t.Errorf("unrelated Forget must not disturb app a, got %v", got)
	}
}

func TestBuffer_ForgetNilReceiver(t *testing.T) {
	var buf *Buffer
	// Must not panic.
	buf.Forget("a")
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
		"OTEL_SERVICE_NAME":                   "my-app",
		"OTEL_RESOURCE_ATTRIBUTES":            "shinyhub.app=my-app,shinyhub.app.slug=my-app,shinyhub.replica=2",
		"OTEL_EXPORTER_OTLP_ENDPOINT":         "http://collector:4318",
		"OTEL_EXPORTER_OTLP_PROTOCOL":         "http/protobuf",
		"OTEL_EXPORTER_OTLP_HEADERS":          "x-key=secret",
		"OTEL_TRACES_SAMPLER":                 "parentbased_traceidratio",
		"OTEL_TRACES_SAMPLER_ARG":             "0.25",
		"OTEL_PYTHON_STARLETTE_EXCLUDED_URLS": StarletteExcludedURLs,
		"SHINYHUB_TRACING_ASGI_EVENTS":        "false",
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
	ctx, parent, sampled := StartProxySpan(incoming, "", config.TracingConfig{SampleRatio: 0})
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
	ctx, parent, _ := StartProxySpan("", "", config.TracingConfig{SampleRatio: 1})
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

// TRC-4: tracestate is carried through when the upstream traceparent is valid.
func TestStartProxySpan_CarriesTracestateOnContinuation(t *testing.T) {
	incoming := "00-0af7651916cd43dd8448eb211c80319c-b7ad6b7169203331-01"
	ctx, _, _ := StartProxySpan(incoming, " dd=s:1;o:rum ", config.TracingConfig{SampleRatio: 0})
	if ctx.TraceState != "dd=s:1;o:rum" {
		t.Errorf("expected trimmed tracestate carried through, got %q", ctx.TraceState)
	}
}

// TRC-4: a fresh trace drops any inbound tracestate (it refers to another trace).
func TestStartProxySpan_DropsTracestateOnFreshTrace(t *testing.T) {
	ctx, _, _ := StartProxySpan("", "dd=s:1;o:rum", config.TracingConfig{SampleRatio: 1})
	if ctx.TraceState != "" {
		t.Errorf("fresh trace must not inherit upstream tracestate, got %q", ctx.TraceState)
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

// envValue returns the value of the last "key=" entry in env, failing the
// test if key is absent (last-occurrence-wins mirrors how the process
// manager applies these to a command's environment).
func envValue(t *testing.T, env []string, key string) string {
	t.Helper()
	prefix := key + "="
	found := false
	var out string
	for _, e := range env {
		if strings.HasPrefix(e, prefix) {
			out = strings.TrimPrefix(e, prefix)
			found = true
		}
	}
	if !found {
		t.Fatalf("env missing key %q: %v", key, env)
	}
	return out
}

// enabledCfg returns a minimal TracingConfig with tracing enabled and an
// endpoint set, the common starting point for EnvFor/JobEnvFor tests.
func enabledCfg() config.TracingConfig {
	return config.TracingConfig{
		Enabled:      true,
		OTLPEndpoint: "http://c:4318",
		OTLPProtocol: "http/protobuf",
		SampleRatio:  1,
	}
}

func TestEnvFor_AppendsResourceAttributesSortedAndEncoded(t *testing.T) {
	cfg := enabledCfg()
	cfg.ResourceAttributes = map[string]string{"team": "data, eng", "deployment.environment.name": "prd", "owner": "Zoë=x%"}
	got := envValue(t, EnvFor(cfg, "a", 0), "OTEL_RESOURCE_ATTRIBUTES")
	want := "shinyhub.app=a,shinyhub.app.slug=a,shinyhub.replica=0,deployment.environment.name=prd,owner=Zo%C3%AB%3Dx%25,team=data%2C%20eng"
	if got != want {
		t.Fatalf("got %q\nwant %q", got, want)
	}
}

func TestEnvFor_NoResourceAttributesUnchanged(t *testing.T) {
	got := envValue(t, EnvFor(enabledCfg(), "a", 0), "OTEL_RESOURCE_ATTRIBUTES")
	if got != "shinyhub.app=a,shinyhub.app.slug=a,shinyhub.replica=0" {
		t.Fatalf("got %q", got)
	}
}

func TestEncodeResourceAttributes_RoundTripsThroughGoSDKDecoding(t *testing.T) {
	vals := []string{"prd", "data, eng", "a=b", "100%", "Zoë", " lead", "tab\there", "x/y:z"}
	for _, v := range vals {
		enc := EncodeResourceAttributes([][2]string{{"k", v}})
		_, raw, _ := strings.Cut(enc, "=")
		if strings.ContainsAny(raw, ",= \t") {
			t.Fatalf("%q encoded to %q, which still contains a separator", v, raw)
		}
		dec, err := url.PathUnescape(raw) // what go.opentelemetry.io/otel/sdk/resource env detection applies
		if err != nil || dec != v {
			t.Fatalf("%q -> %q -> %q (%v)", v, raw, dec, err)
		}
	}
}

func TestEnvFor_ExcludesStarletteWebsocket(t *testing.T) {
	if got := envValue(t, EnvFor(enabledCfg(), "a", 0), "OTEL_PYTHON_STARLETTE_EXCLUDED_URLS"); got != StarletteExcludedURLs {
		t.Fatalf("got %q", got)
	}
	if EnvFor(config.TracingConfig{}, "a", 0) != nil {
		t.Fatal("disabled tracing must inject nothing")
	}
}

func TestJobEnvFor(t *testing.T) {
	cfg := enabledCfg()
	cfg.ResourceAttributes = map[string]string{"deployment.environment.name": "prd"}
	env := JobEnvFor(cfg, "sales", "nightly refresh", 42)
	if envValue(t, env, "OTEL_SERVICE_NAME") != "sales" {
		t.Fatal("service name must be the app slug")
	}
	want := "shinyhub.app=sales,shinyhub.app.slug=sales,shinyhub.schedule=nightly%20refresh,shinyhub.schedule.run_id=42,deployment.environment.name=prd"
	if got := envValue(t, env, "OTEL_RESOURCE_ATTRIBUTES"); got != want {
		t.Fatalf("got %q want %q", got, want)
	}
	if JobEnvFor(config.TracingConfig{}, "sales", "n", 1) != nil {
		t.Fatal("disabled tracing must inject nothing")
	}
}
