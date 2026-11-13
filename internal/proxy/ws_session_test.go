package proxy

import (
	"context"
	"encoding/binary"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"go.opentelemetry.io/otel/codes"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"
)

type testWSBody struct{ io.ReadWriteCloser }
type testWSCloseWriter struct{ testWSBody }

func (testWSCloseWriter) CloseWrite() error { return nil }

func TestWSConnectionIDAcceptsOnlyBrowserFormat(t *testing.T) {
	const valid = "0123456789abcdef01234567"
	if got := wsConnectionID(valid); got != valid {
		t.Fatalf("valid ID changed to %q", got)
	}
	pattern := regexp.MustCompile(`^[0-9a-f]{24}$`)
	for _, raw := range []string{"", "short", "0123456789ABCDEF01234567", "0123456789abcdef0123456/"} {
		got := wsConnectionID(raw)
		if got == raw || !pattern.MatchString(got) {
			t.Fatalf("invalid ID %q became %q", raw, got)
		}
	}
	s := &wsSession{}
	s.start("demo", 9, 42, nil)
	if !pattern.MatchString(s.connectionID) {
		t.Fatalf("untagged session ID = %q", s.connectionID)
	}
}

func TestStripWSConnectionIDPreservesOtherQueryBytes(t *testing.T) {
	raw := "signed=a%20b&shinyhub_cid=0123456789abcdef01234567&x=%2f&shinyhub%5fcid=other"
	if got, want := stripWSConnectionID(raw), "signed=a%20b&x=%2f"; got != want {
		t.Fatalf("stripped query = %q, want %q", got, want)
	}
}

func TestObserveWSTunnelPreservesOptionalCloseWrite(t *testing.T) {
	s := &wsSession{}
	req := (&http.Request{}).WithContext(context.WithValue(context.Background(), wsSessionContextKey{}, s))
	for _, tc := range []struct {
		body io.ReadWriteCloser
		want bool
	}{
		{body: testWSBody{}, want: false},
		{body: testWSCloseWriter{}, want: true},
	} {
		resp := &http.Response{StatusCode: http.StatusSwitchingProtocols,
			Header: http.Header{"Upgrade": []string{"websocket"}}, Body: tc.body, Request: req}
		if err := (&Proxy{}).observeWSTunnel(resp); err != nil {
			t.Fatal(err)
		}
		_, has := resp.Body.(interface{ CloseWrite() error })
		if has != tc.want {
			t.Fatalf("CloseWrite present = %t, want %t", has, tc.want)
		}
	}
}

func TestWSSessionCloseFrameAcrossReads(t *testing.T) {
	s := &wsSession{}
	s.start("demo", 9, 42, nil)
	frame := append([]byte{0x88, 0x02 + byte(len("keepalive ping timeout"))}, 0x03, 0xf3)
	frame = append(frame, []byte("keepalive ping timeout")...)
	for _, b := range frame {
		s.observeRead("upstream", []byte{b}, nil)
	}
	s.observeRead("upstream", nil, io.EOF)
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closedBy != "upstream" || s.transportEnd != "upstream" || s.closeCode == nil || *s.closeCode != 1011 || s.closeReason != "keepalive ping timeout" {
		t.Fatalf("close observation = %q, %v, %q", s.closedBy, s.closeCode, s.closeReason)
	}
}

func TestWSSessionMaskedClientCloseAndDrainOverride(t *testing.T) {
	s := &wsSession{}
	s.start("demo", 9, 42, nil)
	mask := [4]byte{1, 2, 3, 4}
	frame := []byte{0x88, 0x82, mask[0], mask[1], mask[2], mask[3]}
	var code [2]byte
	binary.BigEndian.PutUint16(code[:], 1000)
	frame = append(frame, code[0]^mask[0], code[1]^mask[1])
	s.observeRead("client", frame, nil)
	s.setProxyReason("drain") // A later drain must not reattribute the close.
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closedBy != "client" || s.proxyReason != "" || s.closeCode == nil || *s.closeCode != 1000 {
		t.Fatalf("close observation = %q, %q, %v", s.closedBy, s.proxyReason, s.closeCode)
	}
}

func TestTrackedWSSessionEmitsOnceOnForcedDrain(t *testing.T) {
	tr := newConnTracker()
	events := make(chan WSSessionEnd, 2)
	s := &wsSession{}
	s.start("demo", 9, 42, func(e WSSessionEnd) { events <- e })
	c := tr.trackWithSession(&stubConn{}, ConnPrincipal{}, s.end, s)
	tr.closeAll()
	_ = c.Close()
	select {
	case e := <-events:
		if e.ClosedBy != "drain" || e.Abnormal || e.ReplicaIndex != 9 || e.DeploymentID != 42 {
			t.Fatalf("event = %+v", e)
		}
	case <-time.After(time.Second):
		t.Fatal("missing end event")
	}
	select {
	case e := <-events:
		t.Fatalf("duplicate end event: %+v", e)
	default:
	}
}

func TestTrackedWSSessionLateDrainDoesNotChangeNaturalClose(t *testing.T) {
	tr := newConnTracker()
	events := make(chan WSSessionEnd, 2)
	s := &wsSession{}
	s.start("demo", 9, 42, func(e WSSessionEnd) { events <- e })
	s.observeRead("upstream", nil, io.EOF)
	c := tr.trackWithSession(&stubConn{}, ConnPrincipal{}, s.end, s).(*trackedConn)
	_ = c.Close()
	_ = c.closeWithReason("drain")
	e := <-events
	if e.ClosedBy != "unknown" || e.TransportEndSide != "upstream" || e.EndSignal != "transport_end" || !e.Abnormal {
		t.Fatalf("event changed by late drain: %+v", e)
	}
	select {
	case extra := <-events:
		t.Fatalf("duplicate event: %+v", extra)
	default:
	}
}

func TestWSSessionEndWaitsForBackendAndInFlightWrite(t *testing.T) {
	events := make(chan WSSessionEnd, 2)
	s := &wsSession{}
	s.start("demo", 9, 42, func(e WSSessionEnd) { events <- e })
	s.attachBackend()
	s.beginIO()
	s.end()
	s.closeBackend()
	select {
	case e := <-events:
		t.Fatalf("emitted before in-flight write completed: %+v", e)
	default:
	}
	s.finishIO("client", 5, nil, false, nil)
	e := <-events
	if e.BytesToClient != 5 {
		t.Fatalf("bytes to client = %d, want 5", e.BytesToClient)
	}
}

// Exercise classification from observed frames through trace status and burst
// detection, so consumers cannot accidentally disagree about expected endings.
func TestWSSessionEndOutcomes(t *testing.T) {
	for _, tc := range []struct {
		name, side, reason, proxyReason string
		kind                            WSEndKind
		code                            int // Zero means no close frame; -1 means an empty close frame.
		abnormal                        bool
	}{
		{name: "normal", side: "upstream", code: 1000, kind: WSEndNormal},
		{name: "going away", side: "upstream", code: 1001, kind: WSEndNormal},
		{name: "upstream restart", side: "upstream", code: 1012, reason: "redeploy", kind: WSEndRestart},
		{name: "keepalive timeout", side: "upstream", code: 1011, reason: "keepalive ping timeout", kind: WSEndKeepaliveTimeout},
		{name: "internal error", side: "upstream", code: 1011, reason: "internal error", kind: WSEndUpstreamError, abnormal: true},
		{name: "1011 without reason", side: "upstream", code: 1011, kind: WSEndUpstreamError, abnormal: true},
		{name: "similar reason", side: "upstream", code: 1011, reason: "keepalive ping timeout exceeded", kind: WSEndUpstreamError, abnormal: true},
		{name: "reason with NUL suffix", side: "upstream", code: 1011, reason: "keepalive ping timeout\x00", kind: WSEndUpstreamError, abnormal: true},
		{name: "reason with embedded control", side: "upstream", code: 1011, reason: "keepalive\x01 ping timeout", kind: WSEndUpstreamError, abnormal: true},
		{name: "reason with invalid UTF-8", side: "upstream", code: 1011, reason: "keepalive ping timeout\xff", kind: WSEndUpstreamError, abnormal: true},
		{name: "reason with different case", side: "upstream", code: 1011, reason: "Keepalive ping timeout", kind: WSEndUpstreamError, abnormal: true},
		{name: "reason with whitespace", side: "upstream", code: 1011, reason: "keepalive ping timeout ", kind: WSEndUpstreamError, abnormal: true},
		{name: "other code with timeout reason", side: "upstream", code: 1008, reason: "keepalive ping timeout", kind: WSEndUpstreamError, abnormal: true},
		{name: "upstream EOF", side: "upstream", kind: WSEndTransportError, abnormal: true},
		{name: "empty upstream close", side: "upstream", code: -1, kind: WSEndUpstreamError, abnormal: true},
		{name: "client normal", side: "client", code: 1000, kind: WSEndNormal},
		{name: "client error", side: "client", code: 1011, reason: "internal error", kind: WSEndClientGone},
		{name: "client EOF", side: "client", kind: WSEndClientGone},
		{name: "proxy restart", proxyReason: "proxy", code: 1012, kind: WSEndRestart},
		{name: "proxy drain", proxyReason: "drain", kind: WSEndProxyStop},
		{name: "proxy lifetime", proxyReason: "lifetime", kind: WSEndProxyStop},
		{name: "unknown", kind: WSEndUnknown},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := tracetest.NewSpanRecorder()
			tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(rec))
			t.Cleanup(func() { _ = tp.Shutdown(context.Background()) })
			bursts := NewWSAbnormalBurstDetector()
			for i := 0; i < 3; i++ {
				var event WSSessionEnd
				s := &wsSession{}
				s.start("demo", 9, 42, func(e WSSessionEnd) { event = e })
				if tc.proxyReason != "" {
					s.setProxyReason(tc.proxyReason)
					if tc.code > 0 {
						code := uint16(tc.code)
						s.closedBy, s.closeCode = tc.proxyReason, &code
					}
				} else if tc.side != "" {
					if tc.code != 0 {
						var payload []byte
						if tc.code > 0 {
							payload = make([]byte, 2+len(tc.reason))
							binary.BigEndian.PutUint16(payload, uint16(tc.code))
							copy(payload[2:], tc.reason)
						}
						frame := []byte{0x88, byte(len(payload))}
						if tc.side == "client" {
							frame[1] |= 0x80
							mask := []byte{1, 2, 3, 4}
							frame = append(frame, mask...)
							for j := range payload {
								payload[j] ^= mask[j%4]
							}
						}
						frame = append(frame, payload...)
						for _, b := range frame {
							s.observeRead(tc.side, []byte{b}, nil)
						}
					}
					s.observeRead(tc.side, nil, io.EOF)
				}
				s.end()
				if event.EndKind != tc.kind || event.Abnormal != tc.abnormal {
					t.Fatalf("end kind=%q abnormal=%v; want %q %v", event.EndKind, event.Abnormal, tc.kind, tc.abnormal)
				}
				recordWSCloseSpan(tp.Tracer("test"), trace.SpanContext{}, event)
				span := rec.Ended()[i]
				wantStatus := codes.Unset
				if tc.abnormal {
					wantStatus = codes.Error
				}
				if span.Status().Code != wantStatus {
					t.Fatalf("span status=%v, want %v", span.Status().Code, wantStatus)
				}
				attrs := map[string]string{}
				for _, a := range span.Attributes() {
					attrs[string(a.Key)] = a.Value.Emit()
					if tc.reason != "" && strings.Contains(a.Value.Emit(), tc.reason) {
						t.Fatalf("close reason leaked in %s", a.Key)
					}
				}
				if attrs["shinyhub.ws.end_kind"] != string(tc.kind) || attrs["shinyhub.ws.abnormal"] != strconv.FormatBool(tc.abnormal) {
					t.Fatalf("wrong span classification: %v", attrs)
				}
				if _, ok := attrs["shinyhub.ws.close_reason"]; ok {
					t.Fatal("close reason exported")
				}
				_, burst := bursts.Record(event)
				if want := tc.abnormal && i == 2; burst != want {
					t.Fatalf("burst after %d ends=%v, want %v", i+1, burst, want)
				}
			}
		})
	}
}
