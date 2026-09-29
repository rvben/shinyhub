package proxy_test

import (
	"bufio"
	"encoding/binary"
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

func TestProxy_WSSessionEndFromRealTunnel(t *testing.T) {
	for _, tc := range []struct {
		name, wantSide string
		wantCode       uint16
		clientCloses   bool
		compressed     bool
		customPath     bool
	}{
		{name: "upstream EOF", wantSide: "unknown"},
		{name: "upstream 1011", wantSide: "upstream", wantCode: 1011},
		{name: "upstream 1011 with deflate", wantSide: "upstream", wantCode: 1011, compressed: true},
		{name: "client 1000", wantSide: "client", wantCode: 1000, clientCloses: true},
		{name: "other app socket preserves query", wantSide: "unknown", customPath: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			backendQuery := make(chan string, 1)
			backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				backendQuery <- r.URL.RawQuery
				conn, buf, err := w.(http.Hijacker).Hijack()
				if err != nil {
					t.Errorf("backend hijack: %v", err)
					return
				}
				defer conn.Close()
				_, _ = buf.WriteString("HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\n")
				if tc.compressed {
					_, _ = buf.WriteString("Sec-WebSocket-Extensions: permessage-deflate\r\n")
				}
				_, _ = buf.WriteString("\r\n")
				if err := buf.Flush(); err != nil {
					return
				}
				if tc.clientCloses {
					var data [8]byte
					_, _ = buf.Read(data[:])
					return
				}
				if tc.wantCode != 0 {
					var frame [4]byte
					frame[0], frame[1] = 0x88, 2
					binary.BigEndian.PutUint16(frame[2:], tc.wantCode)
					_, _ = buf.Write(frame[:])
					_ = buf.Flush()
				}
			}))
			defer backend.Close()
			p := proxy.New()
			if err := p.Register("demo", backend.URL); err != nil {
				t.Fatal(err)
			}
			events := make(chan proxy.WSSessionEnd, 2)
			p.SetWSSessionEndRecorder(func(e proxy.WSSessionEnd) { events <- e })
			front := httptest.NewServer(p)
			defer front.Close()
			conn, err := net.DialTimeout("tcp", front.Listener.Addr().String(), time.Second)
			if err != nil {
				t.Fatal(err)
			}
			defer conn.Close()
			path := "/app/demo/websocket/"
			if tc.customPath {
				path = "/app/demo/custom-socket"
			}
			req := "GET " + path + "?keep=1&shinyhub_cid=0123456789abcdef01234567 HTTP/1.1\r\nHost: x\r\nConnection: Upgrade\r\nUpgrade: websocket\r\n"
			if tc.compressed {
				req += "Sec-WebSocket-Extensions: permessage-deflate\r\n"
			}
			_, _ = conn.Write([]byte(req + "\r\n"))
			reader := bufio.NewReader(conn)
			if line, err := reader.ReadString('\n'); err != nil || line != "HTTP/1.1 101 Switching Protocols\r\n" {
				t.Fatalf("upgrade = %q, %v", line, err)
			}
			for {
				line, err := reader.ReadString('\n')
				if err != nil {
					t.Fatal(err)
				}
				if line == "\r\n" {
					break
				}
			}
			if tc.clientCloses {
				// 1000, masked as all browser-to-server frames must be.
				_, _ = conn.Write([]byte{0x88, 0x82, 1, 2, 3, 4, 0x03 ^ 1, 0xe8 ^ 2})
			} else if tc.wantCode != 0 {
				var frame [4]byte
				if _, err := reader.Read(frame[:]); err != nil {
					t.Fatal(err)
				}
			}
			select {
			case e := <-events:
				if !tc.customPath && e.ConnectionID != "0123456789abcdef01234567" || tc.customPath && (len(e.ConnectionID) != 24 || e.ConnectionID == "0123456789abcdef01234567") {
					t.Fatalf("connection ID = %q", e.ConnectionID)
				}
				wantQuery := "keep=1"
				if tc.customPath {
					wantQuery += "&shinyhub_cid=0123456789abcdef01234567"
				}
				if got := <-backendQuery; got != wantQuery {
					t.Fatalf("backend query = %q, want %q", got, wantQuery)
				}
				if e.ClosedBy != tc.wantSide || (tc.wantCode == 0) != (e.CloseCode == nil) {
					t.Fatalf("event = %+v", e)
				}
				if tc.wantCode == 0 && (e.TransportEndSide != "upstream" || !e.Abnormal) {
					t.Fatalf("unframed upstream end = %+v", e)
				}
				if tc.wantCode != 0 && *e.CloseCode != tc.wantCode {
					t.Fatalf("close code = %d, want %d", *e.CloseCode, tc.wantCode)
				}
				if tc.clientCloses && e.BytesToUpstream == 0 || tc.wantCode != 0 && !tc.clientCloses && e.BytesToClient == 0 {
					t.Fatalf("missing tunnel byte count: %+v", e)
				}
			case <-time.After(2 * time.Second):
				t.Fatal("missing session end event")
			}
			select {
			case e := <-events:
				t.Fatalf("duplicate event: %+v", e)
			default:
			}
		})
	}
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
