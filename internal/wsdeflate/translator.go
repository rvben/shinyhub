package wsdeflate

import (
	"bufio"
	"bytes"
	"compress/flate"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"sync"
)

const (
	opContinuation = 0x0
	opText         = 0x1
	opBinary       = 0x2
	opClose        = 0x8

	finBit  = 0x80
	rsv1Bit = 0x40
	rsvBits = 0x70
	maskBit = 0x80

	maxControlPayload = 125

	// Close codes from RFC 6455 section 7.4.1.
	closeProtocolError = 1002
	closeTooBig        = 1009
	closeInternalError = 1011
)

const (
	// MinCompressSize is the smallest backend message worth compressing.
	// Below it the deflate framing saves little and costs a compressor pass.
	MinCompressSize = 256

	// MaxCompressSource is the largest backend message that is buffered in
	// order to compress it. A larger one streams through uncompressed, so the
	// proxy never holds an unbounded message in memory.
	MaxCompressSource = 16 << 20

	// MaxCompressedMessage caps the compressed bytes of one client message.
	// It is enforced from each frame header, before the payload is read.
	MaxCompressedMessage = 16 << 20

	// MaxInflatedMessage caps the size one client message may inflate to, so
	// a small compressed message cannot expand without bound.
	MaxInflatedMessage = 64 << 20
)

// deflateTail is the empty stored block a sync flush ends with. RFC 7692
// section 7.2.1 removes it from every compressed message.
var deflateTail = []byte{0x00, 0x00, 0xff, 0xff}

// inflateTail restores the removed tail and adds an empty final stored block:
// Go's flate reader only reports a clean end of stream after a final block.
var inflateTail = []byte{0x00, 0x00, 0xff, 0xff, 0x01, 0x00, 0x00, 0xff, 0xff}

var deflaters = sync.Pool{New: func() any {
	w, _ := flate.NewWriter(nil, flate.DefaultCompression)
	return w
}}

var inflaters sync.Pool

// errProtocol marks a frame that violates RFC 6455 or RFC 7692.
var errProtocol = errors.New("wsdeflate: protocol error")

// errTooBig marks a client message over MaxCompressedMessage or
// MaxInflatedMessage.
var errTooBig = errors.New("wsdeflate: message too big")

// Translator sits where httputil.ReverseProxy expects the upgraded backend
// connection. The proxy copies client bytes into Write and copies Read's bytes
// to the client, each from its own goroutine. Read turns the backend's plain
// frames into compressed ones for the client; Write turns the client's
// compressed frames back into plain ones for the backend.
//
// The two directions share nothing except a failure latch: when the client
// side must be failed (a protocol error or an oversized message), Write queues
// a close frame for the client, closes the backend so a Read blocked on it
// returns, and keeps accepting input. Read then delivers the close frame at
// the next frame boundary before reporting the error. Write never reports that
// failure itself, because the proxy tears the client connection down as soon
// as either direction returns an error, which would discard the close frame.
type Translator struct {
	backend io.ReadWriteCloser
	br      *bufio.Reader

	// Read side (backend to client); touched only by the Read goroutine.
	out             []byte
	streamRemaining int64
	fragmented      bool
	terminal        error

	// Write side (client to backend); touched only by the Write goroutine.
	in              []byte
	passRemaining   int64
	collectRemain   int64
	collectFin      bool
	mask            [4]byte
	maskPos         int
	msgActive       bool
	msgCompressed   bool
	msgOpcode       byte
	msg             []byte
	rejectingClient bool

	mu         sync.Mutex
	failure    error
	closeFrame []byte

	closeOnce sync.Once
	closeErr  error
}

// NewTranslator wraps an upgraded backend connection whose handshake
// negotiated no extensions.
func NewTranslator(backend io.ReadWriteCloser) *Translator {
	return &Translator{backend: backend, br: bufio.NewReaderSize(backend, 32<<10)}
}

// Close closes the backend connection. It is safe to call more than once.
func (t *Translator) Close() error {
	t.closeOnce.Do(func() { t.closeErr = t.backend.Close() })
	return t.closeErr
}

// CloseWrite half-closes the backend when the underlying connection supports
// it, which is how the proxy passes on the client's end of input.
func (t *Translator) CloseWrite() error {
	if cw, ok := t.backend.(interface{ CloseWrite() error }); ok {
		return cw.CloseWrite()
	}
	return nil
}

// failClient latches the first client-side failure, queues the close frame
// the client is owed, and closes the backend so a blocked Read wakes up.
func (t *Translator) failClient(code uint16, err error) {
	t.mu.Lock()
	if t.failure == nil {
		t.failure = err
		t.closeFrame = closeFrame(code)
	}
	t.mu.Unlock()
	t.rejectingClient = true
	_ = t.Close()
}

// takeFailure returns the queued client close frame, once, and the latched
// failure.
func (t *Translator) takeFailure() ([]byte, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	cf := t.closeFrame
	t.closeFrame = nil
	return cf, t.failure
}

// ---- Read side: backend frames to client frames ----

func (t *Translator) Read(p []byte) (int, error) {
	for {
		if len(t.out) > 0 {
			n := copy(p, t.out)
			t.out = t.out[n:]
			return n, nil
		}
		if t.terminal != nil {
			return 0, t.terminal
		}
		if t.streamRemaining > 0 {
			chunk := p
			if int64(len(chunk)) > t.streamRemaining {
				chunk = chunk[:t.streamRemaining]
			}
			n, err := t.br.Read(chunk)
			t.streamRemaining -= int64(n)
			if n > 0 {
				return n, nil
			}
			// Mid-frame there is no clean place for a close frame; the
			// connection simply ends.
			if _, failure := t.takeFailure(); failure != nil {
				return 0, failure
			}
			return 0, err
		}
		// At a frame boundary: a pending client failure goes out first.
		if cf, failure := t.takeFailure(); failure != nil {
			t.out, t.terminal = cf, failure
			continue
		}
		if err := t.nextBackendFrame(); err != nil {
			if cf, failure := t.takeFailure(); failure != nil {
				t.out, t.terminal = cf, failure
				continue
			}
			if errors.Is(err, errProtocol) {
				t.out, t.terminal = closeFrame(closeInternalError), err
				_ = t.Close()
				continue
			}
			return 0, err
		}
	}
}

// nextBackendFrame reads one frame header from the backend and stages the
// client-bound bytes for it in t.out, leaving any payload that streams
// through in t.streamRemaining.
func (t *Translator) nextBackendFrame() error {
	h, raw, err := readHeader(t.br)
	if err != nil {
		return err
	}
	if h.masked || h.rsv != 0 || h.length < 0 {
		return fmt.Errorf("%w: backend frame masked, with reserved bits, or of invalid length", errProtocol)
	}
	switch {
	case h.opcode >= opClose:
		if !h.fin || h.length > maxControlPayload || h.opcode > 0xA {
			return fmt.Errorf("%w: invalid backend control frame", errProtocol)
		}
		t.out = raw
		t.streamRemaining = h.length
		return nil
	case h.opcode == opContinuation:
		if !t.fragmented {
			return fmt.Errorf("%w: backend continuation outside a message", errProtocol)
		}
		t.fragmented = !h.fin
		t.out = raw
		t.streamRemaining = h.length
		return nil
	case h.opcode == opText || h.opcode == opBinary:
		if t.fragmented {
			return fmt.Errorf("%w: backend data frame inside a fragmented message", errProtocol)
		}
		if h.fin && h.length >= MinCompressSize && h.length <= MaxCompressSource {
			payload := make([]byte, h.length)
			if _, err := io.ReadFull(t.br, payload); err != nil {
				return err
			}
			if c := compress(payload); len(c) < len(payload) {
				t.out = appendHeader(nil, finBit|rsv1Bit|h.opcode, int64(len(c)), nil)
				t.out = append(t.out, c...)
			} else {
				t.out = append(raw, payload...)
			}
			return nil
		}
		// Fragmented and oversized messages are relayed as they arrive.
		t.fragmented = !h.fin
		t.out = raw
		t.streamRemaining = h.length
		return nil
	default:
		return fmt.Errorf("%w: reserved backend opcode %#x", errProtocol, h.opcode)
	}
}

func compress(payload []byte) []byte {
	var buf bytes.Buffer
	buf.Grow(len(payload) / 4)
	w := deflaters.Get().(*flate.Writer)
	w.Reset(&buf)
	_, _ = w.Write(payload)
	_ = w.Flush()
	deflaters.Put(w)
	return bytes.TrimSuffix(buf.Bytes(), deflateTail)
}

// ---- Write side: client frames to backend frames ----

func (t *Translator) Write(p []byte) (int, error) {
	if t.rejectingClient {
		return len(p), nil
	}
	buf := p
	if len(t.in) > 0 {
		buf = append(t.in, p...)
	}
	for len(buf) > 0 && !t.rejectingClient {
		switch {
		case t.passRemaining > 0:
			n := min(int64(len(buf)), t.passRemaining)
			if _, err := t.backend.Write(buf[:n]); err != nil {
				return 0, err
			}
			t.passRemaining -= n
			buf = buf[n:]
		case t.collectRemain > 0:
			n := min(int64(len(buf)), t.collectRemain)
			for _, b := range buf[:n] {
				t.msg = append(t.msg, b^t.mask[t.maskPos&3])
				t.maskPos++
			}
			t.collectRemain -= n
			buf = buf[n:]
			if t.collectRemain == 0 && t.collectFin {
				if err := t.finishMessage(); err != nil {
					return 0, err
				}
			}
		default:
			h, n, ok := parseHeader(buf)
			if !ok {
				// Keep the partial header for the next Write. append onto
				// t.in[:0] is a memmove, so buf aliasing t.in is safe.
				t.in = append(t.in[:0], buf...)
				return len(p), nil
			}
			if err := t.clientFrame(h, buf[:n]); err != nil {
				return 0, err
			}
			buf = buf[n:]
		}
	}
	t.in = t.in[:0]
	return len(p), nil
}

// clientFrame validates one client frame header and either relays it
// unchanged or starts collecting its compressed payload. A returned error is a
// backend write failure; client faults go through failClient.
func (t *Translator) clientFrame(h header, raw []byte) error {
	fault := func(reason string) error {
		t.failClient(closeProtocolError, fmt.Errorf("%w: %s", errProtocol, reason))
		return nil
	}
	if !h.masked {
		return fault("unmasked client frame")
	}
	if h.length < 0 {
		return fault("invalid payload length")
	}
	if h.rsv&^rsv1Bit != 0 {
		return fault("reserved bits set")
	}
	rsv1 := h.rsv&rsv1Bit != 0
	switch {
	case h.opcode >= opClose:
		if !h.fin || h.length > maxControlPayload || rsv1 || h.opcode > 0xA {
			return fault("invalid control frame")
		}
		return t.relay(raw, h.length)
	case h.opcode == opContinuation:
		if !t.msgActive || rsv1 {
			return fault("invalid continuation frame")
		}
		t.msgActive = !h.fin
		if t.msgCompressed {
			return t.collect(h)
		}
		return t.relay(raw, h.length)
	case h.opcode == opText || h.opcode == opBinary:
		if t.msgActive {
			return fault("data frame inside a fragmented message")
		}
		t.msgActive = !h.fin
		t.msgCompressed = rsv1
		t.msgOpcode = h.opcode
		if rsv1 {
			t.msg = t.msg[:0]
			return t.collect(h)
		}
		return t.relay(raw, h.length)
	default:
		return fault("reserved opcode")
	}
}

func (t *Translator) relay(raw []byte, length int64) error {
	if _, err := t.backend.Write(raw); err != nil {
		return err
	}
	t.passRemaining = length
	return nil
}

func (t *Translator) collect(h header) error {
	// Compared against what is left rather than summed: a declared length
	// can be up to 2^63-1, and the sum would wrap negative past the cap.
	if h.length > MaxCompressedMessage-int64(len(t.msg)) {
		t.failClient(closeTooBig, errTooBig)
		return nil
	}
	t.mask, t.maskPos = h.mask, 0
	t.collectRemain, t.collectFin = h.length, h.fin
	if h.length == 0 && h.fin {
		return t.finishMessage()
	}
	return nil
}

// finishMessage inflates the collected message and sends it to the backend
// as one plain, freshly masked frame.
func (t *Translator) finishMessage() error {
	t.collectFin = false
	plain, err := inflate(t.msg)
	t.msg = t.msg[:0]
	switch {
	case errors.Is(err, errTooBig):
		t.failClient(closeTooBig, err)
		return nil
	case err != nil:
		t.failClient(closeProtocolError, fmt.Errorf("%w: %v", errProtocol, err))
		return nil
	}
	var key [4]byte
	if _, err := rand.Read(key[:]); err != nil {
		return err
	}
	frame := appendHeader(make([]byte, 0, 14+len(plain)), finBit|t.msgOpcode, int64(len(plain)), &key)
	start := len(frame)
	frame = append(frame, plain...)
	for i := range plain {
		frame[start+i] ^= key[i&3]
	}
	_, err = t.backend.Write(frame)
	return err
}

func inflate(compressed []byte) ([]byte, error) {
	src := io.MultiReader(bytes.NewReader(compressed), bytes.NewReader(inflateTail))
	r, _ := inflaters.Get().(io.ReadCloser)
	if r == nil {
		r = flate.NewReader(src)
	} else {
		_ = r.(flate.Resetter).Reset(src, nil)
	}
	defer inflaters.Put(r)
	var out bytes.Buffer
	n, err := out.ReadFrom(io.LimitReader(r, MaxInflatedMessage+1))
	if err != nil {
		return nil, err
	}
	if n > MaxInflatedMessage {
		return nil, errTooBig
	}
	return out.Bytes(), nil
}

// ---- Framing ----

type header struct {
	fin    bool
	rsv    byte
	opcode byte
	masked bool
	length int64
	mask   [4]byte
}

// parseHeader decodes a frame header from the start of b, reporting false
// when b does not yet hold all of it.
func parseHeader(b []byte) (header, int, bool) {
	if len(b) < 2 {
		return header{}, 0, false
	}
	h := header{
		fin:    b[0]&finBit != 0,
		rsv:    b[0] & rsvBits,
		opcode: b[0] & 0x0f,
		masked: b[1]&maskBit != 0,
	}
	n := 2
	switch l := b[1] &^ maskBit; l {
	case 126:
		if len(b) < n+2 {
			return header{}, 0, false
		}
		h.length = int64(binary.BigEndian.Uint16(b[n:]))
		n += 2
	case 127:
		if len(b) < n+8 {
			return header{}, 0, false
		}
		// RFC 6455 requires the most significant bit to be zero; a set bit
		// decodes as a negative length, which every caller rejects.
		h.length = int64(binary.BigEndian.Uint64(b[n:]))
		n += 8
	default:
		h.length = int64(l)
	}
	if h.masked {
		if len(b) < n+4 {
			return header{}, 0, false
		}
		copy(h.mask[:], b[n:n+4])
		n += 4
	}
	return h, n, true
}

// readHeader reads one frame header, returning it with its raw bytes.
func readHeader(r *bufio.Reader) (header, []byte, error) {
	raw := make([]byte, 2, 14)
	if _, err := io.ReadFull(r, raw); err != nil {
		return header{}, nil, err
	}
	extra := 0
	switch raw[1] &^ maskBit {
	case 126:
		extra = 2
	case 127:
		extra = 8
	}
	if raw[1]&maskBit != 0 {
		extra += 4
	}
	raw = raw[:2+extra]
	if _, err := io.ReadFull(r, raw[2:]); err != nil {
		if err == io.EOF {
			err = io.ErrUnexpectedEOF
		}
		return header{}, nil, err
	}
	h, _, _ := parseHeader(raw)
	return h, raw, nil
}

// appendHeader encodes a frame header, masked when key is non-nil.
func appendHeader(b []byte, first byte, length int64, key *[4]byte) []byte {
	var maskFlag byte
	if key != nil {
		maskFlag = maskBit
	}
	switch {
	case length <= 125:
		b = append(b, first, maskFlag|byte(length))
	case length <= 0xffff:
		b = append(b, first, maskFlag|126)
		b = binary.BigEndian.AppendUint16(b, uint16(length))
	default:
		b = append(b, first, maskFlag|127)
		b = binary.BigEndian.AppendUint64(b, uint64(length))
	}
	if key != nil {
		b = append(b, key[:]...)
	}
	return b
}

// closeFrame builds an unmasked server-to-client close frame.
func closeFrame(code uint16) []byte {
	return []byte{finBit | opClose, 2, byte(code >> 8), byte(code)}
}
