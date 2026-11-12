package proxy

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

// Every replica is its own host, so a busy server holds idle connections to
// many hosts at once. A cap on the total across hosts evicts connections that
// are about to be reused, and each eviction is a fresh dial on the next
// request to that replica. Reuse must therefore hold for more backends than
// net/http's default total of 100 idle connections.
func TestBackendTransport_ReusesConnectionsAcrossManyBackends(t *testing.T) {
	const backends = 130

	tr := newBackendTransport()
	defer tr.CloseIdleConnections()
	var dials atomic.Int64
	dialer := &net.Dialer{}
	tr.DialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
		dials.Add(1)
		return dialer.DialContext(ctx, network, addr)
	}

	urls := make([]string, backends)
	for i := range urls {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte("ok"))
		}))
		defer srv.Close()
		urls[i] = srv.URL
	}

	client := &http.Client{Transport: tr}
	sweep := func() int64 {
		before := dials.Load()
		for _, u := range urls {
			resp, err := client.Get(u)
			if err != nil {
				t.Fatal(err)
			}
			_, _ = io.Copy(io.Discard, resp.Body)
			_ = resp.Body.Close()
		}
		return dials.Load() - before
	}

	if got := sweep(); got != backends {
		t.Fatalf("first sweep dialed %d times, want one dial per backend (%d); the dial counter is not observing this transport", got, backends)
	}
	if got := sweep(); got != 0 {
		t.Fatalf("second sweep over %d backends dialed %d new connections, want 0: idle connections to other backends are being evicted", backends, got)
	}

	// With no total cap, what bounds idle connections is the per-host cap and
	// the idle timeout, so both must stay set.
	if tr.MaxIdleConnsPerHost <= 0 {
		t.Errorf("MaxIdleConnsPerHost = %d, want a positive per-backend cap", tr.MaxIdleConnsPerHost)
	}
	if tr.IdleConnTimeout <= 0 {
		t.Errorf("IdleConnTimeout = %v, want > 0 so idle backend connections are eventually closed", tr.IdleConnTimeout)
	}
}
