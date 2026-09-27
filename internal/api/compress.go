package api

import (
	"bufio"
	"compress/gzip"
	"net"
	"net/http"
	"strings"

	"github.com/rvben/shinyhub/internal/ui"
)

// jsonCompressMinBytes is the smallest uncompressed body worth gzip-encoding.
// gzip's own framing (a 10-byte header, an 8-byte trailer, per-block sync
// overhead) can make a body smaller than this bigger, not smaller, so
// anything under the threshold is always sent identity. 1024 bytes matches
// the long-standing default used by other frameworks' response-compression
// middleware (e.g. Node's `compression` package).
const jsonCompressMinBytes = 1024

// compressJSON gzip-encodes application/json API responses for a client that
// sent Accept-Encoding: gzip. It is scoped to exactly that one content type,
// checked against the header the handler already set, so every other
// response shape on this router - text/event-stream (SSE log streaming),
// plain-text log tails, anything that already set its own Content-Encoding -
// passes through completely untouched: no buffering, no added latency, no
// behavior change. This is what keeps SSE and (structurally, since this
// middleware only ever runs on the chi router mounted at /api/, never on the
// /app/ reverse proxy or a websocket upgrade, both handled by entirely
// separate handlers in cmd/shinyhub/main.go) every other traffic class
// unaffected.
//
// A response under jsonCompressMinBytes is written identity even when it is
// JSON, so a small error body or a single small object never pays gzip's
// framing cost for no gain.
func compressJSON(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !ui.AcceptsGzip(r) {
			next.ServeHTTP(w, r)
			return
		}
		cw := &jsonCompressWriter{ResponseWriter: w}
		// Deliberately not deferred: if the handler panics, cw is simply
		// abandoned mid-buffer and the panic unwinds to recoverAPI's own
		// recover(), which writes its 500 straight to the real
		// ResponseWriter exactly as it would without this middleware. A
		// deferred Close() here would instead run during that unwind and
		// could commit a premature 200, ahead of recoverAPI's response.
		next.ServeHTTP(cw, r)
		cw.Close()
	})
}

// jsonCompressWriter defers the identity-vs-gzip decision until either
// jsonCompressMinBytes have been buffered or the response turns out not to
// be a compression candidate, because a streamed encoder (json.Encoder,
// which every JSON helper in this package uses) does not know its own body
// size at WriteHeader time.
type jsonCompressWriter struct {
	http.ResponseWriter
	status      int
	wroteHeader bool
	decided     bool
	gzip        bool
	buf         []byte
	gz          *gzip.Writer
}

func (cw *jsonCompressWriter) WriteHeader(status int) {
	if cw.wroteHeader {
		return
	}
	cw.wroteHeader = true
	cw.status = status
}

func (cw *jsonCompressWriter) isJSONCandidate() bool {
	ct, _, _ := strings.Cut(cw.Header().Get("Content-Type"), ";")
	return ct == "application/json" && cw.Header().Get("Content-Encoding") == ""
}

// decide commits the real ResponseWriter's header exactly once, choosing
// gzip when the response is an eligible content type that reached the
// buffering threshold, identity otherwise. Safe to call multiple times;
// only the first call has any effect.
func (cw *jsonCompressWriter) decide() error {
	if cw.decided {
		return nil
	}
	cw.decided = true

	if cw.isJSONCandidate() && len(cw.buf) >= jsonCompressMinBytes {
		cw.gzip = true
		cw.Header().Set("Content-Encoding", "gzip")
		cw.Header().Add("Vary", "Accept-Encoding")
		cw.Header().Del("Content-Length")
		cw.ResponseWriter.WriteHeader(cw.status)
		cw.gz = gzip.NewWriter(cw.ResponseWriter)
		_, err := cw.gz.Write(cw.buf)
		cw.buf = nil
		return err
	}

	cw.ResponseWriter.WriteHeader(cw.status)
	if len(cw.buf) == 0 {
		return nil
	}
	_, err := cw.ResponseWriter.Write(cw.buf)
	cw.buf = nil
	return err
}

func (cw *jsonCompressWriter) Write(p []byte) (int, error) {
	if cw.decided {
		if cw.gzip {
			return cw.gz.Write(p)
		}
		return cw.ResponseWriter.Write(p)
	}
	if !cw.wroteHeader {
		cw.status = http.StatusOK
	}
	cw.buf = append(cw.buf, p...)
	if !cw.isJSONCandidate() || len(cw.buf) >= jsonCompressMinBytes {
		if err := cw.decide(); err != nil {
			return 0, err
		}
	}
	return len(p), nil
}

// Flush forces the identity/gzip decision (using whatever is buffered so
// far, possibly nothing) before flushing, so a handler that flushes before
// its first Write - or between two small writes, neither of which alone
// crosses the threshold - still commits the real ResponseWriter's header
// exactly once. Without this, a Flush ahead of decide() would let the real
// writer's own implicit WriteHeader(200) fire first, and a later decide()
// would then try to send a second, superfluous one.
func (cw *jsonCompressWriter) Flush() {
	if !cw.decided {
		if !cw.wroteHeader {
			cw.status = http.StatusOK
		}
		_ = cw.decide()
	}
	if cw.gz != nil {
		_ = cw.gz.Flush()
	}
	if f, ok := cw.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// Hijack delegates to the real ResponseWriter. Nothing under /api/ actually
// hijacks the connection today, but implementing the interface keeps this
// wrapper transparent to any future handler that checks for it, matching
// the same delegation pattern already used by authFailWriter in this
// package.
func (cw *jsonCompressWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	if hj, ok := cw.ResponseWriter.(http.Hijacker); ok {
		return hj.Hijack()
	}
	return nil, nil, http.ErrNotSupported
}

// Close finalizes the response: if nothing ever decided the encoding
// (handler returned having written nothing at all, or having written and
// flushed already), it commits identity now, and closes the gzip stream
// when gzip was chosen. Called by compressJSON after the handler returns.
func (cw *jsonCompressWriter) Close() error {
	if !cw.decided {
		if !cw.wroteHeader && len(cw.buf) == 0 {
			// Handler never wrote anything; let the real ResponseWriter
			// apply its own default (implicit 200, empty body) exactly as
			// it would without this middleware.
			return nil
		}
		if err := cw.decide(); err != nil {
			return err
		}
	}
	if cw.gz != nil {
		return cw.gz.Close()
	}
	return nil
}
