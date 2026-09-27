package ui

import (
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"mime"
	"net/http"
	"path"
	"strconv"
	"strings"
	"sync"
)

// AcceptsGzip reports whether r's Accept-Encoding header allows a
// gzip-encoded response. A bare "gzip" token (with or without other
// encodings listed) is treated as acceptance; a quality value of zero in any
// spelling ("q=0", "q=0.0", "q=0.000", RFC 9110 Section 12.4.2) is a refusal.
func AcceptsGzip(r *http.Request) bool {
	for _, part := range strings.Split(r.Header.Get("Accept-Encoding"), ",") {
		token, params, _ := strings.Cut(strings.TrimSpace(part), ";")
		if !strings.EqualFold(strings.TrimSpace(token), "gzip") {
			continue
		}
		for _, p := range strings.Split(params, ";") {
			name, value, ok := strings.Cut(strings.TrimSpace(p), "=")
			if !ok || !strings.EqualFold(strings.TrimSpace(name), "q") {
				continue
			}
			if q, err := strconv.ParseFloat(strings.TrimSpace(value), 64); err == nil && q <= 0 {
				return false
			}
		}
		return true
	}
	return false
}

// addVaryAcceptEncoding adds "Accept-Encoding" to the response's Vary header
// without clobbering a value a caller already set (e.g. the root route's
// "Vary: Cookie" for an auth-varying landing page), appending to any existing
// value rather than replacing it.
func addVaryAcceptEncoding(w http.ResponseWriter) {
	const token = "Accept-Encoding"
	existing := w.Header().Get("Vary")
	if existing == "" {
		w.Header().Set("Vary", token)
		return
	}
	for _, v := range strings.Split(existing, ",") {
		if strings.EqualFold(strings.TrimSpace(v), token) {
			return
		}
	}
	w.Header().Set("Vary", existing+", "+token)
}

func gzipCompress(data []byte) []byte {
	var buf bytes.Buffer
	gw, err := gzip.NewWriterLevel(&buf, gzip.BestCompression)
	if err != nil {
		gw = gzip.NewWriter(&buf)
	}
	_, _ = gw.Write(data)
	_ = gw.Close()
	return buf.Bytes()
}

// contentETag renders a short content-addressed ETag: a truncated sha256
// hex digest in quotes, matching the style of assetsETag.
func contentETag(data []byte) string {
	sum := sha256.Sum256(data)
	return `"` + hex.EncodeToString(sum[:])[:16] + `"`
}

// gzipETag derives the gzip variant's ETag from the identity ETag by
// appending a suffix, so a cache keyed on ETag never conflates the two
// encodings' bytes.
func gzipETag(identityETag string) string {
	if len(identityETag) < 2 {
		return identityETag
	}
	return identityETag[:len(identityETag)-1] + `-gzip"`
}

// gzipEntry is a memoized gzip-compressed representation of some content,
// paired with the ETag that identifies it.
type gzipEntry struct {
	data []byte
	etag string
}

var (
	gzipMemoMu sync.Mutex
	gzipMemo   = map[string]gzipEntry{}
)

// gzipMemoized returns the gzip-compressed form of data plus its ETag,
// computing it once per key and content and caching thereafter. The memo is
// keyed by the content digest as well as the caller's key, so two callers
// that happen to share a key but serve different bytes never receive each
// other's body. Pass key "" to skip caching (compress on every call) for
// content that can change between requests, such as the SPA shell under
// SHINYHUB_DEV_STATIC.
func gzipMemoized(key string, data []byte) gzipEntry {
	identityETag := contentETag(data)
	if key != "" {
		key += ":" + identityETag
		gzipMemoMu.Lock()
		e, ok := gzipMemo[key]
		gzipMemoMu.Unlock()
		if ok {
			return e
		}
	}

	e := gzipEntry{data: gzipCompress(data), etag: gzipETag(identityETag)}

	if key != "" {
		gzipMemoMu.Lock()
		gzipMemo[key] = e
		gzipMemoMu.Unlock()
	}
	return e
}

// ServeHTML writes an HTML response, negotiating gzip when the request's
// Accept-Encoding allows it. identity is the exact bytes that would
// otherwise be written. cacheKey memoizes the compressed bytes across
// requests (pass "" for content that can change per request, e.g. under
// live-reload dev static serving).
//
// When the client does not accept gzip, this writes identity exactly as the
// pre-compression code path did: no ETag, no Content-Length, no Vary. Those
// are only added on the gzip path, which no request could have exercised
// before this negotiation existed.
func ServeHTML(w http.ResponseWriter, r *http.Request, identity []byte, cacheKey string) {
	if !AcceptsGzip(r) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		if r.Method != http.MethodHead {
			_, _ = w.Write(identity)
		}
		return
	}

	addVaryAcceptEncoding(w)
	e := gzipMemoized(cacheKey, identity)
	w.Header().Set("ETag", e.etag)
	if r.Header.Get("If-None-Match") == e.etag {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Content-Encoding", "gzip")
	w.Header().Set("Content-Length", strconv.Itoa(len(e.data)))
	if r.Method != http.MethodHead {
		_, _ = w.Write(e.data)
	}
}

// noGzipExt lists static-asset extensions that are already compressed (or
// bring negligible benefit from gzip), so re-compressing them would spend
// CPU for no size gain.
var noGzipExt = map[string]bool{
	".png": true, ".jpg": true, ".jpeg": true, ".gif": true, ".webp": true,
	".ico": true, ".woff": true, ".woff2": true, ".ttf": true, ".otf": true,
	".eot": true, ".gz": true, ".br": true, ".zip": true, ".mp4": true, ".webm": true,
}

func isCompressibleAsset(name string) bool {
	return !noGzipExt[strings.ToLower(path.Ext(name))]
}

// gzipAssetEntry is a memoized gzip-compressed static asset, alongside the
// Content-Type resolved for its identity bytes (so both encodings of the
// same asset report the same type).
type gzipAssetEntry struct {
	gzipEntry
	contentType string
}

var (
	assetGzipMu    sync.Mutex
	assetGzipCache = map[string]gzipAssetEntry{}
)

// gzipStaticAsset returns the memoized gzip-compressed form of the embedded
// static asset at name (relative to the static root), computing it on first
// use. Embedded assets are immutable for the life of the process, so this is
// never called under SHINYHUB_DEV_STATIC, where files can change on disk.
func gzipStaticAsset(name string) (gzipAssetEntry, bool) {
	assetGzipMu.Lock()
	e, ok := assetGzipCache[name]
	assetGzipMu.Unlock()
	if ok {
		return e, true
	}

	f, err := Static().Open(name)
	if err != nil {
		return gzipAssetEntry{}, false
	}
	defer f.Close()
	raw, err := io.ReadAll(f)
	if err != nil {
		return gzipAssetEntry{}, false
	}

	ct := mime.TypeByExtension(path.Ext(name))
	if ct == "" {
		ct = http.DetectContentType(raw)
	}
	entry := gzipAssetEntry{
		gzipEntry:   gzipEntry{data: gzipCompress(raw), etag: gzipETag(contentETag(raw))},
		contentType: ct,
	}

	assetGzipMu.Lock()
	assetGzipCache[name] = entry
	assetGzipMu.Unlock()
	return entry, true
}
