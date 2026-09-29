package proxy

import (
	"crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"
)

// WSSessionEnd describes one successfully hijacked WebSocket tunnel. ClosedBy
// identifies a close-frame sender or a known proxy action; transport EOF alone
// leaves it unknown. TransportEndSide records the first side whose read ended.
type WSSessionEnd struct {
	Slug, ConnectionID, ClosedBy, EndSignal, TransportEndSide, CloseReason string
	ReplicaIndex                                                           int
	DeploymentID                                                           int64
	Duration                                                               time.Duration
	CloseCode                                                              *uint16
	BytesToClient                                                          int64
	BytesToUpstream                                                        int64
	Abnormal                                                               bool
}

type wsEndFn func(WSSessionEnd)

// wsSession is shared by the two sides of one tunnel. The frame scanners only
// retain headers and at most 125 close-payload bytes, never application data.
type wsSession struct {
	mu              sync.Mutex
	started         time.Time
	slug            string
	connectionID    string
	replica         int
	deploymentID    int64
	clientFrames    wsFrameScanner
	upstreamFrames  wsFrameScanner
	closedBy        string
	transportEnd    string
	proxyReason     string
	closeCode       *uint16
	closeReason     string
	bytesToClient   int64
	bytesToUpstream int64
	onEnd           wsEndFn
	backendAttached bool
	backendClosed   bool
	clientClosed    bool
	clientClosing   bool
	activeIO        int
	emitted         bool
}

func (s *wsSession) start(slug string, replica int, deploymentID int64, fn wsEndFn) {
	s.mu.Lock()
	s.started = time.Now()
	s.slug, s.replica, s.deploymentID, s.onEnd = slug, replica, deploymentID, fn
	if s.connectionID == "" {
		s.connectionID = newWSConnectionID()
	}
	s.mu.Unlock()
}

// A browser-generated ID is accepted only in the same fixed format that the
// injected overlay produces. Untagged clients receive a server-generated ID.
func wsConnectionID(raw string) string {
	if len(raw) != 24 {
		return newWSConnectionID()
	}
	for _, ch := range raw {
		if !(ch >= '0' && ch <= '9' || ch >= 'a' && ch <= 'f') {
			return newWSConnectionID()
		}
	}
	return raw
}

// Remove only ShinyHub's query component. Re-encoding the other components
// could change an app's signed or otherwise byte-sensitive WebSocket URL.
func stripWSConnectionID(raw string) string {
	parts := strings.Split(raw, "&")
	keep := parts[:0]
	for _, part := range parts {
		key, _, _ := strings.Cut(part, "=")
		decoded, err := url.QueryUnescape(key)
		if err == nil && decoded == "shinyhub_cid" {
			continue
		}
		keep = append(keep, part)
	}
	return strings.Join(keep, "&")
}

func newWSConnectionID() string {
	var b [12]byte
	_, _ = rand.Read(b[:]) // crypto/rand.Read is guaranteed not to fail on supported Go versions.
	return hex.EncodeToString(b[:])
}

func (s *wsSession) setProxyReason(reason string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.clientClosing && !s.emitted && s.proxyReason == "" && s.closedBy == "" && s.transportEnd == "" {
		s.proxyReason = reason
		return true
	}
	return false
}

func (s *wsSession) beginClientClose(reason string) {
	s.mu.Lock()
	s.clientClosing = true
	if reason != "" && s.proxyReason == "" && s.closedBy == "" && s.transportEnd == "" {
		s.proxyReason = reason
	}
	s.mu.Unlock()
}

func (s *wsSession) clearProxyReason(reason string) {
	s.mu.Lock()
	if !s.emitted && s.proxyReason == reason {
		s.proxyReason = ""
	}
	s.mu.Unlock()
}

func (s *wsSession) observeRead(side string, p []byte, err error) {
	s.mu.Lock()
	if s.emitted {
		s.mu.Unlock()
		return
	}
	if len(p) > 0 {
		var scanner *wsFrameScanner
		if side == "client" {
			scanner = &s.clientFrames
		} else {
			scanner = &s.upstreamFrames
		}
		scanner.feed(p, side == "client", func(code *uint16, reason string) {
			// A close frame is stronger evidence than an earlier transport EOF.
			if s.closeCode == nil && s.closeReason == "" && !sawClose(s) {
				s.closedBy, s.closeCode, s.closeReason = side, code, reason
				s.clientFrames.seenClose = true
				s.upstreamFrames.seenClose = true
			}
		})
	}
	if err != nil && s.transportEnd == "" {
		s.transportEnd = side
	}
	s.mu.Unlock()
}

func sawClose(s *wsSession) bool { return s.clientFrames.seenClose || s.upstreamFrames.seenClose }

func (s *wsSession) beginIO() {
	s.mu.Lock()
	s.activeIO++
	s.mu.Unlock()
}

func (s *wsSession) finishIO(side string, n int, err error, read bool, p []byte) {
	if read {
		s.observeRead(side, p, err)
	}
	s.mu.Lock()
	if !read {
		if side == "client" {
			s.bytesToClient += int64(n)
		} else {
			s.bytesToUpstream += int64(n)
		}
	}
	s.activeIO--
	e, fn := s.readyToEmitLocked()
	s.mu.Unlock()
	if fn != nil {
		fn(e)
	}
}

func (s *wsSession) end() {
	s.mu.Lock()
	s.clientClosed = true
	e, fn := s.readyToEmitLocked()
	s.mu.Unlock()
	if fn != nil {
		fn(e)
	}
}

func (s *wsSession) attachBackend() {
	s.mu.Lock()
	s.backendAttached = true
	s.mu.Unlock()
}

func (s *wsSession) closeBackend() {
	s.mu.Lock()
	s.backendClosed = true
	e, fn := s.readyToEmitLocked()
	s.mu.Unlock()
	if fn != nil {
		fn(e)
	}
}

func (s *wsSession) readyToEmitLocked() (WSSessionEnd, wsEndFn) {
	if s.emitted || !s.clientClosed || s.backendAttached && !s.backendClosed || s.activeIO != 0 {
		return WSSessionEnd{}, nil
	}
	s.emitted = true
	closedBy, signal := s.closedBy, "close_frame"
	if closedBy == "" && s.proxyReason != "" {
		closedBy, signal = s.proxyReason, "proxy_action"
	} else if closedBy == "" {
		closedBy, signal = "unknown", "unknown"
		if s.transportEnd != "" {
			signal = "transport_end"
		}
	}
	e := WSSessionEnd{
		Slug: s.slug, ConnectionID: s.connectionID, ReplicaIndex: s.replica, DeploymentID: s.deploymentID,
		ClosedBy: closedBy, EndSignal: signal, TransportEndSide: s.transportEnd,
		CloseCode: s.closeCode, CloseReason: s.closeReason,
		Duration: time.Since(s.started), BytesToClient: s.bytesToClient,
		BytesToUpstream: s.bytesToUpstream,
	}
	e.Abnormal = (closedBy == "upstream" || closedBy == "unknown" && s.transportEnd == "upstream") &&
		(e.CloseCode == nil || *e.CloseCode != 1000 && *e.CloseCode != 1001)
	return e, s.onEnd
}

// observeWSTunnel wraps the backend body after any compression translation.
// It preserves ReadWriteCloser, as required by ReverseProxy's upgrade path.
func (p *Proxy) observeWSTunnel(resp *http.Response) error {
	if resp.StatusCode != http.StatusSwitchingProtocols || resp.Request == nil ||
		!strings.EqualFold(resp.Header.Get("Upgrade"), "websocket") {
		return nil
	}
	s, _ := resp.Request.Context().Value(wsSessionContextKey{}).(*wsSession)
	if s == nil {
		return nil
	}
	backend, ok := resp.Body.(io.ReadWriteCloser)
	if !ok {
		return nil
	}
	s.attachBackend()
	wrapped := &wsBackendConn{ReadWriteCloser: backend, session: s}
	if cw, ok := backend.(interface{ CloseWrite() error }); ok {
		resp.Body = &wsBackendConnWithCloseWrite{wsBackendConn: wrapped, closeWrite: cw.CloseWrite}
	} else {
		resp.Body = wrapped
	}
	return nil
}

type wsSessionContextKey struct{}

type wsBackendConn struct {
	io.ReadWriteCloser
	session *wsSession
}

// Preserve the optional half-close capability without claiming it for
// backends that never had it. ReverseProxy checks this interface directly.
type wsBackendConnWithCloseWrite struct {
	*wsBackendConn
	closeWrite func() error
}

func (c *wsBackendConnWithCloseWrite) CloseWrite() error { return c.closeWrite() }

func (c *wsBackendConn) Read(p []byte) (int, error) {
	c.session.beginIO()
	n, err := c.ReadWriteCloser.Read(p)
	c.session.finishIO("upstream", n, err, true, p[:n])
	return n, err
}

func (c *wsBackendConn) Write(p []byte) (int, error) {
	c.session.beginIO()
	n, err := c.ReadWriteCloser.Write(p)
	c.session.finishIO("upstream", n, err, false, nil)
	return n, err
}

func (c *wsBackendConn) Close() error {
	err := c.ReadWriteCloser.Close()
	c.session.closeBackend()
	return err
}

// wsFrameScanner skips data payloads without retaining them. It tolerates
// arbitrary Read boundaries and records only the first complete close frame.
type wsFrameScanner struct {
	header    [14]byte
	have      int
	need      int
	remaining uint64
	frameLen  uint64
	opcode    byte
	masked    bool
	mask      [4]byte
	closeData [125]byte
	closeN    int
	seenClose bool
	invalid   bool
}

func (f *wsFrameScanner) feed(p []byte, expectMask bool, onClose func(*uint16, string)) {
	if f.invalid {
		return
	}
	for len(p) > 0 {
		if f.remaining > 0 {
			n := uint64(len(p))
			if n > f.remaining {
				n = f.remaining
			}
			if f.opcode == 8 && f.closeN < len(f.closeData) {
				copyN := int(n)
				if copyN > len(f.closeData)-f.closeN {
					copyN = len(f.closeData) - f.closeN
				}
				copy(f.closeData[f.closeN:], p[:copyN])
				f.closeN += copyN
			}
			p = p[int(n):]
			f.remaining -= n
			if f.remaining == 0 {
				f.finish(onClose)
			}
			continue
		}
		if f.need == 0 {
			f.need = 2
		}
		n := f.need - f.have
		if n > len(p) {
			n = len(p)
		}
		copy(f.header[f.have:], p[:n])
		f.have += n
		p = p[n:]
		if f.have < f.need {
			continue
		}
		if f.need == 2 {
			f.opcode = f.header[0] & 0x0f
			f.masked = f.header[1]&0x80 != 0
			control := f.opcode >= 8
			if f.masked != expectMask || f.header[0]&0x30 != 0 ||
				(control && (f.header[0]&0x40 != 0 || f.header[0]&0x80 == 0 || f.header[1]&0x7f > 125)) ||
				(f.opcode > 2 && f.opcode < 8) || f.opcode > 10 {
				f.invalid = true
				return
			}
			f.need = 2
			switch f.header[1] & 0x7f {
			case 126:
				f.need += 2
			case 127:
				f.need += 8
			}
			if f.masked {
				f.need += 4
			}
			if f.have < f.need {
				continue
			}
		}
		length := uint64(f.header[1] & 0x7f)
		offset := 2
		if length == 126 {
			length = uint64(binary.BigEndian.Uint16(f.header[offset:]))
			offset += 2
		} else if length == 127 {
			length = binary.BigEndian.Uint64(f.header[offset:])
			offset += 8
		}
		if f.masked {
			copy(f.mask[:], f.header[offset:offset+4])
		}
		f.remaining, f.frameLen, f.closeN = length, length, 0
		if length == 0 {
			f.finish(onClose)
		}
	}
}

func (f *wsFrameScanner) finish(onClose func(*uint16, string)) {
	if f.opcode == 8 && !f.seenClose && f.frameLen <= 125 && f.closeN >= 2 {
		data := f.closeData[:f.closeN]
		if f.masked {
			for i := range data {
				data[i] ^= f.mask[i%4]
			}
		}
		code := binary.BigEndian.Uint16(data[:2])
		reason := string(data[2:])
		if !utf8.ValidString(reason) {
			reason = strings.ToValidUTF8(reason, "")
		}
		reason = strings.Map(func(r rune) rune {
			if unicode.IsControl(r) {
				return -1
			}
			return r
		}, reason)
		onClose(&code, reason)
		f.seenClose = true
	} else if f.opcode == 8 && !f.seenClose && f.frameLen == 0 {
		onClose(nil, "")
		f.seenClose = true
	}
	f.have, f.need, f.remaining, f.frameLen, f.closeN = 0, 0, 0, 0, 0
}
