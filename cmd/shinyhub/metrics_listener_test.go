package main

import (
	"context"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/rvben/shinyhub/internal/metrics"
)

// TestStartMetricsListener proves the dedicated scrape listener binds the given
// address, serves the Prometheus exposition at /metrics, and shuts down cleanly.
func TestStartMetricsListener(t *testing.T) {
	reg := metrics.New("v-test")
	srv, ln, err := startMetricsListener(net.Listen, "127.0.0.1:0", reg)
	if err != nil {
		t.Fatalf("startMetricsListener: %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = srv.Shutdown(ctx)
	})

	url := "http://" + ln.Addr().String() + "/metrics"
	resp, err := http.Get(url)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /metrics returned %d, want 200", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "shinyhub_build_info") {
		t.Fatalf("scrape body missing shinyhub_build_info:\n%s", body)
	}
}

// TestStartMetricsListener_BadAddrErrors proves a bind failure surfaces as an
// error rather than a silent no-op.
func TestStartMetricsListener_BadAddrErrors(t *testing.T) {
	if _, _, err := startMetricsListener(net.Listen, "not-a-host-port", metrics.New("v")); err == nil {
		t.Fatal("expected an error binding a malformed address")
	}
}

// TestStartMetricsListener_UsesInjectedListener proves the listener is built via
// the injected constructor (so it can be routed through the tableflip upgrader
// for zero-downtime handoff) rather than calling net.Listen directly.
func TestStartMetricsListener_UsesInjectedListener(t *testing.T) {
	used := false
	listen := func(network, addr string) (net.Listener, error) {
		used = true
		return net.Listen(network, addr)
	}
	srv, ln, err := startMetricsListener(listen, "127.0.0.1:0", metrics.New("test"))
	if err != nil {
		t.Fatalf("startMetricsListener: %v", err)
	}
	defer srv.Close()
	if !used {
		t.Fatal("startMetricsListener must use the injected listen function")
	}
	resp, err := http.Get("http://" + ln.Addr().String() + "/metrics")
	if err != nil {
		t.Fatalf("GET /metrics: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("/metrics status = %d, want 200", resp.StatusCode)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
}
