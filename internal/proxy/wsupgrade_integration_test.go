package proxy_test

import (
	"bufio"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/rvben/shinyhub/internal/proxy"
)

// wsUpgradingBackend hijacks the raw connection and performs a genuine 101
// Switching Protocols handshake, then echoes one line, exactly like a real
// WebSocket backend (e.g. a Shiny app). It intentionally does NOT go through
// http.ResponseWriter.WriteHeader — real WS servers write the response line
// on the hijacked bufio.Writer, matching what httputil.ReverseProxy itself
// does on the proxy side (see handleUpgradeResponse in the stdlib).
func wsUpgradingBackend(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hj, ok := w.(http.Hijacker)
		if !ok {
			http.Error(w, "no hijack", http.StatusInternalServerError)
			return
		}
		conn, buf, err := hj.Hijack()
		if err != nil {
			t.Errorf("backend hijack: %v", err)
			return
		}
		defer conn.Close()
		if _, err := buf.WriteString("HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\n\r\n"); err != nil {
			t.Errorf("backend write status line: %v", err)
			return
		}
		if err := buf.Flush(); err != nil {
			t.Errorf("backend flush: %v", err)
			return
		}
		line, err := buf.ReadString('\n')
		if err != nil {
			return
		}
		if _, err := buf.WriteString("echo:" + line); err != nil {
			return
		}
		_ = buf.Flush()
	}))
}

// TestProxy_RealReverseProxyUpgrade_MarksWSReady drives a genuine WebSocket
// upgrade through the PRODUCTION reverse-proxy path (Proxy.ServeHTTP ->
// httputil.ReverseProxy, exactly as used for real traffic), not a direct
// rec.WriteHeader(101) call. On this Go toolchain, httputil.ReverseProxy's
// upgrade handling (net/http/httputil.handleUpgradeResponse) hijacks the
// connection FIRST and writes the 101 status line straight to the hijacked
// bufio.Writer afterwards — it never calls the wrapped ResponseWriter's
// WriteHeader(101). So a hook wired only to WriteHeader(101) (the pre-fix
// wiring) never observes a real upgrade, and IsWSReady stays false forever
// even though the WS tunnel itself works fine. This test proves the fix:
// IsWSReady must flip to true once a real upgrade completes.
func TestProxy_RealReverseProxyUpgrade_MarksWSReady(t *testing.T) {
	backend := wsUpgradingBackend(t)
	defer backend.Close()

	p := proxy.New()
	if err := p.Register("demo", backend.URL); err != nil {
		t.Fatalf("Register: %v", err)
	}

	front := httptest.NewServer(p)
	defer front.Close()

	if p.IsWSReady("demo") {
		t.Fatal("precondition: IsWSReady must be false before any upgrade")
	}

	conn, err := net.DialTimeout("tcp", front.Listener.Addr().String(), 2*time.Second)
	if err != nil {
		t.Fatalf("dial front: %v", err)
	}
	defer conn.Close()

	req := "GET /app/demo/ws HTTP/1.1\r\nHost: x\r\nConnection: Upgrade\r\nUpgrade: websocket\r\n\r\n"
	if _, err := conn.Write([]byte(req)); err != nil {
		t.Fatalf("write request: %v", err)
	}

	reader := bufio.NewReader(conn)
	statusLine, err := reader.ReadString('\n')
	if err != nil {
		t.Fatalf("reading status line: %v", err)
	}
	if statusLine != "HTTP/1.1 101 Switching Protocols\r\n" {
		t.Fatalf("status line = %q, want 101 Switching Protocols", statusLine)
	}
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			t.Fatalf("reading headers: %v", err)
		}
		if line == "\r\n" {
			break
		}
	}

	// Prove the tunnel actually carries traffic, so this is a real upgrade
	// and not just a status line.
	if _, err := conn.Write([]byte("hello\n")); err != nil {
		t.Fatalf("write payload: %v", err)
	}
	echo, err := reader.ReadString('\n')
	if err != nil {
		t.Fatalf("reading echo: %v", err)
	}
	if echo != "echo:hello\n" {
		t.Fatalf("echo = %q, want echo:hello", echo)
	}

	if !p.IsWSReady("demo") {
		t.Fatal("IsWSReady = false after a real WS upgrade completed; MarkWSReady was never invoked")
	}
}
