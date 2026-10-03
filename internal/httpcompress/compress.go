// Package httpcompress gzip-encodes HTTP responses for clients that accept it.
//
// It sits at the root of the server so one policy covers every surface: the
// dashboard's own assets, the API, ShinyHub's status pages, and everything the
// reverse proxy relays from applications. Shiny backends (httpuv, uvicorn)
// serve their HTML, JavaScript and CSS uncompressed, and a page's dependency
// set is routinely over a megabyte of text that gzip shrinks four-fold.
//
// The policy is conservative by construction: a response is compressed only
// when every condition holds, and any doubt passes it through untouched.
// WebSocket upgrades and HEAD requests never see the wrapper at all, and a
// partial (206) response is always sent as the identity bytes it ranges over.
package httpcompress

import (
	"compress/gzip"
	"maps"
	"net/http"
	"strconv"
	"strings"
	"sync"
)

// MinSize is the smallest body worth compressing. Below it the gzip framing
// and the CPU spent outweigh the bytes saved.
const MinSize = 1024

var gzipWriters = sync.Pool{New: func() any {
	w, _ := gzip.NewWriterLevel(nil, gzip.DefaultCompression)
	return w
}}

// Handler wraps next so that eligible responses are gzip-encoded for clients
// that accept it.
func Handler(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodHead || isUpgrade(r) {
			next.ServeHTTP(w, r)
			return
		}
		cw := &writer{rw: w, accepts: acceptsGzip(r.Header.Values("Accept-Encoding"))}
		// Deliberately not deferred: on a panic the recovery middleware above
		// writes its own response, and flushing a half-built body first would
		// corrupt it.
		next.ServeHTTP(cw, r)
		cw.finish()
	})
}

// writer defers the compress-or-not decision until it has seen the response
// headers and either a declared length or MinSize bytes of body, so a small
// body of unknown length is never wrapped in gzip framing it does not need.
// The decision is final once made; everything after it streams.
type writer struct {
	rw      http.ResponseWriter
	accepts bool

	status  int
	headers http.Header
	decided bool
	gz      *gzip.Writer
	pending []byte
}

func (w *writer) Header() http.Header { return w.rw.Header() }

// Unwrap exposes the underlying writer to http.ResponseController, so
// deadline changes made through the wrapper reach the connection.
func (w *writer) Unwrap() http.ResponseWriter { return w.rw }

func (w *writer) WriteHeader(code int) {
	if w.decided || w.status != 0 {
		// Let net/http report the superfluous call exactly as it would unwrapped.
		if w.decided {
			w.rw.WriteHeader(code)
		}
		return
	}
	if code >= 100 && code < 200 {
		// Informational responses (103 Early Hints) go out immediately and do
		// not end the header phase.
		w.rw.WriteHeader(code)
		return
	}
	w.status = code
	w.headers = w.Header().Clone()
}

func (w *writer) Write(p []byte) (int, error) {
	if w.decided {
		if w.gz != nil {
			return w.gz.Write(p)
		}
		return w.rw.Write(p)
	}
	if w.status == 0 {
		w.WriteHeader(http.StatusOK)
	}
	if _, typed := w.headers["Content-Type"]; !typed && len(w.pending)+len(p) > 0 {
		// net/http sniffs the type of an untyped body from its first bytes.
		// Doing it here keeps that behavior and lets the type decide
		// eligibility; an empty write has nothing to sniff, so it waits.
		sniff := append(w.pending[:len(w.pending):len(w.pending)], p...)
		w.headers.Set("Content-Type", http.DetectContentType(sniff))
	}
	if declaredLength(w.headers) >= 0 {
		// A declared length already answers the size question; no need to
		// hold bytes back.
		if err := w.decide(false); err != nil {
			return 0, err
		}
		return w.Write(p)
	}
	w.pending = append(w.pending, p...)
	if len(w.pending) >= MinSize {
		if err := w.decide(false); err != nil {
			return 0, err
		}
	}
	return len(p), nil
}

// Flush forces the decision, so a streaming handler's bytes leave now rather
// than waiting for MinSize.
func (w *writer) Flush() { _ = w.FlushError() }

func (w *writer) FlushError() error {
	if !w.decided {
		if w.status == 0 {
			w.WriteHeader(http.StatusOK)
		}
		if err := w.decide(false); err != nil {
			return err
		}
	}
	if w.gz != nil {
		if err := w.gz.Flush(); err != nil {
			return err
		}
	}
	return http.NewResponseController(w.rw).Flush()
}

// finish completes the response after the handler returns.
func (w *writer) finish() {
	if !w.decided && w.status == 0 && len(w.pending) == 0 {
		// Nothing was written; net/http sends its implicit empty 200.
		return
	}
	if !w.decided {
		_ = w.decide(true)
	}
	if w.gz != nil {
		_ = w.gz.Close()
		w.gz.Reset(nil)
		gzipWriters.Put(w.gz)
		w.gz = nil
	}
}

// decide commits the headers and releases any pending bytes. final means the
// handler has returned, so the pending bytes are the whole body.
func (w *writer) decide(final bool) error {
	w.decided = true
	h := w.headers
	if w.varies() {
		addVary(h, "Accept-Encoding")
		if w.accepts && w.bigEnough(final) {
			h.Del("Content-Length")
			h.Set("Content-Encoding", "gzip")
			if etag := h.Get("ETag"); strings.HasPrefix(etag, `"`) {
				// The encoded bytes are a different representation, so a
				// strong validator would be a false claim of byte identity.
				h.Set("ETag", "W/"+etag)
			}
			w.gz = gzipWriters.Get().(*gzip.Writer)
			w.gz.Reset(w.rw)
		}
	}
	// Send the headers captured at the first final WriteHeader (or Write),
	// then restore the handler's live map so declared and late trailers keep
	// their values. Ordinary late mutations must not affect the wire headers.
	live := w.rw.Header()
	saved := maps.Clone(live)
	replaceHeader(live, h)
	w.rw.WriteHeader(w.status)
	replaceHeader(live, saved)
	pending := w.pending
	w.pending = nil
	if len(pending) == 0 {
		return nil
	}
	var err error
	if w.gz != nil {
		_, err = w.gz.Write(pending)
	} else {
		_, err = w.rw.Write(pending)
	}
	return err
}

func replaceHeader(dst, src http.Header) {
	clear(dst)
	for key, values := range src {
		dst[key] = values
	}
}

// varies reports whether this response's encoding depends on the request's
// Accept-Encoding, which is exactly when it would be compressed for a client
// that accepts gzip and is large enough.
func (w *writer) varies() bool {
	switch {
	case w.status < 200, w.status == http.StatusNoContent,
		w.status == http.StatusPartialContent, w.status == http.StatusNotModified:
		return false
	}
	h := w.headers
	for _, enc := range h.Values("Content-Encoding") {
		if enc != "" && !strings.EqualFold(enc, "identity") {
			return false
		}
	}
	if hasToken(h.Values("Cache-Control"), "no-transform") {
		return false
	}
	return compressible(h.Get("Content-Type"))
}

func (w *writer) bigEnough(final bool) bool {
	if n := declaredLength(w.headers); n >= 0 {
		return n >= MinSize
	}
	return !final || len(w.pending) >= MinSize
}

// declaredLength returns the Content-Length the handler set, or -1.
func declaredLength(h http.Header) int64 {
	v := h.Get("Content-Length")
	if v == "" {
		return -1
	}
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil || n < 0 {
		return -1
	}
	return n
}

// compressibleTypes is the explicit allowlist beyond text/*. Formats that are
// already compressed (images, woff2, zip, parquet) gain nothing and are left
// out by being absent.
var compressibleTypes = map[string]bool{
	"application/javascript":        true,
	"application/x-javascript":      true,
	"application/ecmascript":        true,
	"application/json":              true,
	"application/manifest+json":     true,
	"application/ld+json":           true,
	"application/geo+json":          true,
	"application/problem+json":      true,
	"application/xml":               true,
	"application/xhtml+xml":         true,
	"application/rss+xml":           true,
	"application/atom+xml":          true,
	"application/wasm":              true,
	"application/vnd.ms-fontobject": true,
	"image/svg+xml":                 true,
	"image/x-icon":                  true,
	"image/vnd.microsoft.icon":      true,
	"font/ttf":                      true,
	"font/otf":                      true,
}

func compressible(contentType string) bool {
	mt, _, _ := strings.Cut(contentType, ";")
	mt = strings.ToLower(strings.TrimSpace(mt))
	if mt == "text/event-stream" {
		// Server-sent events are consumed incrementally; buffering them behind
		// a compressor's window would delay every event.
		return false
	}
	return strings.HasPrefix(mt, "text/") || compressibleTypes[mt]
}

// acceptsGzip applies RFC 9110 content negotiation: an explicit gzip entry
// decides, otherwise a wildcard does, and q=0 means "not acceptable".
func acceptsGzip(values []string) bool {
	gzipQ, starQ := -1.0, -1.0
	for _, v := range values {
		for _, part := range strings.Split(v, ",") {
			name, params, _ := strings.Cut(part, ";")
			q := qValue(params)
			switch strings.ToLower(strings.TrimSpace(name)) {
			case "gzip", "x-gzip":
				gzipQ = q
			case "*":
				starQ = q
			}
		}
	}
	if gzipQ >= 0 {
		return gzipQ > 0
	}
	return starQ > 0
}

func qValue(params string) float64 {
	for _, p := range strings.Split(params, ";") {
		k, v, ok := strings.Cut(strings.TrimSpace(p), "=")
		if ok && strings.EqualFold(strings.TrimSpace(k), "q") {
			q, err := strconv.ParseFloat(strings.TrimSpace(v), 64)
			if err != nil || q < 0 {
				return 0
			}
			return q
		}
	}
	return 1
}

func isUpgrade(r *http.Request) bool {
	return hasToken(r.Header.Values("Connection"), "upgrade")
}

// hasToken reports whether a comma-separated header contains token.
func hasToken(values []string, token string) bool {
	for _, v := range values {
		for _, t := range strings.Split(v, ",") {
			t, _, _ = strings.Cut(t, "=")
			if strings.EqualFold(strings.TrimSpace(t), token) {
				return true
			}
		}
	}
	return false
}

func addVary(h http.Header, field string) {
	for _, v := range h.Values("Vary") {
		for _, t := range strings.Split(v, ",") {
			t = strings.TrimSpace(t)
			if t == "*" || strings.EqualFold(t, field) {
				return
			}
		}
	}
	h.Add("Vary", field)
}
