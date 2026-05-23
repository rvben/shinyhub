package main

import (
	"context"
	"io"
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
	srv, ln, err := startMetricsListener("127.0.0.1:0", reg)
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
	if _, _, err := startMetricsListener("not-a-host-port", metrics.New("v")); err == nil {
		t.Fatal("expected an error binding a malformed address")
	}
}
