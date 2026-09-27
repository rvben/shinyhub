package main

import (
	"bytes"
	"compress/gzip"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/rvben/shinyhub/internal/config"
)

// gunzipBody decompresses a gzip response body, failing the test on error.
func gunzipBody(t *testing.T, body []byte) []byte {
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

// TestBrandingRoutes_Gzip_StockShellUnauthenticated verifies the zero-branding,
// unauthenticated fast path (the ServeFileFS path when identity is requested)
// negotiates gzip correctly: decompressed body matches the identity bytes,
// Content-Length matches the actual compressed body, and Vary carries only
// Accept-Encoding (no landing page is configured, so nothing else sets Vary).
func TestBrandingRoutes_Gzip_StockShellUnauthenticated(t *testing.T) {
	staticIndex := mustReadStaticIndex(t)
	mux, _ := buildBrandingMux(t, config.BrandingConfig{})

	for _, path := range []string{"/", "/login"} {
		t.Run(path, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, path, nil)
			req.Header.Set("Accept-Encoding", "gzip, deflate, br")
			rr := httptest.NewRecorder()
			mux.ServeHTTP(rr, req)

			if rr.Code != http.StatusOK {
				t.Fatalf("GET %s = %d, want 200", path, rr.Code)
			}
			if enc := rr.Header().Get("Content-Encoding"); enc != "gzip" {
				t.Fatalf("Content-Encoding = %q, want gzip", enc)
			}
			if v := rr.Header().Get("Vary"); v != "Accept-Encoding" {
				t.Errorf("Vary = %q, want Accept-Encoding", v)
			}
			wantLen := strconv.Itoa(rr.Body.Len())
			if cl := rr.Header().Get("Content-Length"); cl != wantLen {
				t.Errorf("Content-Length = %q, want %q", cl, wantLen)
			}
			decoded := gunzipBody(t, rr.Body.Bytes())
			if path == "/" && !bytes.Equal(decoded, staticIndex) {
				t.Errorf("decompressed GET / body not byte-identical to identity index.html (%d vs %d bytes)",
					len(decoded), len(staticIndex))
			}
			if !bytes.Contains(decoded, []byte("app.js")) {
				t.Errorf("decompressed body for %s does not contain 'app.js'", path)
			}
		})
	}
}

// TestBrandingRoutes_Gzip_NotAcceptedServesIdentity verifies a request with no
// Accept-Encoding still gets the byte-identical, uncompressed response: the
// negotiation must never fire uninvited.
func TestBrandingRoutes_Gzip_NotAcceptedServesIdentity(t *testing.T) {
	staticIndex := mustReadStaticIndex(t)
	mux, _ := buildBrandingMux(t, config.BrandingConfig{})

	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/", nil))

	if rr.Code != http.StatusOK {
		t.Fatalf("GET / = %d, want 200", rr.Code)
	}
	if enc := rr.Header().Get("Content-Encoding"); enc != "" {
		t.Errorf("Content-Encoding = %q, want none", enc)
	}
	if !bytes.Equal(rr.Body.Bytes(), staticIndex) {
		t.Error("GET / without Accept-Encoding must stay byte-identical to the identity index.html")
	}
}

// TestBrandingRoutes_Gzip_ConditionalGet verifies the gzip shell response
// supports a matching If-None-Match returning 304.
func TestBrandingRoutes_Gzip_ConditionalGet(t *testing.T) {
	mux, _ := buildBrandingMux(t, config.BrandingConfig{})

	first := httptest.NewRequest(http.MethodGet, "/", nil)
	first.Header.Set("Accept-Encoding", "gzip")
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, first)
	etag := rr.Header().Get("ETag")
	if etag == "" {
		t.Fatal("missing ETag on gzip shell response")
	}

	second := httptest.NewRequest(http.MethodGet, "/", nil)
	second.Header.Set("Accept-Encoding", "gzip")
	second.Header.Set("If-None-Match", etag)
	rr2 := httptest.NewRecorder()
	mux.ServeHTTP(rr2, second)
	if rr2.Code != http.StatusNotModified {
		t.Errorf("conditional GET / = %d, want 304", rr2.Code)
	}
}

// TestBrandingRoutes_Gzip_BrandedShell verifies the branded render path (no
// landing page, so serveShell's dynamic-render branch runs) also negotiates
// gzip, and the decompressed body still carries the branding injection.
func TestBrandingRoutes_Gzip_BrandedShell(t *testing.T) {
	mux, _ := buildBrandingMux(t, config.BrandingConfig{SiteTitle: "AcmeCorp"})

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Accept-Encoding", "gzip")
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("GET / (branded) = %d, want 200", rr.Code)
	}
	if rr.Header().Get("Content-Encoding") != "gzip" {
		t.Fatalf("Content-Encoding = %q, want gzip", rr.Header().Get("Content-Encoding"))
	}
	decoded := gunzipBody(t, rr.Body.Bytes())
	if !bytes.Contains(decoded, []byte("window.__SHINYHUB_BRANDING__")) {
		t.Error("decompressed branded shell missing window.__SHINYHUB_BRANDING__")
	}
}

// TestBrandingRoutes_Gzip_AuthedAutoLandingRootPreservesVaryCookie is the
// regression test for a Vary-clobbering bug: the auto-landing root route sets
// "Vary: Cookie" itself (an auth-varying response must never be shared-cached
// across viewers) before falling through to serveShell. A naive
// gzip-negotiation Vary write that does w.Header().Set("Vary", "Accept-Encoding")
// would silently destroy that "Cookie" value for any gzip-accepting client -
// virtually all real browsers - breaking cache correctness in this exact
// deployment shape (operator landing page + auth-optional root). The response
// must carry BOTH tokens.
func TestBrandingRoutes_Gzip_AuthedAutoLandingRootPreservesVaryCookie(t *testing.T) {
	landingContent := []byte("<!DOCTYPE html><html><body>operator landing</body></html>")
	landingPath := filepath.Join(t.TempDir(), "landing.html")
	if err := os.WriteFile(landingPath, landingContent, 0o644); err != nil {
		t.Fatal(err)
	}
	cfg := buildBrandingConfigWithLanding(t, landingPath) // default root_behavior = auto
	mux, store := buildBrandingMux(t, cfg)

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.AddCookie(brandingSessionCookie(t, store))
	req.Header.Set("Accept-Encoding", "gzip")
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("GET / (authed, gzip) = %d, want 200", rr.Code)
	}
	if rr.Header().Get("Content-Encoding") != "gzip" {
		t.Fatalf("Content-Encoding = %q, want gzip", rr.Header().Get("Content-Encoding"))
	}
	vary := rr.Header().Get("Vary")
	for _, want := range []string{"Cookie", "Accept-Encoding"} {
		found := false
		for _, v := range strings.Split(vary, ",") {
			if strings.EqualFold(strings.TrimSpace(v), want) {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("Vary = %q, missing %q", vary, want)
		}
	}
	if cc := rr.Header().Get("Cache-Control"); !strings.Contains(cc, "no-store") {
		t.Errorf("Cache-Control = %q, want no-store on the auth-varying root", cc)
	}
	decoded := gunzipBody(t, rr.Body.Bytes())
	if !bytes.Contains(decoded, []byte("app.js")) {
		t.Error("decompressed authed shell missing app.js")
	}
}

// TestBrandingRoutes_Gzip_DevStaticRecompressesOnEveryRequest verifies
// SHINYHUB_DEV_STATIC mode still negotiates gzip (so a developer testing
// compression locally sees it), but never serves a stale cached copy: editing
// the on-disk index.html between two gzip requests must be reflected in the
// second response, proving compression is recomputed per request rather than
// memoized under a shared cache key.
func TestBrandingRoutes_Gzip_DevStaticRecompressesOnEveryRequest(t *testing.T) {
	dir := t.TempDir()
	first := "<!DOCTYPE html><html><body>dev shell v1 app.js</body></html>"
	if err := os.WriteFile(filepath.Join(dir, "index.html"), []byte(first), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	t.Setenv("SHINYHUB_DEV_STATIC", dir)

	mux, _ := buildBrandingMux(t, config.BrandingConfig{})

	req1 := httptest.NewRequest(http.MethodGet, "/", nil)
	req1.Header.Set("Accept-Encoding", "gzip")
	rr1 := httptest.NewRecorder()
	mux.ServeHTTP(rr1, req1)
	if rr1.Header().Get("Content-Encoding") != "gzip" {
		t.Fatalf("dev-static Content-Encoding = %q, want gzip", rr1.Header().Get("Content-Encoding"))
	}
	if decoded := gunzipBody(t, rr1.Body.Bytes()); string(decoded) != first {
		t.Fatalf("first response decoded = %q, want %q", decoded, first)
	}

	second := "<!DOCTYPE html><html><body>dev shell v2 app.js</body></html>"
	if err := os.WriteFile(filepath.Join(dir, "index.html"), []byte(second), 0o644); err != nil {
		t.Fatalf("rewrite fixture: %v", err)
	}

	req2 := httptest.NewRequest(http.MethodGet, "/", nil)
	req2.Header.Set("Accept-Encoding", "gzip")
	rr2 := httptest.NewRecorder()
	mux.ServeHTTP(rr2, req2)
	if rr2.Header().Get("Content-Encoding") != "gzip" {
		t.Fatalf("dev-static Content-Encoding = %q, want gzip", rr2.Header().Get("Content-Encoding"))
	}
	if decoded := gunzipBody(t, rr2.Body.Bytes()); string(decoded) != second {
		t.Errorf("second response decoded = %q, want %q (edit must not be served from a stale cache)", decoded, second)
	}
}
