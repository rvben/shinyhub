package ui_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/rvben/shinyhub/internal/ui"
)

// TestHandler_SetsRevalidationCacheHeaders verifies static assets are served
// with a revalidation cache policy: an ETag validator plus Cache-Control:
// no-cache. Without a validator the unversioned asset URLs either never cache
// (slow repeat loads) or cache stale across a release (broken dashboard).
func TestHandler_SetsRevalidationCacheHeaders(t *testing.T) {
	req := httptest.NewRequest("GET", "/static/app.js", nil)
	rec := httptest.NewRecorder()
	ui.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("GET /static/app.js = %d, want 200", rec.Code)
	}
	if cc := rec.Header().Get("Cache-Control"); cc != "no-cache" {
		t.Errorf("Cache-Control = %q, want no-cache", cc)
	}
	if etag := rec.Header().Get("ETag"); etag == "" {
		t.Error("ETag must be set as a revalidation validator")
	}
}

// TestHandler_ConditionalGetReturns304 verifies a repeat load with a matching
// If-None-Match gets a cheap 304 instead of the full asset body.
func TestHandler_ConditionalGetReturns304(t *testing.T) {
	// First request to learn the ETag.
	first := httptest.NewRecorder()
	ui.Handler().ServeHTTP(first, httptest.NewRequest("GET", "/static/app.js", nil))
	etag := first.Header().Get("ETag")
	if etag == "" {
		t.Fatal("no ETag on first response")
	}

	req := httptest.NewRequest("GET", "/static/app.js", nil)
	req.Header.Set("If-None-Match", etag)
	rec := httptest.NewRecorder()
	ui.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusNotModified {
		t.Fatalf("conditional GET with matching ETag = %d, want 304", rec.Code)
	}
	if rec.Body.Len() != 0 {
		t.Errorf("304 must have an empty body, got %d bytes", rec.Body.Len())
	}
}
