package proxy

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/rvben/shinyhub/internal/servertrace"
	"go.opentelemetry.io/otel/codes"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"
)

func TestWSCloseSpanRecordsLongSessionWithoutLongSpanOrCloseText(t *testing.T) {
	for _, abnormal := range []bool{false, true} {
		rec := tracetest.NewSpanRecorder()
		tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(rec))
		tr := tp.Tracer("test")
		_, handshake := tr.Start(context.Background(), "GET /app/{slug}")
		handshake.End()
		before := time.Now()
		code := uint16(1000)
		recordWSCloseSpan(tr, handshake.SpanContext(), WSSessionEnd{Duration: 6 * time.Hour, Slug: "app", ReplicaIndex: 2, DeploymentID: 17, CloseCode: &code, CloseReason: "sensitive user-controlled message", Abnormal: abnormal, BytesToClient: 42, BytesToUpstream: 7, ClosedBy: "client"})
		ended := rec.Ended()[1]
		if ended.Parent().IsValid() || ended.SpanContext().TraceID() == handshake.SpanContext().TraceID() {
			t.Fatal("close must start an independent trace")
		}
		if links := ended.Links(); len(links) != 1 || !links[0].SpanContext.Equal(handshake.SpanContext()) {
			t.Fatalf("close must link to handshake: %v", links)
		}
		if ended.StartTime().Before(before) || ended.EndTime().After(time.Now()) {
			t.Fatal("close span must cover only close recording, not the six-hour session")
		}
		if ended.Name() != "WS close /app/{slug}" || ended.SpanKind() != trace.SpanKindInternal {
			t.Fatalf("unexpected close span: %v", ended)
		}
		if (ended.Status().Code == codes.Error) != abnormal {
			t.Fatalf("abnormal=%v status=%v", abnormal, ended.Status())
		}
		attrs := map[string]string{}
		for _, kv := range ended.Attributes() {
			attrs[string(kv.Key)] = kv.Value.Emit()
		}
		if attrs["shinyhub.ws.session_duration_seconds"] != "21600" || attrs["shinyhub.ws.close_code"] != "1000" || attrs["shinyhub.ws.bytes_to_client"] != "42" || attrs["shinyhub.ws.bytes_to_upstream"] != "7" {
			t.Fatalf("missing lifecycle data: %v", attrs)
		}
		for _, key := range []string{"http.route", "http.response.status_code", "shinyhub.ws.close_reason"} {
			if _, ok := attrs[key]; ok {
				t.Errorf("unexpected attribute %s", key)
			}
		}
		_ = tp.Shutdown(context.Background())
	}
}

// Close events remain separate traces but follow the handshake's sampling
// decision, even when the current ratio differs after a tracer replacement.
func TestWSCloseSpanSamplingFollowsHandshake(t *testing.T) {
	for _, ratio := range []float64{0, 1} {
		for _, flags := range []trace.TraceFlags{0, trace.FlagsSampled} {
			t.Run(fmt.Sprintf("ratio=%v/flags=%v", ratio, flags), func(t *testing.T) {
				rec := tracetest.NewSpanRecorder()
				tp := sdktrace.NewTracerProvider(
					sdktrace.WithSampler(servertrace.NewSampler(ratio)),
					sdktrace.WithSpanProcessor(rec),
				)
				t.Cleanup(func() { _ = tp.Shutdown(context.Background()) })
				handshake := trace.NewSpanContext(trace.SpanContextConfig{
					TraceID: trace.TraceID{1}, SpanID: trace.SpanID{1}, TraceFlags: flags,
				})
				recordWSCloseSpan(tp.Tracer("test"), handshake, WSSessionEnd{Duration: 24 * time.Hour})
				want := 0
				if handshake.IsSampled() {
					want = 1
				}
				if got := len(rec.Ended()); got != want {
					t.Fatalf("got %d close spans, want %d", got, want)
				}
			})
		}
	}
}
