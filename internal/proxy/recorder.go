package proxy

import (
	"bufio"
	"fmt"
	"io"
	"net"
	"net/http"
)

// statusRecorder wraps an http.ResponseWriter to capture the status code and
// response size for access logging while preserving every optional capability
// of the underlying writer. It delegates:
//
//   - Flusher: for httputil.ReverseProxy's streaming responses and SSE.
//   - Hijacker: for WebSocket upgrades routed through the reverse proxy.
//   - ReaderFrom: for the zero-copy fast path (sendfile on Linux) that
//     net/http uses when the body is read from an *os.File or TCP conn.
//
// status is 0 until WriteHeader or Write is called. Write without a prior
// WriteHeader records 200, matching net/http's implicit behaviour. A final
// status of 0 means no response was produced (typically a panic higher up
// the stack that was recovered elsewhere).
type statusRecorder struct {
	http.ResponseWriter
	status      int
	bytes       int64
	wroteHeader bool
	// proxyErr holds the upstream transport error captured by the reverse
	// proxy's ErrorHandler (connection refused, timeout, mid-stream drop) so
	// the trace span can surface it. Nil on success.
	proxyErr error
	// onUpgrade, when non-nil, fires exactly once at the moment a genuine
	// WebSocket/protocol-switch upgrade is observed. On this Go toolchain
	// httputil.ReverseProxy's upgrade path (handleUpgradeResponse) hijacks the
	// connection FIRST and writes the 101 status line directly to the
	// hijacked bufio.Writer afterwards - it never calls rw.WriteHeader(101).
	// So the real signal is Hijack succeeding, not WriteHeader(101); see the
	// Hijack method below. The WriteHeader(101) branch is kept as a defensive
	// fallback for callers/toolchains that do write the status explicitly
	// before hijacking, and to keep this hook observable without waiting for
	// the hijacked goroutine to finish - which it never does until the
	// client disconnects.
	onUpgrade func()
	// onHandshake fires after the 101 response headers have been flushed to
	// the client. Hijack alone precedes that write in ReverseProxy.
	onHandshake func()
	// trackHijack, when non-nil, wraps the hijacked connection so the proxy can
	// track its lifetime for graceful drain on shutdown. It returns the conn to
	// hand back to the hijacking caller (httputil.ReverseProxy's upgrade path).
	trackHijack func(net.Conn) net.Conn
	// rejectReason, when set by recordReject, is the platform rejection reason
	// for this request. The ServeHTTP access-log defer copies it into
	// AccessLogEntry.Reject. Empty for non-rejected requests.
	rejectReason RejectReason
}

func newStatusRecorder(w http.ResponseWriter) *statusRecorder {
	return &statusRecorder{ResponseWriter: w}
}

func (r *statusRecorder) WriteHeader(code int) {
	if !r.wroteHeader {
		r.status = code
		r.wroteHeader = true
		if code == http.StatusSwitchingProtocols && r.onUpgrade != nil {
			r.onUpgrade()
		}
	}
	r.ResponseWriter.WriteHeader(code)
}

func (r *statusRecorder) Write(b []byte) (int, error) {
	if !r.wroteHeader {
		r.status = http.StatusOK
		r.wroteHeader = true
	}
	n, err := r.ResponseWriter.Write(b)
	r.bytes += int64(n)
	return n, err
}

// Flush forwards to the underlying writer's Flusher if present. Required for
// httputil.ReverseProxy to stream responses (SSE, chunked) correctly.
func (r *statusRecorder) Flush() {
	if f, ok := r.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// Hijack forwards to the underlying writer's Hijacker. Required for WebSocket
// upgrades routed through the reverse proxy.
//
// This is the authoritative upgrade signal, not WriteHeader(101):
// httputil.ReverseProxy's upgrade path (handleUpgradeResponse) hijacks the
// connection before writing anything, then writes the 101 status line
// directly to the hijacked bufio.Writer - rw.WriteHeader is never called.
// A successful Hijack reached through this proxy's request path only ever
// happens via that upgrade handling, which itself only runs when the
// backend answered 101 (see the res.StatusCode == http.StatusSwitchingProtocols
// gate in reverseproxy.go), so treating Hijack success as the upgrade signal
// cannot false-positive on a non-upgrade response.
//
// We only do this when nothing was written yet (!wroteHeader): if a caller
// explicitly wrote a status before hijacking (e.g. for a non-WS protocol
// takeover), that status is preserved and this hijack is not mistaken for a
// WS handshake.
func (r *statusRecorder) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	h, ok := r.ResponseWriter.(http.Hijacker)
	if !ok {
		return nil, nil, fmt.Errorf("proxy: underlying ResponseWriter does not support hijacking")
	}
	conn, rw, err := h.Hijack()
	if err == nil {
		if !r.wroteHeader {
			r.status = http.StatusSwitchingProtocols
			r.wroteHeader = true
			if r.onUpgrade != nil {
				r.onUpgrade()
			}
		}
		if r.trackHijack != nil {
			conn = r.trackHijack(conn)
		}
		if r.status == http.StatusSwitchingProtocols && r.onHandshake != nil {
			rw.Writer = bufio.NewWriter(&upgradeHeaderWriter{writer: rw.Writer, complete: r.onHandshake})
		}
	}
	return conn, rw, err
}

// ReadFrom enables io.Copy's zero-copy fast path. When the underlying
// ResponseWriter supports ReaderFrom (every net/http response does, via
// *response.ReadFrom), we delegate and add the copied byte count to our
// accounting. When it does not, we fall back through Write so bytes are
// still tracked. Using writeOnly prevents io.Copy from re-discovering
// ReaderFrom on the wrapper and recursing.
func (r *statusRecorder) ReadFrom(src io.Reader) (int64, error) {
	if !r.wroteHeader {
		r.status = http.StatusOK
		r.wroteHeader = true
	}
	if rf, ok := r.ResponseWriter.(io.ReaderFrom); ok {
		n, err := rf.ReadFrom(src)
		r.bytes += n
		return n, err
	}
	return io.Copy(writeOnly{Writer: r.ResponseWriter, rec: r}, src)
}

// Unwrap lets http.ResponseController discover the embedded writer.
func (r *statusRecorder) Unwrap() http.ResponseWriter { return r.ResponseWriter }

// writeOnly hides optional interfaces on its embedded Writer so io.Copy
// does not attempt the ReaderFrom / WriterTo fast-paths that would bypass
// the statusRecorder's byte accounting. Write goes through the embedded
// writer directly and updates rec.bytes, so behaviour matches the
// ReaderFrom fast path minus the zero-copy optimisation.
type writeOnly struct {
	io.Writer
	rec *statusRecorder
}

func (w writeOnly) Write(b []byte) (int, error) {
	n, err := w.Writer.Write(b)
	w.rec.bytes += int64(n)
	return n, err
}

var (
	_ http.Flusher  = (*statusRecorder)(nil)
	_ http.Hijacker = (*statusRecorder)(nil)
	_ io.ReaderFrom = (*statusRecorder)(nil)
)

// ReverseProxy writes the upgrade response directly to the hijacked buffer.
// Flush through its original writer before reporting completion. Only headers
// traverse this wrapper; the tunnel subsequently writes to the connection.
type upgradeHeaderWriter struct {
	writer   *bufio.Writer
	complete func()
	matched  int
}

func (w *upgradeHeaderWriter) Write(p []byte) (int, error) {
	n, err := w.writer.Write(p)
	if err == nil {
		err = w.writer.Flush()
	}
	if err != nil || w.complete == nil {
		return n, err
	}
	const delimiter = "\r\n\r\n"
	for _, b := range p[:n] {
		if b == delimiter[w.matched] {
			w.matched++
		} else if b == '\r' {
			w.matched = 1
		} else {
			w.matched = 0
		}
		if w.matched == len(delimiter) {
			fn := w.complete
			w.complete = nil
			fn()
			break
		}
	}
	return n, err
}
