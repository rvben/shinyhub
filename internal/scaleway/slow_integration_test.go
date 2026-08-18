//go:build integration && slow

package scaleway

import (
	"net"
	"os"
	"testing"
	"time"

	"golang.org/x/net/websocket"
)

// TestSlowIntegrationScaleToZeroWake proves the real provider behavior that
// unit fakes cannot: after Scaleway's documented 15-minute idle window, the
// retained private endpoint wakes a new instance and remains routable.
func TestSlowIntegrationScaleToZeroWake(t *testing.T) {
	if os.Getenv("SHINYHUB_TEST_SCALEWAY_LONG") != "1" {
		t.Skip("set SHINYHUB_TEST_SCALEWAY_LONG=1 to run the billed 16-minute test")
	}
	t.Parallel()
	h := newIntegrationHarness(t)
	h.start(t)
	h.probeHTTP(t)
	t.Log("waiting past the provider's 15-minute scale-to-zero window")
	time.Sleep(16 * time.Minute)
	h.probeHTTP(t)
	h.probeWebSocket(t)
}

// TestSlowIntegrationWebSocketDeadlineAndReconnect pins the other important
// serverless edge: Scaleway caps one HTTP request at 60 minutes. A WebSocket is
// expected to be closed by the provider, after which a fresh connection must
// succeed. This is intentionally real-provider-only because mocks cannot prove
// proxy upgrade support or the managed edge's timeout behavior.
func TestSlowIntegrationWebSocketDeadlineAndReconnect(t *testing.T) {
	if os.Getenv("SHINYHUB_TEST_SCALEWAY_LONG") != "1" {
		t.Skip("set SHINYHUB_TEST_SCALEWAY_LONG=1 to run the billed 62-minute test")
	}
	t.Parallel()
	h := newIntegrationHarness(t)
	h.start(t)
	h.probeHTTP(t)
	conn := h.dialWebSocket(t)
	defer conn.Close()
	if err := conn.SetDeadline(time.Now().Add(62 * time.Minute)); err != nil {
		t.Fatalf("set WebSocket deadline: %v", err)
	}
	var reply string
	err := websocket.Message.Receive(conn, &reply)
	if err == nil {
		t.Fatalf("WebSocket unexpectedly received %q instead of closing at the provider request limit", reply)
	}
	if netErr, ok := err.(net.Error); ok && netErr.Timeout() {
		t.Fatalf("WebSocket remained open beyond the expected provider limit: %v", err)
	}
	h.probeWebSocket(t)
}
