package wsdeflate

import (
	"bufio"
	"bytes"
	"compress/flate"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"strings"
	"sync"
	"testing"
	"time"
)

// harness connects a Translator to one end of an in-memory backend
// connection. The test plays the backend on the other end, and plays the
// client by calling Write (client bytes in) and Read (client bytes out),
// exactly as httputil.ReverseProxy does.
type harness struct {
	t       *testing.T
	tr      *Translator
	backend net.Conn // the backend's side
	br      *bufio.Reader
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	proxySide, backendSide := net.Pipe()
	h := &harness{t: t, tr: NewTranslator(proxySide), backend: backendSide, br: bufio.NewReader(backendSide)}
	t.Cleanup(func() {
		_ = h.tr.Close()
		_ = backendSide.Close()
	})
	return h
}

// clientSend writes client bytes into the translator from its own goroutine,
// since net.Pipe blocks until the backend reads.
func (h *harness) clientSend(b []byte) <-chan error {
	done := make(chan error, 1)
	go func() {
		n, err := h.tr.Write(b)
		if err == nil && n != len(b) {
			err = io.ErrShortWrite
		}
		done <- err
	}()
	return done
}

// backendSend writes backend bytes from its own goroutine.
func (h *harness) backendSend(b []byte) {
	go func() { _, _ = h.backend.Write(b) }()
}

// readBackendFrame reads one frame as the backend sees it, unmasking it.
func (h *harness) readBackendFrame() frame {
	h.t.Helper()
	_ = h.backend.SetReadDeadline(time.Now().Add(5 * time.Second))
	f, err := readFrame(h.br)
	if err != nil {
		h.t.Fatalf("backend read: %v", err)
	}
	return f
}

// readClientFrame reads one frame from the translator's client-bound output.
func (h *harness) readClientFrame() frame {
	h.t.Helper()
	type result struct {
		f   frame
		err error
	}
	ch := make(chan result, 1)
	go func() {
		f, err := readFrame(bufio.NewReaderSize(&oneFrameReader{tr: h.tr}, 1))
		ch <- result{f, err}
	}()
	select {
	case r := <-ch:
		if r.err != nil {
			h.t.Fatalf("client read: %v", r.err)
		}
		return r.f
	case <-time.After(5 * time.Second):
		h.t.Fatal("client read timed out")
	}
	return frame{}
}

// oneFrameReader adapts Translator.Read for a byte-at-a-time bufio reader so
// a frame read never consumes bytes of the next frame.
type oneFrameReader struct{ tr *Translator }

func (r *oneFrameReader) Read(p []byte) (int, error) { return r.tr.Read(p[:1]) }

type frame struct {
	fin     bool
	rsv     byte
	opcode  byte
	masked  bool
	payload []byte // unmasked
}

func readFrame(r *bufio.Reader) (frame, error) {
	var hdr [2]byte
	if _, err := io.ReadFull(r, hdr[:]); err != nil {
		return frame{}, err
	}
	f := frame{fin: hdr[0]&0x80 != 0, rsv: hdr[0] & 0x70, opcode: hdr[0] & 0x0f, masked: hdr[1]&0x80 != 0}
	length := uint64(hdr[1] & 0x7f)
	switch length {
	case 126:
		var ext [2]byte
		if _, err := io.ReadFull(r, ext[:]); err != nil {
			return frame{}, err
		}
		length = uint64(binary.BigEndian.Uint16(ext[:]))
	case 127:
		var ext [8]byte
		if _, err := io.ReadFull(r, ext[:]); err != nil {
			return frame{}, err
		}
		length = binary.BigEndian.Uint64(ext[:])
	}
	var key [4]byte
	if f.masked {
		if _, err := io.ReadFull(r, key[:]); err != nil {
			return frame{}, err
		}
	}
	f.payload = make([]byte, length)
	if _, err := io.ReadFull(r, f.payload); err != nil {
		return frame{}, err
	}
	if f.masked {
		for i := range f.payload {
			f.payload[i] ^= key[i&3]
		}
	}
	return f, nil
}

// encodeFrame builds a frame. first carries FIN, RSV and opcode bits.
func encodeFrame(first byte, payload []byte, masked bool) []byte {
	var key *[4]byte
	if masked {
		key = &[4]byte{0x37, 0xfa, 0x21, 0x3d}
	}
	b := appendHeader(nil, first, int64(len(payload)), key)
	start := len(b)
	b = append(b, payload...)
	if key != nil {
		for i := range payload {
			b[start+i] ^= key[i&3]
		}
	}
	return b
}

// clientDeflate compresses a message the way a permessage-deflate client
// does (RFC 7692 section 7.2.1), independently of the translator's code.
func clientDeflate(t *testing.T, msg []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	w, err := flate.NewWriter(&buf, flate.BestSpeed)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write(msg); err != nil {
		t.Fatal(err)
	}
	if err := w.Flush(); err != nil {
		t.Fatal(err)
	}
	out := buf.Bytes()
	if !bytes.HasSuffix(out, []byte{0, 0, 0xff, 0xff}) {
		t.Fatal("sync flush did not end with the empty stored block")
	}
	return out[:len(out)-4]
}

// clientInflate decompresses a message the way a client does (RFC 7692
// section 7.2.2): append the removed tail and inflate.
func clientInflate(t *testing.T, payload []byte) []byte {
	t.Helper()
	r := flate.NewReader(io.MultiReader(bytes.NewReader(payload), bytes.NewReader([]byte{0, 0, 0xff, 0xff})))
	out, err := io.ReadAll(r)
	if err != nil && !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("inflate: %v", err)
	}
	return out
}

func tableJSON(rows int) []byte {
	var b strings.Builder
	b.WriteString(`{"values":{"table":"<table>`)
	for i := 0; i < rows; i++ {
		b.WriteString(`<tr><td>row</td><td>12.5</td><td>north</td><td>2026-01-01</td></tr>`)
	}
	b.WriteString(`</table>"}}`)
	return []byte(b.String())
}

func TestRFC7692HelloFromClientReachesBackendPlain(t *testing.T) {
	h := newHarness(t)
	// RFC 7692 section 7.2.3.1: "Hello" compressed in a single frame.
	compressed := []byte{0xf2, 0x48, 0xcd, 0xc9, 0xc9, 0x07, 0x00}
	done := h.clientSend(encodeFrame(0x80|0x40|opText, compressed, true))
	f := h.readBackendFrame()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if !f.fin || f.rsv != 0 || f.opcode != opText || !f.masked || string(f.payload) != "Hello" {
		t.Fatalf("backend got fin=%v rsv=%#x op=%d masked=%v %q", f.fin, f.rsv, f.opcode, f.masked, f.payload)
	}
}

func TestRFC7692FragmentedHelloIsReassembled(t *testing.T) {
	h := newHarness(t)
	// RFC 7692 section 7.2.3.1: the compressed "Hello" split over two frames,
	// with RSV1 on the first only.
	first := encodeFrame(0x40|opText, []byte{0xf2, 0x48, 0xcd}, true)
	second := encodeFrame(0x80|opContinuation, []byte{0xc9, 0xc9, 0x07, 0x00}, true)
	done := h.clientSend(append(first, second...))
	f := h.readBackendFrame()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if !f.fin || f.opcode != opText || string(f.payload) != "Hello" {
		t.Fatalf("backend got fin=%v op=%d %q", f.fin, f.opcode, f.payload)
	}
}

func TestLargeBackendMessageIsCompressedForClient(t *testing.T) {
	h := newHarness(t)
	msg := tableJSON(2000)
	h.backendSend(encodeFrame(0x80|opText, msg, false))
	f := h.readClientFrame()
	if f.rsv != 0x40 || !f.fin || f.opcode != opText || f.masked {
		t.Fatalf("client frame fin=%v rsv=%#x op=%d masked=%v", f.fin, f.rsv, f.opcode, f.masked)
	}
	if len(f.payload) >= len(msg)/4 {
		t.Fatalf("compressed %d bytes to %d; expected at least 4x", len(msg), len(f.payload))
	}
	if got := clientInflate(t, f.payload); !bytes.Equal(got, msg) {
		t.Fatalf("inflated message differs: %d vs %d bytes", len(got), len(msg))
	}
}

func TestEachBackendMessageDecompressesIndependently(t *testing.T) {
	// No context takeover: a client must be able to inflate each message
	// alone, with a fresh inflater.
	h := newHarness(t)
	a, b := tableJSON(50), tableJSON(60)
	h.backendSend(append(encodeFrame(0x80|opText, a, false), encodeFrame(0x80|opBinary, b, false)...))
	fa, fb := h.readClientFrame(), h.readClientFrame()
	if !bytes.Equal(clientInflate(t, fa.payload), a) || !bytes.Equal(clientInflate(t, fb.payload), b) {
		t.Fatal("messages do not inflate independently")
	}
	if fb.opcode != opBinary {
		t.Fatalf("opcode %d, want binary", fb.opcode)
	}
}

func TestPassthroughBackendFramesAreByteIdentical(t *testing.T) {
	cases := map[string][]byte{
		"small message": encodeFrame(0x80|opText, []byte(`{"busy":"idle"}`), false),
		"ping":          encodeFrame(0x80|0x9, []byte("p"), false),
		"close":         encodeFrame(0x80|opClose, []byte{0x03, 0xe8}, false),
		"fragmented with ping between": append(append(
			encodeFrame(opText, bytes.Repeat([]byte("a"), 400), false),
			encodeFrame(0x80|0x9, nil, false)...),
			encodeFrame(0x80|opContinuation, bytes.Repeat([]byte("b"), 400), false)...),
	}
	for name, in := range cases {
		t.Run(name, func(t *testing.T) {
			h := newHarness(t)
			h.backendSend(in)
			got := make([]byte, len(in))
			if _, err := io.ReadFull(h.tr, got); err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(got, in) {
				t.Fatalf("passthrough altered bytes")
			}
		})
	}
}

func TestIncompressibleBackendMessageIsSentPlain(t *testing.T) {
	h := newHarness(t)
	msg := make([]byte, 4096)
	x := uint32(2463534242)
	for i := range msg {
		x ^= x << 13
		x ^= x >> 17
		x ^= x << 5
		msg[i] = byte(x)
	}
	in := encodeFrame(0x80|opBinary, msg, false)
	h.backendSend(in)
	got := make([]byte, len(in))
	if _, err := io.ReadFull(h.tr, got); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, in) {
		t.Fatal("incompressible message was not relayed unchanged")
	}
}

func TestPlainClientFramesPassThroughUnchanged(t *testing.T) {
	h := newHarness(t)
	in := append(encodeFrame(0x80|opText, []byte("plain message"), true), encodeFrame(0x80|0x9, []byte("hi"), true)...)
	done := h.clientSend(in)
	got := make([]byte, len(in))
	_ = h.backend.SetReadDeadline(time.Now().Add(5 * time.Second))
	if _, err := io.ReadFull(h.br, got); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, in) {
		t.Fatal("plain client frames were altered")
	}
}

func TestClientBytesArrivingOneAtATime(t *testing.T) {
	h := newHarness(t)
	msg := tableJSON(30)
	in := encodeFrame(0x80|0x40|opText, clientDeflate(t, msg), true)
	go func() {
		for i := range in {
			if _, err := h.tr.Write(in[i : i+1]); err != nil {
				return
			}
		}
	}()
	f := h.readBackendFrame()
	if !bytes.Equal(f.payload, msg) {
		t.Fatal("byte-at-a-time message was not reassembled")
	}
}

func TestControlFrameInsideCompressedMessageIsForwardedFirst(t *testing.T) {
	h := newHarness(t)
	msg := tableJSON(20)
	c := clientDeflate(t, msg)
	half := len(c) / 2
	in := encodeFrame(0x40|opText, c[:half], true)
	in = append(in, encodeFrame(0x80|0x9, []byte("ping"), true)...)
	in = append(in, encodeFrame(0x80|opContinuation, c[half:], true)...)
	done := h.clientSend(in)
	ping := h.readBackendFrame()
	data := h.readBackendFrame()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if ping.opcode != 0x9 || string(ping.payload) != "ping" {
		t.Fatalf("first backend frame op=%d %q, want the ping", ping.opcode, ping.payload)
	}
	if data.opcode != opText || !bytes.Equal(data.payload, msg) {
		t.Fatal("reassembled message differs")
	}
}

// expectClientClose asserts the client receives a close frame with code and
// that Read then reports an error, i.e. the proxy would tear down.
func expectClientClose(t *testing.T, h *harness, code uint16) {
	t.Helper()
	f := h.readClientFrame()
	if f.opcode != opClose || len(f.payload) < 2 || binary.BigEndian.Uint16(f.payload) != code {
		t.Fatalf("client got op=%d payload=%x, want close %d", f.opcode, f.payload, code)
	}
	errc := make(chan error, 1)
	go func() {
		_, err := h.tr.Read(make([]byte, 16))
		errc <- err
	}()
	select {
	case err := <-errc:
		if err == nil {
			t.Fatal("Read after the close frame returned no error")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Read after the close frame did not return")
	}
}

func TestClientFaultsCloseWithCode(t *testing.T) {
	bomb := func(t *testing.T) []byte {
		return clientDeflate(t, make([]byte, MaxInflatedMessage+1))
	}
	cases := []struct {
		name  string
		frame func(t *testing.T) []byte
		code  uint16
	}{
		{"unmasked frame", func(*testing.T) []byte { return encodeFrame(0x80|opText, []byte("x"), false) }, closeProtocolError},
		{"rsv2 set", func(*testing.T) []byte { return encodeFrame(0x80|0x20|opText, []byte("x"), true) }, closeProtocolError},
		{"rsv1 on control", func(*testing.T) []byte { return encodeFrame(0x80|0x40|0x9, nil, true) }, closeProtocolError},
		{"oversized control", func(*testing.T) []byte { return encodeFrame(0x80|0x9, make([]byte, 126), true) }, closeProtocolError},
		{"continuation without message", func(*testing.T) []byte { return encodeFrame(0x80|opContinuation, []byte("x"), true) }, closeProtocolError},
		{"reserved opcode", func(*testing.T) []byte { return encodeFrame(0x80|0x3, []byte("x"), true) }, closeProtocolError},
		{"invalid deflate data", func(*testing.T) []byte { return encodeFrame(0x80|0x40|opText, []byte{0xff, 0xff, 0xff}, true) }, closeProtocolError},
		{"inflates past the cap", func(t *testing.T) []byte { return encodeFrame(0x80|0x40|opBinary, bomb(t), true) }, closeTooBig},
		{"compressed frame header over the cap", func(*testing.T) []byte {
			// Only the header: the cap must trip before any payload is read.
			return appendHeader(nil, 0x80|0x40|opBinary, MaxCompressedMessage+1, &[4]byte{1, 2, 3, 4})
		}, closeTooBig},
		{"continuation length that overflows the running total", func(*testing.T) []byte {
			// A one-byte compressed first fragment, then a continuation
			// declaring the largest valid 64-bit length: the running total
			// wraps negative unless the cap is checked against what is left.
			b := encodeFrame(0x40|opBinary, []byte{0x01}, true)
			return appendHeader(b, 0x80|opContinuation, 1<<63-1, &[4]byte{1, 2, 3, 4})
		}, closeTooBig},
		{"negative 64-bit length", func(*testing.T) []byte {
			b := []byte{0x80 | opBinary, 0x80 | 127}
			b = binary.BigEndian.AppendUint64(b, 1<<63)
			return append(b, 1, 2, 3, 4)
		}, closeProtocolError},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t)
			// A Read already blocked on the backend when the fault arrives
			// must still deliver the close frame.
			type result struct {
				f   frame
				err error
			}
			ch := make(chan result, 1)
			go func() {
				f, err := readFrame(bufio.NewReaderSize(&oneFrameReader{tr: h.tr}, 1))
				ch <- result{f, err}
			}()
			time.Sleep(20 * time.Millisecond)
			if err := <-h.clientSend(tc.frame(t)); err != nil {
				t.Fatalf("Write reported the client fault as an error (%v); the proxy would drop the close frame", err)
			}
			var r result
			select {
			case r = <-ch:
			case <-time.After(5 * time.Second):
				t.Fatal("blocked Read was not woken by the client fault")
			}
			if r.err != nil {
				t.Fatalf("client read: %v", r.err)
			}
			if r.f.opcode != opClose || binary.BigEndian.Uint16(r.f.payload) != tc.code {
				t.Fatalf("client got op=%d payload=%x, want close %d", r.f.opcode, r.f.payload, tc.code)
			}
			if _, err := h.tr.Read(make([]byte, 16)); err == nil {
				t.Fatal("Read after the close frame returned no error")
			}
			// Further client input is accepted and discarded.
			if n, err := h.tr.Write([]byte{1, 2, 3}); err != nil || n != 3 {
				t.Fatalf("Write after fault = %d, %v", n, err)
			}
		})
	}
}

func TestBackendProtocolErrorClosesClientWith1011(t *testing.T) {
	cases := map[string][]byte{
		"masked frame":             encodeFrame(0x80|opText, []byte("x"), true),
		"rsv1 without negotiation": encodeFrame(0x80|0x40|opText, []byte("x"), false),
		"continuation first":       encodeFrame(0x80|opContinuation, []byte("x"), false),
	}
	for name, in := range cases {
		t.Run(name, func(t *testing.T) {
			h := newHarness(t)
			h.backendSend(in)
			expectClientClose(t, h, closeInternalError)
		})
	}
}

func TestBackendEOFEndsRead(t *testing.T) {
	h := newHarness(t)
	_ = h.backend.Close()
	if _, err := h.tr.Read(make([]byte, 8)); !errors.Is(err, io.EOF) {
		t.Fatalf("Read after backend close = %v, want EOF", err)
	}
}

type closeWriteConn struct {
	io.ReadWriteCloser
	mu     sync.Mutex
	closed bool
}

func (c *closeWriteConn) CloseWrite() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.closed = true
	return nil
}

func TestCloseWriteIsForwarded(t *testing.T) {
	a, b := net.Pipe()
	defer a.Close()
	defer b.Close()
	cw := &closeWriteConn{ReadWriteCloser: a}
	if err := NewTranslator(cw).CloseWrite(); err != nil {
		t.Fatal(err)
	}
	if !cw.closed {
		t.Fatal("CloseWrite was not forwarded to the backend")
	}
}
