// internal/worker/dialer_cache_test.go
package worker

import (
	"crypto/tls"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rvben/shinyhub/internal/db"
)

// startCountingMTLSServer starts a bare TLS+HTTP/1.1 server whose certificate
// is signed by ca and carries the DNS SAN the dialer's tlsConfig expects for
// nodeID (<nodeID>.node.shinyhub.internal), counting every completed TLS
// handshake via VerifyConnection. The caller must call the returned stop func.
func startCountingMTLSServer(t *testing.T, ca *CA, nodeID string) (addr string, handshakes *int32, stop func()) {
	t.Helper()

	serverCert, err := ca.ServerCertificate(nodeID + nodeIDSANSuffix)
	if err != nil {
		t.Fatalf("server cert: %v", err)
	}

	var count int32
	tlsCfg := &tls.Config{
		Certificates: []tls.Certificate{serverCert},
		MinVersion:   tls.VersionTLS12,
		VerifyConnection: func(tls.ConnectionState) error {
			atomic.AddInt32(&count, 1)
			return nil
		},
	}

	ln, err := tls.Listen("tcp", "127.0.0.1:0", tlsCfg)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/ping", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	srv := &http.Server{Handler: mux}
	go func() { _ = srv.Serve(ln) }()

	return ln.Addr().String(), &count, func() { _ = srv.Close() }
}

// TestMTLSDialer_DialWorkerReusesTransportAcrossCalls asserts that repeated
// DialWorker calls for the same worker share one underlying *http.Transport,
// so its connection pool is actually reused instead of every call paying for
// a fresh TCP dial and TLS handshake. Before the fix, DialWorker built a
// brand-new *http.Transport (and therefore a brand-new connection pool) on
// every call, so three sequential requests to the same worker performed three
// TLS handshakes even though nothing about the worker had changed between
// them.
func TestMTLSDialer_DialWorkerReusesTransportAcrossCalls(t *testing.T) {
	ca, err := OpenCA(t.TempDir(), nil)
	if err != nil {
		t.Fatalf("open ca: %v", err)
	}

	addr, handshakes, stop := startCountingMTLSServer(t, ca, "node-a")
	defer stop()

	mint := func() (tls.Certificate, error) { return ca.ControlClientCertificate() }
	dialer, err := NewMTLSDialer(mint, ca.Pool())
	if err != nil {
		t.Fatalf("new mtls dialer: %v", err)
	}
	w := db.Worker{NodeID: "node-a", AdvertiseAddr: addr}

	var transports []http.RoundTripper
	for i := 0; i < 3; i++ {
		client, base, err := dialer.DialWorker(w)
		if err != nil {
			t.Fatalf("DialWorker call %d: %v", i, err)
		}
		transports = append(transports, client.Transport)

		req, err := http.NewRequest(http.MethodGet, base+"/ping", nil)
		if err != nil {
			t.Fatalf("build request %d: %v", i, err)
		}
		resp, err := client.Do(req)
		if err != nil {
			t.Fatalf("request %d: %v", i, err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("request %d: status = %d, want 200", i, resp.StatusCode)
		}
	}

	for i := 1; i < len(transports); i++ {
		if transports[i] != transports[0] {
			t.Errorf("DialWorker call %d returned a different *http.Transport than call 0; want the same pointer reused", i)
		}
	}

	if got := atomic.LoadInt32(handshakes); got != 1 {
		t.Errorf("TLS handshakes = %d, want 1 (transport was rebuilt instead of reused across calls, so the connection pool was thrown away every time)", got)
	}
}

// TestMTLSDialer_DialWorkerAddressChangeRetiresTransport asserts that when a
// worker re-registers at a new advertise address under the same node id (an
// agent restart behind a new IP, or a rescheduled container), DialWorker
// stops handing out the transport cached for the old address instead of
// reusing it forever. This guards against a cache keyed only by node id: such
// a cache would keep returning the stale transport, silently leaving its idle
// connections pinned to an address that no longer belongs to this worker's
// current identity. A stable address across repeated calls must still reuse
// one transport.
func TestMTLSDialer_DialWorkerAddressChangeRetiresTransport(t *testing.T) {
	mint := func() (tls.Certificate, error) {
		return selfSignedCert(t, time.Now().Add(-time.Minute), time.Now().Add(time.Hour)), nil
	}
	dialer, err := NewMTLSDialer(mint, nil)
	if err != nil {
		t.Fatalf("new mtls dialer: %v", err)
	}

	wOld := db.Worker{NodeID: "node-a", AdvertiseAddr: "192.0.2.10:8443"}
	wNew := db.Worker{NodeID: "node-a", AdvertiseAddr: "192.0.2.11:8443"}

	c1, _, err := dialer.DialWorker(wOld)
	if err != nil {
		t.Fatalf("DialWorker (old address): %v", err)
	}
	c2, _, err := dialer.DialWorker(wNew)
	if err != nil {
		t.Fatalf("DialWorker (new address): %v", err)
	}
	if c1.Transport == c2.Transport {
		t.Error("DialWorker reused the transport cached for the old advertise address after the worker re-registered at a new one")
	}

	// The address is otherwise stable across repeated calls: a third call at
	// the new address must reuse c2's transport, not build yet another one.
	c3, _, err := dialer.DialWorker(wNew)
	if err != nil {
		t.Fatalf("DialWorker (new address, second call): %v", err)
	}
	if c3.Transport != c2.Transport {
		t.Error("DialWorker rebuilt the transport for an unchanged worker identity")
	}
}

// newCacheTestDialer returns an mtlsDialer with a controllable clock, for
// tests of cache bookkeeping that never need a live worker (DialWorker only
// builds a client; it does not connect).
func newCacheTestDialer(t *testing.T) (*mtlsDialer, *time.Time) {
	t.Helper()
	ca, err := OpenCA(t.TempDir(), nil)
	if err != nil {
		t.Fatalf("open ca: %v", err)
	}
	mint := func() (tls.Certificate, error) { return ca.ControlClientCertificate() }
	ad, err := NewMTLSDialer(mint, ca.Pool())
	if err != nil {
		t.Fatalf("new mtls dialer: %v", err)
	}
	d := ad.(*mtlsDialer)
	clock := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	d.now = func() time.Time { return clock }
	return d, &clock
}

func cachedNodeIDs(d *mtlsDialer) map[string]bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	ids := make(map[string]bool, len(d.cache))
	for id := range d.cache {
		ids[id] = true
	}
	return ids
}

// TestMTLSDialer_EvictsTransportsOfDepartedWorkers asserts that the transport
// cache does not grow with every node ID ever dialed: a worker that stops
// being dialed for transportCacheIdleTTL is evicted by the next cache miss,
// while a worker still in use inside that window keeps its transport.
func TestMTLSDialer_EvictsTransportsOfDepartedWorkers(t *testing.T) {
	d, clock := newCacheTestDialer(t)
	dial := func(id string) http.RoundTripper {
		t.Helper()
		c, _, err := d.DialWorker(db.Worker{NodeID: id, AdvertiseAddr: "192.0.2.10:9443"})
		if err != nil {
			t.Fatalf("DialWorker %s: %v", id, err)
		}
		return c.Transport
	}

	dial("departed")
	activeTr := dial("active")

	*clock = clock.Add(transportCacheIdleTTL / 2)
	dial("active") // still in use: refreshes its last-used time
	*clock = clock.Add(transportCacheIdleTTL/2 + time.Second)

	dial("newcomer") // a cache miss sweeps idle entries
	got := cachedNodeIDs(d)
	if got["departed"] {
		t.Errorf("transport for a worker idle past the TTL is still cached: %v", got)
	}
	if !got["active"] || !got["newcomer"] {
		t.Errorf("cache = %v, want the active worker and the newcomer kept", got)
	}
	if dial("active") != activeTr {
		t.Error("the active worker's transport was rebuilt; want it reused")
	}
}

// TestMTLSDialer_RevokedWorkerDropsCachedTransport asserts that once a worker
// is revoked, its cached transport is dropped rather than kept alive for a
// peer the dialer will never address again.
func TestMTLSDialer_RevokedWorkerDropsCachedTransport(t *testing.T) {
	d, _ := newCacheTestDialer(t)
	w := db.Worker{NodeID: "node-r", AdvertiseAddr: "192.0.2.11:9443"}
	if _, _, err := d.DialWorker(w); err != nil {
		t.Fatalf("DialWorker: %v", err)
	}
	w.RevokedAt = "2026-01-01T00:00:00Z"
	if _, _, err := d.DialWorker(w); err == nil {
		t.Fatal("DialWorker on a revoked worker succeeded; want an error")
	}
	if cachedNodeIDs(d)["node-r"] {
		t.Error("revoked worker's transport is still cached")
	}
}
