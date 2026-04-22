package proxy

import (
	"bufio"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestStatusRecorderInitialState(t *testing.T) {
	rec := newStatusRecorder(httptest.NewRecorder())
	if rec.status != 0 {
		t.Errorf("initial status: expected 0 sentinel, got %d", rec.status)
	}
	if rec.bytes != 0 {
		t.Errorf("initial bytes: expected 0, got %d", rec.bytes)
	}
}

func TestStatusRecorderImplicitOK(t *testing.T) {
	rec := newStatusRecorder(httptest.NewRecorder())
	if _, err := rec.Write([]byte("hi")); err != nil {
		t.Fatal(err)
	}
	if rec.status != http.StatusOK {
		t.Errorf("implicit status: expected 200, got %d", rec.status)
	}
	if rec.bytes != 2 {
		t.Errorf("bytes: expected 2, got %d", rec.bytes)
	}
}

func TestStatusRecorderExplicitStatus(t *testing.T) {
	rec := newStatusRecorder(httptest.NewRecorder())
	rec.WriteHeader(http.StatusTeapot)
	if rec.status != http.StatusTeapot {
		t.Errorf("explicit status: expected 418, got %d", rec.status)
	}
}

func TestStatusRecorderFirstStatusWins(t *testing.T) {
	rec := newStatusRecorder(httptest.NewRecorder())
	rec.WriteHeader(http.StatusBadGateway)
	rec.WriteHeader(http.StatusOK) // second call is a protocol error; net/http logs a warning
	if rec.status != http.StatusBadGateway {
		t.Errorf("first status should win, got %d", rec.status)
	}
}

type flushSpy struct {
	http.ResponseWriter
	flushes int
}

func (f *flushSpy) Flush() { f.flushes++ }

func TestStatusRecorderFlushDelegates(t *testing.T) {
	spy := &flushSpy{ResponseWriter: httptest.NewRecorder()}
	rec := newStatusRecorder(spy)
	rec.Flush()
	rec.Flush()
	if spy.flushes != 2 {
		t.Errorf("expected 2 delegated flushes, got %d", spy.flushes)
	}
}

func TestStatusRecorderFlushNoOpWhenUnsupported(t *testing.T) {
	// httptest.ResponseRecorder is a Flusher, so wrap to strip the interface.
	rec := newStatusRecorder(nonFlusher{httptest.NewRecorder()})
	rec.Flush() // must not panic
}

// nonFlusher hides the Flusher interface of its embedded writer.
type nonFlusher struct{ http.ResponseWriter }

type hijackSpy struct {
	http.ResponseWriter
	calls int
	conn  net.Conn
	err   error
}

func (h *hijackSpy) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	h.calls++
	if h.err != nil {
		return nil, nil, h.err
	}
	return h.conn, bufio.NewReadWriter(bufio.NewReader(h.conn), bufio.NewWriter(h.conn)), nil
}

func TestStatusRecorderHijackDelegates(t *testing.T) {
	c1, c2 := net.Pipe()
	defer c1.Close()
	defer c2.Close()
	spy := &hijackSpy{ResponseWriter: httptest.NewRecorder(), conn: c1}
	rec := newStatusRecorder(spy)

	got, rw, err := rec.Hijack()
	if err != nil {
		t.Fatal(err)
	}
	if spy.calls != 1 {
		t.Errorf("expected 1 hijack delegation, got %d", spy.calls)
	}
	if got != c1 {
		t.Errorf("hijacked conn should be the one the spy returned")
	}
	if rw == nil {
		t.Errorf("hijack should return a bufio.ReadWriter")
	}
}

func TestStatusRecorderHijackPreservesPriorStatus(t *testing.T) {
	c1, c2 := net.Pipe()
	defer c1.Close()
	defer c2.Close()
	spy := &hijackSpy{ResponseWriter: httptest.NewRecorder(), conn: c1}
	rec := newStatusRecorder(spy)

	// ReverseProxy's websocket path writes 101 before Hijack. Our recorder
	// must not overwrite whatever status was already recorded — e.g. if a
	// non-upgrade caller hijacks after writing a 200, we should log 200.
	rec.WriteHeader(http.StatusOK)
	if _, _, err := rec.Hijack(); err != nil {
		t.Fatal(err)
	}
	if rec.status != http.StatusOK {
		t.Errorf("Hijack must not clobber captured status, got %d", rec.status)
	}
}

func TestStatusRecorderHijackUnsupported(t *testing.T) {
	rec := newStatusRecorder(nonHijacker{httptest.NewRecorder()})
	if _, _, err := rec.Hijack(); err == nil {
		t.Fatal("expected error when underlying writer is not a Hijacker")
	}
}

// nonHijacker hides the Hijacker interface of its embedded writer.
type nonHijacker struct{ http.ResponseWriter }

type readFromSpy struct {
	http.ResponseWriter
	called bool
	total  int64
	err    error
}

func (r *readFromSpy) ReadFrom(src io.Reader) (int64, error) {
	r.called = true
	if r.err != nil {
		return 0, r.err
	}
	n, err := io.Copy(r.ResponseWriter, src)
	r.total = n
	return n, err
}

func TestStatusRecorderReadFromUsesFastPath(t *testing.T) {
	inner := httptest.NewRecorder()
	spy := &readFromSpy{ResponseWriter: inner}
	rec := newStatusRecorder(spy)

	n, err := rec.ReadFrom(strings.NewReader("hello world"))
	if err != nil {
		t.Fatal(err)
	}
	if !spy.called {
		t.Errorf("underlying ReaderFrom was not delegated to")
	}
	if n != 11 || rec.bytes != 11 {
		t.Errorf("bytes mismatch: ReadFrom=%d rec.bytes=%d", n, rec.bytes)
	}
	if rec.status != http.StatusOK {
		t.Errorf("ReadFrom should imply 200 when nothing else was written, got %d", rec.status)
	}
	if got := inner.Body.String(); got != "hello world" {
		t.Errorf("body mismatch: %q", got)
	}
}

func TestStatusRecorderReadFromFallbackTracksBytes(t *testing.T) {
	inner := httptest.NewRecorder()
	// Wrap to strip the ReaderFrom interface (httptest.ResponseRecorder does
	// not implement ReaderFrom, but stripping makes intent explicit and
	// future-proofs the test).
	rec := newStatusRecorder(nonReaderFrom{ResponseWriter: inner})

	n, err := rec.ReadFrom(strings.NewReader("abcdef"))
	if err != nil {
		t.Fatal(err)
	}
	if n != 6 || rec.bytes != 6 {
		t.Errorf("fallback byte accounting wrong: n=%d rec.bytes=%d", n, rec.bytes)
	}
	if got := inner.Body.String(); got != "abcdef" {
		t.Errorf("body mismatch: %q", got)
	}
}

func TestStatusRecorderReadFromPropagatesError(t *testing.T) {
	spy := &readFromSpy{ResponseWriter: httptest.NewRecorder(), err: errors.New("boom")}
	rec := newStatusRecorder(spy)
	if _, err := rec.ReadFrom(strings.NewReader("x")); err == nil {
		t.Fatal("expected error propagation from underlying ReaderFrom")
	}
}

// nonReaderFrom hides any io.ReaderFrom implementation while keeping the rest
// of the http.ResponseWriter contract.
type nonReaderFrom struct{ http.ResponseWriter }

var _ http.ResponseWriter = nonReaderFrom{}
