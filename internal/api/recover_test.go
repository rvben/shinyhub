package api_test

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/rvben/shinyhub/internal/api"
)

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// TestRecoverPanic_Returns500 proves a panic in a downstream handler (e.g. the
// /app/* reverse proxy, which had no recovery — only /api/* did) is contained
// as a 500 instead of crashing the connection/process (PROD-5).
func TestRecoverPanic_Returns500(t *testing.T) {
	h := api.RecoverPanic(discardLogger(), http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		panic("boom")
	}))
	rr := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/app/demo/", nil)

	// Must not propagate the panic to the caller.
	h.ServeHTTP(rr, req)

	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("want 500 after recovered panic, got %d", rr.Code)
	}
}

// TestRecoverPanic_RepanicsAbortHandler proves http.ErrAbortHandler is
// re-panicked so the stdlib server still aborts the connection as intended.
func TestRecoverPanic_RepanicsAbortHandler(t *testing.T) {
	h := api.RecoverPanic(discardLogger(), http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		panic(http.ErrAbortHandler)
	}))
	defer func() {
		if rec := recover(); rec != http.ErrAbortHandler {
			t.Fatalf("want re-panicked ErrAbortHandler, got %v", rec)
		}
	}()
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/app/demo/", nil))
	t.Fatal("expected ErrAbortHandler to propagate")
}

// TestRecoverPanic_PassesThroughNormal proves non-panicking handlers are
// unaffected.
func TestRecoverPanic_PassesThroughNormal(t *testing.T) {
	h := api.RecoverPanic(discardLogger(), http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTeapot)
	}))
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest("GET", "/app/demo/", nil))
	if rr.Code != http.StatusTeapot {
		t.Fatalf("want 418 passthrough, got %d", rr.Code)
	}
}
