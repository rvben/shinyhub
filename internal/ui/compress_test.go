package ui_test

import (
	"bytes"
	"compress/gzip"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/rvben/shinyhub/internal/ui"
)

// gunzip decompresses body and fails the test on error, so callers can
// compare decompressed content directly.
func gunzip(t *testing.T, body []byte) []byte {
	t.Helper()
	zr, err := gzip.NewReader(bytes.NewReader(body))
	if err != nil {
		t.Fatalf("gzip.NewReader: %v", err)
	}
	out, err := io.ReadAll(zr)
	if err != nil {
		t.Fatalf("gzip read: %v", err)
	}
	return out
}

// TestHandler_Gzip_ServesCompressedWhenAccepted verifies a client that sends
// Accept-Encoding: gzip gets a gzip-compressed body for a compressible asset,
// with a correct Content-Length and the identical decompressed content to an
// identity request.
func TestHandler_Gzip_ServesCompressedWhenAccepted(t *testing.T) {
	identity := httptest.NewRecorder()
	ui.Handler().ServeHTTP(identity, httptest.NewRequest(http.MethodGet, "/static/app.js", nil))
	if identity.Code != http.StatusOK {
		t.Fatalf("identity GET /static/app.js = %d, want 200", identity.Code)
	}

	req := httptest.NewRequest(http.MethodGet, "/static/app.js", nil)
	req.Header.Set("Accept-Encoding", "gzip, deflate, br")
	rec := httptest.NewRecorder()
	ui.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("gzip GET /static/app.js = %d, want 200", rec.Code)
	}
	if enc := rec.Header().Get("Content-Encoding"); enc != "gzip" {
		t.Fatalf("Content-Encoding = %q, want gzip", enc)
	}
	if vary := rec.Header().Get("Vary"); vary != "Accept-Encoding" {
		t.Errorf("Vary = %q, want Accept-Encoding", vary)
	}
	wantLen := strconv.Itoa(rec.Body.Len())
	if cl := rec.Header().Get("Content-Length"); cl != wantLen {
		t.Errorf("Content-Length = %q, want %q (actual body size)", cl, wantLen)
	}
	if rec.Body.Len() >= identity.Body.Len() {
		t.Errorf("gzip body (%d bytes) not smaller than identity body (%d bytes)", rec.Body.Len(), identity.Body.Len())
	}

	decoded := gunzip(t, rec.Body.Bytes())
	if string(decoded) != identity.Body.String() {
		t.Error("decompressed gzip body does not match identity body")
	}
}

// TestHandler_Gzip_NotAcceptedServesIdentityUnchanged verifies a request
// without Accept-Encoding takes the exact pre-compression code path: no
// Content-Encoding, no Vary, byte-identical to what the identity handler
// always served.
func TestHandler_Gzip_NotAcceptedServesIdentityUnchanged(t *testing.T) {
	rec := httptest.NewRecorder()
	ui.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/static/app.js", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("GET /static/app.js = %d, want 200", rec.Code)
	}
	if enc := rec.Header().Get("Content-Encoding"); enc != "" {
		t.Errorf("Content-Encoding = %q, want none", enc)
	}
	if vary := rec.Header().Get("Vary"); vary != "" {
		t.Errorf("Vary = %q, want none for a request that never negotiated encoding", vary)
	}
}

// TestHandler_Gzip_ETagDiffersFromIdentity verifies the gzip and identity
// representations of the same asset carry different ETags, so a cache keyed
// on ETag can never conflate the two encodings.
func TestHandler_Gzip_ETagDiffersFromIdentity(t *testing.T) {
	identity := httptest.NewRecorder()
	ui.Handler().ServeHTTP(identity, httptest.NewRequest(http.MethodGet, "/static/app.js", nil))
	identityETag := identity.Header().Get("ETag")

	req := httptest.NewRequest(http.MethodGet, "/static/app.js", nil)
	req.Header.Set("Accept-Encoding", "gzip")
	gz := httptest.NewRecorder()
	ui.Handler().ServeHTTP(gz, req)
	gzipETag := gz.Header().Get("ETag")

	if identityETag == "" || gzipETag == "" {
		t.Fatalf("missing ETag: identity=%q gzip=%q", identityETag, gzipETag)
	}
	if identityETag == gzipETag {
		t.Errorf("identity and gzip ETags must differ, both got %q", identityETag)
	}
}

// TestHandler_Gzip_ConditionalGetIsEncodingSpecific verifies a 304 is only
// returned when the If-None-Match ETag matches the representation this
// request actually negotiated - never the other encoding's ETag.
func TestHandler_Gzip_ConditionalGetIsEncodingSpecific(t *testing.T) {
	identity := httptest.NewRecorder()
	ui.Handler().ServeHTTP(identity, httptest.NewRequest(http.MethodGet, "/static/app.js", nil))
	identityETag := identity.Header().Get("ETag")

	gzipReq := httptest.NewRequest(http.MethodGet, "/static/app.js", nil)
	gzipReq.Header.Set("Accept-Encoding", "gzip")
	gzipResp := httptest.NewRecorder()
	ui.Handler().ServeHTTP(gzipResp, gzipReq)
	gzipETag := gzipResp.Header().Get("ETag")

	t.Run("matching gzip etag with gzip accepted returns 304", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/static/app.js", nil)
		req.Header.Set("Accept-Encoding", "gzip")
		req.Header.Set("If-None-Match", gzipETag)
		rec := httptest.NewRecorder()
		ui.Handler().ServeHTTP(rec, req)
		if rec.Code != http.StatusNotModified {
			t.Errorf("code = %d, want 304", rec.Code)
		}
	})

	t.Run("stale identity etag with gzip accepted returns full gzip body, not 304", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/static/app.js", nil)
		req.Header.Set("Accept-Encoding", "gzip")
		req.Header.Set("If-None-Match", identityETag)
		rec := httptest.NewRecorder()
		ui.Handler().ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("code = %d, want 200 (mismatched encoding-specific etag)", rec.Code)
		}
		if rec.Header().Get("Content-Encoding") != "gzip" {
			t.Errorf("Content-Encoding = %q, want gzip", rec.Header().Get("Content-Encoding"))
		}
	})

	t.Run("gzip etag without accepting gzip returns full identity body, not 304", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/static/app.js", nil)
		req.Header.Set("If-None-Match", gzipETag)
		rec := httptest.NewRecorder()
		ui.Handler().ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("code = %d, want 200 (gzip etag is meaningless without Accept-Encoding: gzip)", rec.Code)
		}
		if rec.Header().Get("Content-Encoding") != "" {
			t.Errorf("Content-Encoding = %q, want none", rec.Header().Get("Content-Encoding"))
		}
	})
}

// TestHandler_Gzip_SkipsAlreadyCompressedTypes verifies a PNG asset is never
// gzip-compressed even when the client accepts it: re-compressing an
// already-compressed format wastes CPU for no size benefit.
func TestHandler_Gzip_SkipsAlreadyCompressedTypes(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/static/brand/favicon-64.png", nil)
	req.Header.Set("Accept-Encoding", "gzip")
	rec := httptest.NewRecorder()
	ui.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("GET png = %d, want 200", rec.Code)
	}
	if enc := rec.Header().Get("Content-Encoding"); enc != "" {
		t.Errorf("Content-Encoding = %q, want none for a png asset", enc)
	}
}

// TestHandler_Gzip_RangeRequestKeepsIdentity verifies a Range request is
// never served a compressed representation, since ranging into the
// compressed byte stream would not correspond to the requested byte offsets
// of the actual asset content.
func TestHandler_Gzip_RangeRequestKeepsIdentity(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/static/app.js", nil)
	req.Header.Set("Accept-Encoding", "gzip")
	req.Header.Set("Range", "bytes=0-9")
	rec := httptest.NewRecorder()
	ui.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusPartialContent {
		t.Fatalf("ranged GET = %d, want 206", rec.Code)
	}
	if enc := rec.Header().Get("Content-Encoding"); enc != "" {
		t.Errorf("Content-Encoding = %q, want none for a Range request", enc)
	}
	if rec.Body.Len() != 10 {
		t.Errorf("body = %d bytes, want 10 (bytes=0-9)", rec.Body.Len())
	}
}

// TestHandler_Gzip_DevStaticSkipsCompression verifies SHINYHUB_DEV_STATIC
// mode never compresses, so a live-edited file on disk is always served
// as-is - never a stale cached gzip copy from before the edit.
func TestHandler_Gzip_DevStaticSkipsCompression(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "app.js"), []byte("console.log('dev');"), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	t.Setenv("SHINYHUB_DEV_STATIC", dir)

	req := httptest.NewRequest(http.MethodGet, "/static/app.js", nil)
	req.Header.Set("Accept-Encoding", "gzip")
	rec := httptest.NewRecorder()
	ui.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("dev-static GET = %d, want 200", rec.Code)
	}
	if enc := rec.Header().Get("Content-Encoding"); enc != "" {
		t.Errorf("Content-Encoding = %q, want none in dev-static mode", enc)
	}
	if rec.Body.String() != "console.log('dev');" {
		t.Errorf("body = %q, want the fixture's raw bytes", rec.Body.String())
	}
}

// TestAcceptsGzip_QualityValues asserts that Accept-Encoding quality values
// are compared numerically (RFC 9110 Section 12.4.2): any spelling of zero is
// a refusal, and any positive weight is acceptance.
func TestAcceptsGzip_QualityValues(t *testing.T) {
	cases := []struct {
		header string
		want   bool
	}{
		{"gzip", true},
		{"deflate, GZIP", true},
		{"gzip;q=1", true},
		{"gzip;q=0.5", true},
		{"gzip; q=0.001", true},
		{"gzip;q=0", false},
		{"gzip;q=0.0", false},
		{"gzip;q=0.000", false},
		{"gzip ; Q=0.00", false},
		{"br, gzip;q=0.0, deflate", false},
		{"br", false},
		{"", false},
	}
	for _, tc := range cases {
		r := httptest.NewRequest(http.MethodGet, "/", nil)
		r.Header.Set("Accept-Encoding", tc.header)
		if got := ui.AcceptsGzip(r); got != tc.want {
			t.Errorf("AcceptsGzip(%q) = %v, want %v", tc.header, got, tc.want)
		}
	}
}

// TestServeHTML_CacheKeyNeverServesOtherContent asserts that the memoized
// gzip form is tied to the bytes being served, not only to the caller's key:
// two different shells rendered under the same key (two servers with
// different branding in one process) each get their own compressed body and
// ETag.
func TestServeHTML_CacheKeyNeverServesOtherContent(t *testing.T) {
	serve := func(body string) (string, string) {
		r := httptest.NewRequest(http.MethodGet, "/", nil)
		r.Header.Set("Accept-Encoding", "gzip")
		w := httptest.NewRecorder()
		ui.ServeHTML(w, r, []byte(body), "shell:test-shared-key")
		return string(gunzip(t, w.Body.Bytes())), w.Header().Get("ETag")
	}
	firstBody, firstTag := serve("<html>first brand</html>")
	secondBody, secondTag := serve("<html>second brand</html>")
	if firstBody != "<html>first brand</html>" {
		t.Fatalf("first body = %q", firstBody)
	}
	if secondBody != "<html>second brand</html>" {
		t.Fatalf("second body = %q, want the second shell's own bytes", secondBody)
	}
	if firstTag == secondTag {
		t.Fatalf("both shells share ETag %s", firstTag)
	}
}
