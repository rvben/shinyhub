package proxy

import (
	"context"
	"testing"

	"go.opentelemetry.io/otel/codes"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

func TestWSSpanRecordsLifecycleWithoutHTTPLatencyOrCloseText(t *testing.T) {
	for _, abnormal := range []bool{false, true} {
		rec := tracetest.NewSpanRecorder()
		tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(rec))
		_, span := tp.Tracer("test").Start(context.Background(), "WS /app/{slug}")
		code := uint16(1000)
		endWSSpan(span, WSSessionEnd{Slug: "app", ReplicaIndex: 2, DeploymentID: 17, CloseCode: &code, CloseReason: "sensitive user-controlled message", Abnormal: abnormal, BytesToClient: 42, BytesToUpstream: 7, ClosedBy: "client"})
		ended := rec.Ended()[0]
		if (ended.Status().Code == codes.Error) != abnormal {
			t.Fatalf("abnormal=%v status=%v", abnormal, ended.Status())
		}
		attrs := map[string]string{}
		for _, kv := range ended.Attributes() {
			attrs[string(kv.Key)] = kv.Value.Emit()
		}
		if attrs["shinyhub.ws.close_code"] != "1000" || attrs["shinyhub.ws.bytes_to_client"] != "42" || attrs["shinyhub.ws.bytes_to_upstream"] != "7" {
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
