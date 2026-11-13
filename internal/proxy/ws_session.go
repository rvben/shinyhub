package proxy

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/rvben/shinyhub/internal/rawquery"
	"github.com/rvben/shinyhub/internal/tracing"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
)

// WSEndKind is the bounded outcome exported in WebSocket close spans and logs.
type WSEndKind string

const (
	WSEndNormal           WSEndKind = "normal"
	WSEndRestart          WSEndKind = "restart"
	WSEndKeepaliveTimeout WSEndKind = "keepalive_timeout"
	WSEndUpstreamError    WSEndKind = "upstream_error"
	WSEndTransportError   WSEndKind = "transport_error"
	WSEndClientGone       WSEndKind = "client_gone"
	WSEndProxyStop        WSEndKind = "proxy_stop"
	WSEndUnknown          WSEndKind = "unknown"

	wsKeepaliveTimeoutReason = "keepalive ping timeout"
)

// WSSessionEnd describes one successfully hijacked WebSocket tunnel. ClosedBy
// identifies a close-frame sender or a known proxy action; transport EOF alone
// leaves it unknown. TransportEndSide records the first side whose read ended.
// CloseReason retains bounded raw protocol text for classification only; never
// export it to logs or traces. EndKind is the corresponding bounded outcome.
type WSSessionEnd struct {
	Correlation                                                            tracing.Correlation
	HandshakeCorrelation                                                   tracing.Correlation
	Slug, ConnectionID, ClosedBy, EndSignal, TransportEndSide, CloseReason string
	EndKind                                                                WSEndKind
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
	return rawquery.Delete(raw, "shinyhub_cid")
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
	e.EndKind, e.Abnormal = classifyWSEnd(e)
	return e, s.onEnd
}

// classifyWSEnd distinguishes expected lifecycle endings from upstream failures.
// A keepalive timeout identifies a missed heartbeat, not which peer caused it.
func classifyWSEnd(e WSSessionEnd) (kind WSEndKind, abnormal bool) {
	if e.CloseCode != nil {
		switch *e.CloseCode {
		case 1000, 1001:
			return WSEndNormal, false
		case 1012:
			return WSEndRestart, false
		}
	}
	if e.ClosedBy == "upstream" || e.ClosedBy == "unknown" && e.TransportEndSide == "upstream" {
		if e.CloseCode == nil {
			if e.ClosedBy == "unknown" {
				return WSEndTransportError, true
			}
			return WSEndUpstreamError, true
		}
		if *e.CloseCode == 1011 && e.CloseReason == wsKeepaliveTimeoutReason {
			return WSEndKeepaliveTimeout, false
		}
		return WSEndUpstreamError, true
	}
	if e.ClosedBy == "client" || e.ClosedBy == "unknown" && e.TransportEndSide == "client" {
		return WSEndClientGone, false
	}
	if e.ClosedBy != "" && e.ClosedBy != "unknown" {
		return WSEndProxyStop, false
	}
	return WSEndUnknown, false
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
		// Preserve the original bytes for exact classification. Sanitizing here
		// could turn a different or malformed reason into the known timeout.
		// Close text is bounded by the control-frame limit and never exported.
		onClose(&code, string(data[2:]))
		f.seenClose = true
	} else if f.opcode == 8 && !f.seenClose && f.frameLen == 0 {
		onClose(nil, "")
		f.seenClose = true
	}
	f.have, f.need, f.remaining, f.frameLen, f.closeN = 0, 0, 0, 0, 0
}

// Record a completed lifecycle event in its own trace. Linking to the handshake
// preserves correlation without holding a span open or appending to a trace
// that the collector may have finished processing hours ago. Session duration
// is an attribute, never the span's duration or an HTTP latency measurement.
func recordWSCloseSpan(tr trace.Tracer, handshake trace.SpanContext, e WSSessionEnd) tracing.Correlation {
	opts := []trace.SpanStartOption{trace.WithSpanKind(trace.SpanKindInternal)}
	if handshake.IsValid() {
		opts = append(opts, trace.WithLinks(trace.Link{SpanContext: handshake}))
	}
	_, span := tr.Start(context.Background(), tracing.WSCloseSpanName, opts...)
	defer span.End()
	span.SetAttributes(
		attribute.String("shinyhub.app.slug", e.Slug),
		attribute.Int("shinyhub.replica", e.ReplicaIndex),
		attribute.Int64("shinyhub.deployment.id", e.DeploymentID),
		attribute.Int64("shinyhub.ws.bytes_to_client", e.BytesToClient),
		attribute.Int64("shinyhub.ws.bytes_to_upstream", e.BytesToUpstream),
		attribute.String("shinyhub.ws.closed_by", e.ClosedBy),
		attribute.String("shinyhub.ws.end_signal", e.EndSignal),
		attribute.String("shinyhub.ws.transport_end_side", e.TransportEndSide),
		attribute.Bool("shinyhub.ws.abnormal", e.Abnormal),
		attribute.String("shinyhub.ws.end_kind", string(e.EndKind)),
		attribute.Float64("shinyhub.ws.session_duration_seconds", e.Duration.Seconds()),
	)
	if e.CloseCode != nil {
		span.SetAttributes(attribute.Int("shinyhub.ws.close_code", int(*e.CloseCode)))
	}
	// Close reasons are app-controlled text and may contain sensitive data.
	if e.Abnormal {
		span.SetStatus(codes.Error, "abnormal websocket termination")
	}
	sc := span.SpanContext()
	if !sc.IsValid() {
		return tracing.Correlation{}
	}
	return tracing.Correlation{TraceID: sc.TraceID().String(), SpanID: sc.SpanID().String(), Sampled: sc.IsSampled()}
}
