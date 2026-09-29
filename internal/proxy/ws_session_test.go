package proxy

import (
	"context"
	"encoding/binary"
	"io"
	"net/http"
	"regexp"
	"testing"
	"time"
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
