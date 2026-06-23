package api

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/rvben/shinyhub/internal/auth"
	"github.com/rvben/shinyhub/internal/db"
)

// maxAppIconBytes caps an uploaded app icon. Icons are small glyphs, not
// artwork; 512 KiB is generous for a crisp PNG/WebP and tiny for an SVG.
const maxAppIconBytes = 512 << 10

// allowedRasterIconMIME is the set of raster types accepted on upload, keyed by
// the value http.DetectContentType reports. The sniffed type (not the client's
// Content-Type header) becomes the stored, canonical MIME, so a mislabelled or
// spoofed header cannot get a non-image served back as an image.
var allowedRasterIconMIME = map[string]bool{
	"image/png":  true,
	"image/jpeg": true,
	"image/webp": true,
}

// looksLikeSVG reports whether b is plausibly a standalone SVG document and
// carries no obvious script vector. http.DetectContentType cannot identify SVG
// (it reports text/xml or text/plain), so SVG is validated structurally here.
// The icon-serve handler additionally sends `Content-Security-Policy: sandbox`
// and `X-Content-Type-Options: nosniff`, so this is defense-in-depth, not the
// only barrier.
func looksLikeSVG(b []byte) bool {
	lower := bytes.ToLower(b)
	if !bytes.Contains(lower, []byte("<svg")) {
		return false
	}
	// An app icon never needs scripting or external/script-bearing handlers.
	for _, bad := range [][]byte{[]byte("<script"), []byte("javascript:"), []byte("onload="), []byte("onerror=")} {
		if bytes.Contains(lower, bad) {
			return false
		}
	}
	return true
}

// classifyIcon validates the uploaded bytes against the declared Content-Type
// and returns the canonical MIME to store, or an error message suitable for a
// 400 response.
func classifyIcon(declared string, data []byte) (string, error) {
	if len(data) == 0 {
		return "", errors.New("icon is empty")
	}
	// Strip any charset/parameters from the declared type ("image/svg+xml; charset=utf-8").
	declared = strings.TrimSpace(strings.SplitN(strings.ToLower(declared), ";", 2)[0])

	if declared == "image/svg+xml" {
		if !looksLikeSVG(data) {
			return "", errors.New("not a valid script-free SVG")
		}
		return "image/svg+xml", nil
	}

	// Raster: trust the sniffed type over the header.
	sniffed := http.DetectContentType(data)
	if allowedRasterIconMIME[sniffed] {
		return sniffed, nil
	}
	return "", errors.New("icon must be a PNG, JPEG, WebP, or SVG image")
}

// handleSetAppIcon stores an uploaded icon for the app (manager-gated). The body
// is the raw image bytes; the Content-Type header declares the format. An icon
// change does not restart the app — it is presentation metadata only.
func (s *Server) handleSetAppIcon(w http.ResponseWriter, r *http.Request) {
	slug := chi.URLParam(r, "slug")
	if _, ok := s.requireManageApp(w, r, slug); !ok {
		return
	}

	// Early reject when a declared length already exceeds the cap (saves reading
	// the body). A missing/unknown Content-Length (chunked or proxied uploads) is
	// fine: MaxBytesReader still enforces the cap below, and an empty body is
	// caught by classifyIcon. Unlike the data API, icons need no Content-Length
	// for quota math, so it is not required here.
	if r.ContentLength > maxAppIconBytes {
		writeError(w, http.StatusRequestEntityTooLarge, "icon must be 512 KB or smaller")
		return
	}

	body := http.MaxBytesReader(w, r.Body, maxAppIconBytes)
	data, err := io.ReadAll(body)
	if err != nil {
		// MaxBytesReader trips this when the body exceeds the cap.
		writeError(w, http.StatusRequestEntityTooLarge, "icon must be 512 KB or smaller")
		return
	}

	mime, err := classifyIcon(r.Header.Get("Content-Type"), data)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	if err := s.store.SetAppIcon(slug, mime, data); err != nil {
		if errors.Is(err, db.ErrNotFound) {
			writeError(w, http.StatusNotFound, "not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "internal server error")
		return
	}

	s.auditIcon(r, db.AuditAppIconSet, slug, map[string]any{"mime": mime, "size": len(data)})
	writeJSON(w, http.StatusOK, map[string]any{"slug": slug, "icon_mime": mime, "size": len(data)})
}

// handleGetAppIcon serves the app's icon to anyone with view access. Returns 404
// (not 204) when no icon is set, so the client falls back to the monogram. The
// response is hardened so a directly-navigated SVG cannot execute script.
func (s *Server) handleGetAppIcon(w http.ResponseWriter, r *http.Request) {
	slug := chi.URLParam(r, "slug")
	_, _, ok := s.requireViewApp(w, r, slug)
	if !ok {
		return
	}

	mime, data, err := s.store.GetAppIcon(slug)
	if err != nil {
		if errors.Is(err, db.ErrNotFound) {
			writeError(w, http.StatusNotFound, "no icon")
			return
		}
		writeError(w, http.StatusInternalServerError, "internal server error")
		return
	}

	etag := fmt.Sprintf("\"%x\"", sha256.Sum256(data))
	w.Header().Set("Content-Type", mime)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	// Neutralize any script in a directly-navigated SVG; harmless for <img> use.
	w.Header().Set("Content-Security-Policy", "sandbox")
	// Revalidate against the content ETag on every use rather than serving from a
	// fresh window. The icon URL's only cache-buster is updated_at, which has
	// second resolution on SQLite, so two replacements within one second would
	// otherwise keep a stale icon cached. no-cache + the content ETag makes a
	// replaced icon show immediately while unchanged icons still cost only a 304.
	w.Header().Set("Cache-Control", "private, no-cache")
	w.Header().Set("ETag", etag)
	if match := r.Header.Get("If-None-Match"); match != "" && match == etag {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(data)
}

// handleClearAppIcon removes the app's icon (manager-gated), reverting the UI to
// the monogram. Idempotent: clearing an already-iconless app still succeeds.
func (s *Server) handleClearAppIcon(w http.ResponseWriter, r *http.Request) {
	slug := chi.URLParam(r, "slug")
	if _, ok := s.requireManageApp(w, r, slug); !ok {
		return
	}

	if err := s.store.ClearAppIcon(slug); err != nil {
		if errors.Is(err, db.ErrNotFound) {
			writeError(w, http.StatusNotFound, "not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "internal server error")
		return
	}

	s.auditIcon(r, db.AuditAppIconCleared, slug, nil)
	writeJSON(w, http.StatusOK, map[string]any{"slug": slug, "status": "cleared"})
}

// auditIcon records an icon mutation. Extra fields (mime, size) are merged into
// the detail payload alongside the slug.
func (s *Server) auditIcon(r *http.Request, action, slug string, extra map[string]any) {
	fields := map[string]any{"slug": slug}
	for k, v := range extra {
		fields[k] = v
	}
	detail, _ := json.Marshal(fields)
	u := auth.UserFromContext(r.Context())
	var userID *int64
	if u != nil {
		userID = &u.ID
	}
	s.store.LogAuditEvent(db.AuditEventParams{
		UserID:       userID,
		Action:       action,
		ResourceType: "app",
		ResourceID:   slug,
		Detail:       string(detail),
		IPAddress:    s.ClientIP(r),
	})
}
