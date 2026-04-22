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
}

func newStatusRecorder(w http.ResponseWriter) *statusRecorder {
	return &statusRecorder{ResponseWriter: w}
}

func (r *statusRecorder) WriteHeader(code int) {
	if !r.wroteHeader {
		r.status = code
		r.wroteHeader = true
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
// upgrades routed through the reverse proxy. We do not touch r.status here:
// ReverseProxy's upgrade path calls WriteHeader(101) before Hijack, so the
// 101 has already been recorded.
func (r *statusRecorder) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	h, ok := r.ResponseWriter.(http.Hijacker)
	if !ok {
		return nil, nil, fmt.Errorf("proxy: underlying ResponseWriter does not support hijacking")
	}
	return h.Hijack()
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
	_ http.Flusher   = (*statusRecorder)(nil)
	_ http.Hijacker  = (*statusRecorder)(nil)
	_ io.ReaderFrom  = (*statusRecorder)(nil)
)
