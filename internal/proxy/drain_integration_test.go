package proxy

import (
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// hijackingHandler upgrades every request: it hijacks the connection, registers
// it via the tracker exactly as Proxy.ServeHTTP does (statusRecorder with a
// trackHijack hook), and holds the conn until the client closes it (or it is
// force-closed). This exercises the real hijack path through statusRecorder.
func hijackingHandler(tr *connTracker, held chan<- net.Conn) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		rec := newStatusRecorder(w)
		rec.trackHijack = tr.track
		conn, _, err := rec.Hijack()
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		held <- conn
		// Block until the conn is closed by either side; a 1-byte read returns
		// on EOF/close. The hijacked goroutine lives for the connection's life.
		buf := make([]byte, 1)
		_, _ = conn.Read(buf)
	}
}

func TestDrainIntegration_ForceCloseStraggler(t *testing.T) {
	tr := newConnTracker()
	held := make(chan net.Conn, 1)
	srv := httptest.NewServer(hijackingHandler(tr, held))
	defer srv.Close()

	conn, err := net.Dial("tcp", srv.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if _, err := conn.Write([]byte("GET / HTTP/1.1\r\nHost: x\r\n\r\n")); err != nil {
		t.Fatal(err)
	}
	<-held // handler has hijacked + registered the conn

	if tr.count() != 1 {
		t.Fatalf("tracked count = %d, want 1", tr.count())
	}
	p := &Proxy{conns: tr}
	start := time.Now()
	forced := p.DrainUpgraded(200 * time.Millisecond)
	if forced != 1 {
		t.Fatalf("forced = %d, want 1", forced)
	}
	if time.Since(start) < 190*time.Millisecond {
		t.Fatal("drain must wait ~the full timeout before force-closing a straggler")
	}
	if tr.count() != 0 {
		t.Fatalf("count after force-close = %d, want 0", tr.count())
	}
}

func TestDrainIntegration_ClientCloseDrainsCleanly(t *testing.T) {
	tr := newConnTracker()
	held := make(chan net.Conn, 1)
	srv := httptest.NewServer(hijackingHandler(tr, held))
	defer srv.Close()

	conn, err := net.Dial("tcp", srv.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Write([]byte("GET / HTTP/1.1\r\nHost: x\r\n\r\n")); err != nil {
		t.Fatal(err)
	}
	serverConn := <-held
	if tr.count() != 1 {
		t.Fatalf("tracked count = %d, want 1", tr.count())
	}

	// Client closes; mirror the reverse proxy closing the server-side conn when
	// the upgrade copy ends, which unregisters it via trackedConn.Close.
	conn.Close()
	time.Sleep(20 * time.Millisecond)
	serverConn.Close()

	p := &Proxy{conns: tr}
	if forced := p.DrainUpgraded(2 * time.Second); forced != 0 {
		t.Fatalf("forced = %d, want 0 (conn closed by client)", forced)
	}
	if tr.count() != 0 {
		t.Fatalf("count after clean drain = %d, want 0", tr.count())
	}
}
