package proxy

import (
	"bufio"
	"net"
	"net/http"
	"testing"
)

// hijackableRecorder is an http.ResponseWriter that also satisfies
// http.Hijacker, returning a stub conn so we can exercise statusRecorder.Hijack.
type hijackableRecorder struct {
	http.ResponseWriter
	conn net.Conn
}

func (h *hijackableRecorder) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	return h.conn, nil, nil
}

func TestStatusRecorder_HijackInvokesTrackHook(t *testing.T) {
	tr := newConnTracker()
	raw := &stubConn{}
	hr := &hijackableRecorder{conn: raw}
	rec := newStatusRecorder(hr)
	rec.trackHijack = tr.track

	got, _, err := rec.Hijack()
	if err != nil {
		t.Fatalf("Hijack: %v", err)
	}
	if tr.count() != 1 {
		t.Fatalf("tracker count after hijack = %d, want 1", tr.count())
	}
	if _, ok := got.(*trackedConn); !ok {
		t.Fatalf("Hijack returned %T, want *trackedConn", got)
	}
	got.Close()
	if tr.count() != 0 {
		t.Fatalf("tracker count after close = %d, want 0", tr.count())
	}
	if !raw.isClosed() {
		t.Fatal("closing the tracked conn must close the underlying conn")
	}
}

func TestStatusRecorder_HijackWithoutHookIsUnchanged(t *testing.T) {
	raw := &stubConn{}
	hr := &hijackableRecorder{conn: raw}
	rec := newStatusRecorder(hr) // no trackHijack set
	got, _, err := rec.Hijack()
	if err != nil {
		t.Fatalf("Hijack: %v", err)
	}
	if _, ok := got.(*trackedConn); ok {
		t.Fatal("without a hook, Hijack must return the raw conn untracked")
	}
}
