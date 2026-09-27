package proxy_test

import (
	"bufio"
	"bytes"
	"compress/flate"
	"encoding/binary"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/rvben/shinyhub/internal/proxy"
)

// wsFrame is one decoded RFC 6455 frame as seen on the wire.
type wsFrame struct {
	fin, rsv1 bool
	opcode    byte
	payload   []byte
}

// encodeFrame builds a single frame, masked when mask is non-nil.
func encodeFrame(fin, rsv1 bool, opcode byte, payload []byte, mask []byte) []byte {
	b0 := opcode
	if fin {
		b0 |= 0x80
	}
	if rsv1 {
		b0 |= 0x40
	}
	out := []byte{b0}
	maskBit := byte(0)
	if mask != nil {
		maskBit = 0x80
	}
	switch n := len(payload); {
	case n < 126:
		out = append(out, maskBit|byte(n))
	case n <= 0xFFFF:
		out = append(out, maskBit|126)
		out = binary.BigEndian.AppendUint16(out, uint16(n))
	default:
		out = append(out, maskBit|127)
		out = binary.BigEndian.AppendUint64(out, uint64(n))
	}
	if mask == nil {
		return append(out, payload...)
	}
	out = append(out, mask...)
	for i, c := range payload {
		out = append(out, c^mask[i%4])
	}
	return out
}

// readFrame decodes one frame, unmasking it if it is masked.
func readFrame(r io.Reader) (wsFrame, error) {
	var h [2]byte
	if _, err := io.ReadFull(r, h[:]); err != nil {
		return wsFrame{}, err
	}
	f := wsFrame{fin: h[0]&0x80 != 0, rsv1: h[0]&0x40 != 0, opcode: h[0] & 0x0F}
	n := uint64(h[1] & 0x7F)
	switch n {
	case 126:
		var ext [2]byte
		if _, err := io.ReadFull(r, ext[:]); err != nil {
			return wsFrame{}, err
		}
		n = uint64(binary.BigEndian.Uint16(ext[:]))
	case 127:
		var ext [8]byte
		if _, err := io.ReadFull(r, ext[:]); err != nil {
			return wsFrame{}, err
		}
		n = binary.BigEndian.Uint64(ext[:])
	}
	var mask []byte
	if h[1]&0x80 != 0 {
		mask = make([]byte, 4)
		if _, err := io.ReadFull(r, mask); err != nil {
			return wsFrame{}, err
		}
	}
	f.payload = make([]byte, n)
	if _, err := io.ReadFull(r, f.payload); err != nil {
		return wsFrame{}, err
	}
	for i := range f.payload {
		if mask != nil {
			f.payload[i] ^= mask[i%4]
		}
	}
	return f, nil
}

// deflateMessage compresses a message the way a browser does under
// permessage-deflate: raw deflate with the trailing empty stored block removed.
func deflateMessage(t *testing.T, msg []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	w, _ := flate.NewWriter(&buf, flate.BestCompression)
	if _, err := w.Write(msg); err != nil {
		t.Fatal(err)
	}
	if err := w.Flush(); err != nil {
		t.Fatal(err)
	}
	return bytes.TrimSuffix(buf.Bytes(), []byte{0x00, 0x00, 0xFF, 0xFF})
}

func inflateMessage(t *testing.T, payload []byte) []byte {
	t.Helper()
	data := append(append([]byte{}, payload...), 0x00, 0x00, 0xFF, 0xFF, 0x01, 0x00, 0x00, 0xFF, 0xFF)
	out, err := io.ReadAll(flate.NewReader(bytes.NewReader(data)))
	if err != nil {
		t.Fatalf("inflate: %v", err)
	}
	return out
}

// wsBackend is a raw WebSocket backend. It answers the upgrade with the
// given extension header (empty = negotiates nothing, as httpuv does), then
// records the raw bytes of the first client frame and sends reply verbatim.
type wsBackend struct {
	srv      *httptest.Server
	received chan []byte
	offered  chan string
}

func startWSBackend(t *testing.T, extension string, reply []byte) *wsBackend {
	t.Helper()
	b := &wsBackend{received: make(chan []byte, 1), offered: make(chan string, 1)}
	b.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b.offered <- r.Header.Get("Sec-WebSocket-Extensions")
		conn, buf, err := w.(http.Hijacker).Hijack()
		if err != nil {
			t.Errorf("backend hijack: %v", err)
			return
		}
		defer conn.Close()
		head := "HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\n"
		if extension != "" {
			head += "Sec-WebSocket-Extensions: " + extension + "\r\n"
		}
		if _, err := buf.WriteString(head + "\r\n"); err != nil || buf.Flush() != nil {
			return
		}
		var raw bytes.Buffer
		if _, err := readFrame(io.TeeReader(buf, &raw)); err != nil {
			t.Errorf("backend read frame: %v", err)
			return
		}
		b.received <- raw.Bytes()
		if _, err := conn.Write(reply); err != nil {
			return
		}
		// Hold the connection open until the client goes away, so the
		// proxy's teardown is driven by the test and not by the backend.
		_, _ = io.Copy(io.Discard, buf)
	}))
	t.Cleanup(b.srv.Close)
	return b
}

// dialUpgrade opens a WebSocket through the proxy, offering extensions when
// non-empty, and returns the connection plus the response headers.
func dialUpgrade(t *testing.T, frontAddr, extensions string) (net.Conn, *bufio.Reader, http.Header) {
	t.Helper()
	conn, err := net.DialTimeout("tcp", frontAddr, 2*time.Second)
	if err != nil {
		t.Fatalf("dial front: %v", err)
	}
	t.Cleanup(func() { conn.Close() })
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
	req := "GET /app/demo/websocket/ HTTP/1.1\r\nHost: x\r\nConnection: Upgrade\r\nUpgrade: websocket\r\n" +
		"Sec-WebSocket-Version: 13\r\nSec-WebSocket-Key: dGhlIHNhbXBsZSBub25jZQ==\r\n"
	if extensions != "" {
		req += "Sec-WebSocket-Extensions: " + extensions + "\r\n"
	}
	if _, err := conn.Write([]byte(req + "\r\n")); err != nil {
		t.Fatalf("write request: %v", err)
	}
	reader := bufio.NewReader(conn)
	resp, err := http.ReadResponse(reader, nil)
	if err != nil {
		t.Fatalf("read upgrade response: %v", err)
	}
	if resp.StatusCode != http.StatusSwitchingProtocols {
		t.Fatalf("status = %d, want 101", resp.StatusCode)
	}
	return conn, reader, resp.Header
}

func newWSProxy(t *testing.T, backendURL string, compression bool) string {
	t.Helper()
	p := proxy.New()
	p.SetWebSocketCompression(compression)
	if err := p.Register("demo", backendURL); err != nil {
		t.Fatalf("Register: %v", err)
	}
	front := httptest.NewServer(p)
	t.Cleanup(front.Close)
	return front.Listener.Addr().String()
}

const browserOffer = "permessage-deflate; client_max_window_bits"

// A Shiny output message: large, repetitive JSON, like a rendered table.
var shinyUpdate = []byte(`{"values":{"table":"` + strings.Repeat(`<tr><td>42.0</td><td>north</td></tr>`, 400) + `"}}`)

// An R Shiny backend (httpuv) never negotiates compression. Through the
// proxy the browser must still get a compressed session: the proxy accepts
// the offer, compresses what the backend sends, and hands the backend the
// browser's messages as plain frames it can read.
func TestWebSocketCompression_BackendWithoutDeflate(t *testing.T) {
	reply := encodeFrame(true, false, 0x1, shinyUpdate, nil)
	backend := startWSBackend(t, "", reply)
	conn, reader, header := dialUpgrade(t, newWSProxy(t, backend.srv.URL, true), browserOffer)

	if got := header.Get("Sec-WebSocket-Extensions"); !strings.HasPrefix(got, "permessage-deflate") {
		t.Fatalf("Sec-WebSocket-Extensions = %q; the proxy did not accept the browser's offer", got)
	}

	clientMsg := []byte(`{"method":"update","data":{"filter":"` + strings.Repeat("north,", 100) + `"}}`)
	if _, err := conn.Write(encodeFrame(true, true, 0x1, deflateMessage(t, clientMsg), []byte{1, 2, 3, 4})); err != nil {
		t.Fatalf("write client frame: %v", err)
	}

	var gotRaw []byte
	select {
	case gotRaw = <-backend.received:
	case <-time.After(5 * time.Second):
		t.Fatal("backend never received the client's message")
	}
	got, err := readFrame(bytes.NewReader(gotRaw))
	if err != nil {
		t.Fatalf("decode backend frame: %v", err)
	}
	if got.rsv1 || got.opcode != 0x1 || !got.fin || !bytes.Equal(got.payload, clientMsg) {
		t.Fatalf("backend got fin=%v rsv1=%v opcode=%d payload=%q; want the plain text message", got.fin, got.rsv1, got.opcode, got.payload)
	}

	frame, err := readFrame(reader)
	if err != nil {
		t.Fatalf("read proxied frame: %v", err)
	}
	if !frame.rsv1 {
		t.Fatalf("the backend's %d-byte message reached the browser uncompressed", len(shinyUpdate))
	}
	if len(frame.payload)*4 > len(shinyUpdate) {
		t.Errorf("compressed to %d of %d bytes; expected well under a quarter", len(frame.payload), len(shinyUpdate))
	}
	if msg := inflateMessage(t, frame.payload); !bytes.Equal(msg, shinyUpdate) {
		t.Fatal("the compressed message does not decompress to what the backend sent")
	}
}

// Python Shiny's uvicorn negotiates permessage-deflate itself. The proxy
// must relay that session untouched in both directions: compressing again,
// or rewriting the header, would break a session that already works.
func TestWebSocketCompression_BackendThatNegotiatesIsUntouched(t *testing.T) {
	const backendAccept = "permessage-deflate; client_max_window_bits=12"
	reply := encodeFrame(true, true, 0x1, deflateMessage(t, shinyUpdate), nil)
	backend := startWSBackend(t, backendAccept, reply)
	conn, reader, header := dialUpgrade(t, newWSProxy(t, backend.srv.URL, true), browserOffer)

	if got := header.Get("Sec-WebSocket-Extensions"); got != backendAccept {
		t.Fatalf("Sec-WebSocket-Extensions = %q, want the backend's own %q", got, backendAccept)
	}

	sent := encodeFrame(true, true, 0x1, deflateMessage(t, []byte("hello hello hello")), []byte{9, 8, 7, 6})
	if _, err := conn.Write(sent); err != nil {
		t.Fatalf("write client frame: %v", err)
	}
	select {
	case got := <-backend.received:
		if !bytes.Equal(got, sent) {
			t.Fatalf("backend received %x, want the client's bytes %x unchanged", got, sent)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("backend never received the client's message")
	}

	got := make([]byte, len(reply))
	if _, err := io.ReadFull(reader, got); err != nil {
		t.Fatalf("read proxied bytes: %v", err)
	}
	if !bytes.Equal(got, reply) {
		t.Fatal("the backend's compressed frame was altered on its way to the client")
	}
}

// With the feature off, and for a client that offers no compression, the
// proxy is a plain relay: no extension header, the backend's frames verbatim.
func TestWebSocketCompression_PassthroughCases(t *testing.T) {
	for _, tc := range []struct {
		name        string
		compression bool
		offer       string
	}{
		{name: "feature disabled", compression: false, offer: browserOffer},
		{name: "client offers nothing", compression: true, offer: ""},
		{name: "client offers only an unsupported variant", compression: true, offer: "permessage-deflate; server_max_window_bits=10"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			reply := encodeFrame(true, false, 0x1, shinyUpdate, nil)
			backend := startWSBackend(t, "", reply)
			conn, reader, header := dialUpgrade(t, newWSProxy(t, backend.srv.URL, tc.compression), tc.offer)

			if got := header.Values("Sec-WebSocket-Extensions"); len(got) != 0 {
				t.Fatalf("Sec-WebSocket-Extensions = %q, want none", got)
			}
			sent := encodeFrame(true, false, 0x1, []byte("plain"), []byte{1, 1, 1, 1})
			if _, err := conn.Write(sent); err != nil {
				t.Fatalf("write client frame: %v", err)
			}
			select {
			case got := <-backend.received:
				if !bytes.Equal(got, sent) {
					t.Fatalf("backend received %x, want %x", got, sent)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("backend never received the client's message")
			}
			got := make([]byte, len(reply))
			if _, err := io.ReadFull(reader, got); err != nil {
				t.Fatalf("read proxied bytes: %v", err)
			}
			if !bytes.Equal(got, reply) {
				t.Fatal("the backend's frame was altered on a connection that negotiated nothing")
			}
		})
	}
}
