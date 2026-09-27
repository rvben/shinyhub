package api

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// largeJSONBody returns a JSON array long enough to clear
// jsonCompressMinBytes, with enough repeated structure that gzip actually
// shrinks it (a realistic stand-in for a page of app/metrics rows).
func largeJSONBody() []byte {
	var buf bytes.Buffer
	buf.WriteByte('[')
	for i := 0; i < 60; i++ {
		if i > 0 {
			buf.WriteByte(',')
		}
		buf.WriteString(`{"slug":"demo-app","status":"stopped","replicas_running":0,"cpu_percent":0,"memory_mb":0}`)
	}
	buf.WriteByte(']')
	return buf.Bytes()
}

func gunzip(t *testing.T, b []byte) []byte {
	t.Helper()
	zr, err := gzip.NewReader(bytes.NewReader(b))
	if err != nil {
		t.Fatalf("gzip.NewReader: %v", err)
	}
	defer zr.Close()
	out, err := io.ReadAll(zr)
	if err != nil {
		t.Fatalf("read gzip body: %v", err)
	}
	return out
}

// newCompressedRoundTrip runs handler behind compressJSON on a real HTTP
// server and returns the raw response, with Go's transport-level
// auto-compression disabled so what comes back is exactly what the server
// sent on the wire (no transparent gunzip, no transport-added header).
func doCompressedRequest(t *testing.T, handler http.HandlerFunc, acceptGzip bool) *http.Response {
	t.Helper()
	srv := httptest.NewServer(compressJSON(handler))
	t.Cleanup(srv.Close)

	req, err := http.NewRequest(http.MethodGet, srv.URL, nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	if acceptGzip {
		req.Header.Set("Accept-Encoding", "gzip")
	}
	client := &http.Client{Transport: &http.Transport{DisableCompression: true}}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("do request: %v", err)
	}
	t.Cleanup(func() { resp.Body.Close() })
	return resp
}

func TestCompressJSON_LargeJSONBodyIsGzipped(t *testing.T) {
	body := largeJSONBody()
	handler := func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, json.RawMessage(body))
	}

	resp := doCompressedRequest(t, handler, true)

	if got := resp.Header.Get("Content-Encoding"); got != "gzip" {
		t.Fatalf("Content-Encoding = %q, want gzip", got)
	}
	if !strings.Contains(resp.Header.Get("Vary"), "Accept-Encoding") {
		t.Fatalf("Vary = %q, want it to contain Accept-Encoding", resp.Header.Get("Vary"))
	}
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	got := gunzip(t, raw)
	want := append(append([]byte{}, body...), '\n') // json.Encoder appends a trailing newline
	if !bytes.Equal(got, want) {
		t.Fatalf("decompressed body mismatch:\n got=%s\nwant=%s", got, want)
	}
	if len(raw) >= len(want) {
		t.Fatalf("gzipped body (%d bytes) is not smaller than identity (%d bytes)", len(raw), len(want))
	}
}

func TestCompressJSON_TinyBodyNotCompressed(t *testing.T) {
	handler := func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
	}

	resp := doCompressedRequest(t, handler, true)

	if got := resp.Header.Get("Content-Encoding"); got != "" {
		t.Fatalf("Content-Encoding = %q, want none for a tiny body", got)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	if string(body) != `{"ok":true}`+"\n" {
		t.Fatalf("body = %q, want identity JSON", body)
	}
}

func TestCompressJSON_ClientWithoutAcceptEncodingNotCompressed(t *testing.T) {
	body := largeJSONBody()
	handler := func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, json.RawMessage(body))
	}

	resp := doCompressedRequest(t, handler, false)

	if got := resp.Header.Get("Content-Encoding"); got != "" {
		t.Fatalf("Content-Encoding = %q, want none when the client sent no Accept-Encoding", got)
	}
	got, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	want := append(append([]byte{}, body...), '\n')
	if !bytes.Equal(got, want) {
		t.Fatalf("body mismatch:\n got=%s\nwant=%s", got, want)
	}
}

func TestCompressJSON_NonJSONContentTypeNeverCompressed(t *testing.T) {
	body := largeJSONBody() // shape doesn't matter; only the Content-Type does
	handler := func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(body)
	}

	resp := doCompressedRequest(t, handler, true)

	if got := resp.Header.Get("Content-Encoding"); got != "" {
		t.Fatalf("Content-Encoding = %q, want none for text/event-stream", got)
	}
	got, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	if !bytes.Equal(got, body) {
		t.Fatalf("body mismatch:\n got=%s\nwant=%s", got, body)
	}
}

func TestCompressJSON_AlreadyEncodedResponseUntouched(t *testing.T) {
	body := largeJSONBody()
	handler := func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Content-Encoding", "identity")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(body)
	}

	resp := doCompressedRequest(t, handler, true)

	if got := resp.Header.Get("Content-Encoding"); got != "identity" {
		t.Fatalf("Content-Encoding = %q, want the handler's own \"identity\" preserved", got)
	}
	got, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	if !bytes.Equal(got, body) {
		t.Fatalf("body mismatch:\n got=%s\nwant=%s", got, body)
	}
}

func TestCompressJSON_SSEFlushBeforeFirstWriteCommitsHeaderOnce(t *testing.T) {
	// Mirrors internal/api/logs.go's streamLogReader: it may check for a
	// Flusher and call Flush() before writing any bytes (an empty initial
	// snapshot), then later Write real records. A wrapper that decides the
	// encoding lazily on Write only - never on Flush - lets the real
	// ResponseWriter's own implicit WriteHeader(200) fire during that first
	// Flush, and then calls WriteHeader again itself once the first real
	// Write arrives: a superfluous second call (net/http logs this as
	// "superfluous response.WriteHeader call" and ignores it, so the bug is
	// otherwise invisible to a test that only checks the response body).
	//
	// fakeSSEWriter (not httptest.ResponseRecorder) is used here because
	// ResponseRecorder.Flush's own implicit WriteHeader is an internal
	// same-struct call that bypasses an embedding wrapper's WriteHeader
	// override entirely (Go has no virtual dispatch for embedded methods),
	// which would hide exactly the double-commit this test exists to catch.
	// fakeSSEWriter instead replicates real net/http's self-referential
	// Write/Flush-call-WriteHeader-if-needed behavior directly, so every
	// commit - implicit or explicit - increments the same counter.
	//
	// Confirmed to fail (writeHeaderCalls == 2) with the Flush-time decide()
	// call removed from jsonCompressWriter.Flush.
	fake := &fakeSSEWriter{}

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		_, _ = w.Write([]byte("data: hello\n\n"))
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
	})

	req := httptest.NewRequest(http.MethodGet, "/logs", nil)
	req.Header.Set("Accept-Encoding", "gzip")
	compressJSON(handler).ServeHTTP(fake, req)

	if fake.writeHeaderCalls != 1 {
		t.Fatalf("WriteHeader called %d times, want exactly 1", fake.writeHeaderCalls)
	}
	if got := fake.body.String(); got != "data: hello\n\n" {
		t.Fatalf("body = %q, want the identity SSE payload", got)
	}
}

// fakeSSEWriter is a minimal http.ResponseWriter+http.Flusher whose Write and
// Flush both implicitly call WriteHeader(200) when the header has not been
// sent yet, matching real net/http server semantics (unlike
// httptest.ResponseRecorder, whose equivalent implicit call cannot be
// observed through an embedding wrapper - see the test above).
type fakeSSEWriter struct {
	header           http.Header
	wroteHeader      bool
	writeHeaderCalls int
	body             bytes.Buffer
}

func (f *fakeSSEWriter) Header() http.Header {
	if f.header == nil {
		f.header = make(http.Header)
	}
	return f.header
}

func (f *fakeSSEWriter) WriteHeader(status int) {
	f.writeHeaderCalls++
	f.wroteHeader = true
}

func (f *fakeSSEWriter) Write(p []byte) (int, error) {
	if !f.wroteHeader {
		f.WriteHeader(http.StatusOK)
	}
	return f.body.Write(p)
}

func (f *fakeSSEWriter) Flush() {
	if !f.wroteHeader {
		f.WriteHeader(http.StatusOK)
	}
}
