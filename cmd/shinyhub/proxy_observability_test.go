package main

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/rvben/shinyhub/internal/dbtest"
	"github.com/rvben/shinyhub/internal/metrics"
	"github.com/rvben/shinyhub/internal/proxy"
	"github.com/rvben/shinyhub/internal/tracing"
)

// jsonLog captures structured log records so a test can assert on the
// message, level and attributes an operator would see.
type jsonLog struct{ buf bytes.Buffer }

func (l *jsonLog) logger() *slog.Logger {
	return slog.New(slog.NewJSONHandler(&l.buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
}

func (l *jsonLog) records(t *testing.T, msg string) []map[string]any {
	t.Helper()
	var out []map[string]any
	dec := json.NewDecoder(bytes.NewReader(l.buf.Bytes()))
	for {
		var rec map[string]any
		if err := dec.Decode(&rec); err == io.EOF {
			return out
		} else if err != nil {
			t.Fatalf("decode log record: %v", err)
		}
		if rec["msg"] == msg {
			out = append(out, rec)
		}
	}
}

// A missing row means the slug is unknown, so the proxy answers 404. A failed
// lookup is not the same fact: it must surface as an error so the visitor
// gets the loading page instead of being told a real app was deleted.
func TestProxySlugExistsSeparatesMissingFromFailedLookups(t *testing.T) {
	store := dbtest.New(t)
	createTestApp(t, store, "alpha")
	exists := proxySlugExists(store, slog.New(slog.NewTextHandler(io.Discard, nil)))

	if ok, err := exists("alpha"); !ok || err != nil {
		t.Fatalf("existing app: (%v, %v), want (true, nil)", ok, err)
	}
	if ok, err := exists("missing"); ok || err != nil {
		t.Fatalf("unknown slug: (%v, %v), want (false, nil)", ok, err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	if ok, err := exists("alpha"); ok || err == nil {
		t.Fatalf("failed lookup: (%v, %v), want (false, error) so the app is not reported deleted", ok, err)
	}
}

func TestProxyAccessLoggerAddsRoutingDetailOnlyWhenPresent(t *testing.T) {
	var logs jsonLog
	logAccess := proxyAccessLogger(logs.logger())

	logAccess(proxy.AccessLogEntry{Slug: "alpha", Method: "GET", Path: "/", Status: 404, ReplicaIndex: -1, Reject: proxy.ReasonUnknownSlug})
	logAccess(proxy.AccessLogEntry{Slug: "alpha", Method: "GET", Path: "/x", Status: 200, Duration: 1500 * time.Millisecond,
		ClientIP: "198.51.100.7", Peer: "192.0.2.1:5000", ReplicaIndex: 2, Sticky: true, Fallback: true, FallbackReason: "wait-page"})

	recs := logs.records(t, "proxy_access")
	if len(recs) != 2 {
		t.Fatalf("got %d proxy_access records, want 2", len(recs))
	}
	rejected, routed := recs[0], recs[1]
	if rejected["reject"] != "unknown-slug" || rejected["status"] != float64(404) {
		t.Errorf("rejected request: %v; want status 404 and reject unknown-slug", rejected)
	}
	for _, key := range []string{"replica", "sticky", "fallback", "fallback_reason"} {
		if _, ok := rejected[key]; ok {
			t.Errorf("a request no replica served logged %q: %v", key, rejected)
		}
	}
	if routed["replica"] != float64(2) || routed["sticky"] != true || routed["fallback"] != true ||
		routed["fallback_reason"] != "wait-page" || routed["duration_ms"] != float64(1500) ||
		routed["client_ip"] != "198.51.100.7" || routed["peer"] != "192.0.2.1:5000" {
		t.Errorf("routed request: %v; want replica, stickiness, fallback, duration and both addresses", routed)
	}
	if _, ok := routed["reject"]; ok {
		t.Errorf("a routed request logged a reject reason: %v", routed)
	}
}

// Abnormal WebSocket endings are warnings and are counted; once three hit one
// replica generation inside the window, a single burst is reported and
// counted too.
func TestWSSessionEndRecorderReportsAbnormalBursts(t *testing.T) {
	var logs jsonLog
	reg := metrics.New("test")
	record := wsSessionEndRecorder(logs.logger(), reg, proxy.NewWSAbnormalBurstDetector())

	code := uint16(1011)
	record(proxy.WSSessionEnd{Slug: "alpha", ReplicaIndex: 0, DeploymentID: 7, ClosedBy: "client", TransportEndSide: "client", Duration: 6 * time.Hour})
	for range 3 {
		record(proxy.WSSessionEnd{Slug: "alpha", ReplicaIndex: 0, DeploymentID: 7, ClosedBy: "upstream", TransportEndSide: "upstream",
			Abnormal: true, CloseCode: &code, CloseReason: "internal error"})
	}

	ends := logs.records(t, "ws_session_end")
	if len(ends) != 4 {
		t.Fatalf("got %d ws_session_end records, want 4", len(ends))
	}
	if ends[0]["level"] != "INFO" || ends[1]["level"] != "WARN" {
		t.Errorf("levels %v, %v; want a normal end at INFO and an abnormal one at WARN", ends[0]["level"], ends[1]["level"])
	}
	if ends[1]["close_code"] != float64(1011) {
		t.Errorf("abnormal end %v; want its close code", ends[1])
	}
	if _, ok := ends[0]["close_code"]; ok {
		t.Errorf("an end without a close frame logged a close code: %v", ends[0])
	}
	bursts := logs.records(t, "ws_abnormal_burst")
	if len(bursts) != 1 || bursts[0]["count"] != float64(3) || bursts[0]["deployment_id"] != float64(7) || bursts[0]["close_code"] != float64(1011) {
		t.Fatalf("bursts %v; want one burst of 3 on deployment 7 with the shared close code", bursts)
	}

	if strings.Contains(logs.buf.String(), "internal error") || strings.Contains(logs.buf.String(), "close_reason") {
		t.Fatal("app-controlled close text must not appear in session or burst logs")
	}

	rec := httptest.NewRecorder()
	reg.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/metrics", nil))
	scrape := rec.Body.String()
	for _, want := range []string{
		`shinyhub_ws_session_ends_total{abnormal="true",closed_by="upstream",slug="alpha",transport_end_side="upstream"} 3`,
		`shinyhub_ws_session_ends_total{abnormal="false",closed_by="client",slug="alpha",transport_end_side="client"} 1`,
		`shinyhub_ws_abnormal_bursts_total{slug="alpha"} 1`,
		`shinyhub_ws_session_duration_seconds_sum{slug="alpha"} 21600`,
		`shinyhub_ws_session_duration_seconds_count{slug="alpha"} 4`,
	} {
		if !strings.Contains(scrape, want) {
			t.Errorf("scrape lacks %s", want)
		}
	}
}

// With metrics disabled the registry is nil; endings are still logged.
func TestWSSessionEndRecorderLogsWithoutMetrics(t *testing.T) {
	var logs jsonLog
	record := wsSessionEndRecorder(logs.logger(), nil, proxy.NewWSAbnormalBurstDetector())
	for range 3 {
		record(proxy.WSSessionEnd{Slug: "alpha", ReplicaIndex: 0, Abnormal: true})
	}
	if n := len(logs.records(t, "ws_session_end")); n != 3 {
		t.Fatalf("got %d ws_session_end records, want 3", n)
	}
	if n := len(logs.records(t, "ws_abnormal_burst")); n != 1 {
		t.Fatalf("got %d ws_abnormal_burst records, want 1", n)
	}
}

func TestLifecycleLogCorrelation(t *testing.T) {
	c := tracing.Correlation{TraceID: "0af7651916cd43dd8448eb211c80319c", SpanID: "b7ad6b7169203331", Sampled: false}
	var logs jsonLog
	proxyAccessLogger(logs.logger())(proxy.AccessLogEntry{Correlation: c, DeploymentID: 7})
	wsSessionEndRecorder(logs.logger(), nil, proxy.NewWSAbnormalBurstDetector())(proxy.WSSessionEnd{Correlation: c, HandshakeCorrelation: c})
	for _, msg := range []string{"proxy_access", "ws_session_end"} {
		rec := logs.records(t, msg)[0]
		if rec["trace_id"] != c.TraceID || rec["span_id"] != c.SpanID || rec["trace_sampled"] != false {
			t.Fatalf("%s correlation: %v", msg, rec)
		}
	}
	ws := logs.records(t, "ws_session_end")[0]
	if ws["handshake_trace_id"] != c.TraceID || ws["handshake_span_id"] != c.SpanID || ws["handshake_trace_sampled"] != false {
		t.Fatalf("handshake correlation: %v", ws)
	}
	if logs.records(t, "proxy_access")[0]["deployment_id"] != float64(7) {
		t.Fatal("missing deployment identity")
	}
}
