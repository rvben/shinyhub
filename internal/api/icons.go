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
	"github.com/rvben/shinyhub/internal/deploy"
)

// maxAppIconBytes caps an uploaded app icon. Icons are small glyphs, not
// artwork; 512 KiB is generous for a crisp PNG/WebP and tiny for an SVG.
const maxAppIconBytes = 512 << 10

// Base64 adds one third to the image size; the remainder holds metadata.
const maxAppIconStateBytes = ((maxAppIconBytes + 2) / 3 * 4) + 2048

type appIconState struct {
	AppID    int64  `json:"app_id"`
	Mime     string `json:"mime"`
	Data     []byte `json:"data"`
	Emoji    string `json:"emoji"`
	Revision string `json:"revision"`
}

func iconStateResponse(state db.AppIconState) appIconState {
	// Hash a structured encoding to avoid concatenation ambiguity. Normalize no
	// image bytes so NULL and an empty byte slice describe the same icon state.
	data := state.Data
	if len(data) == 0 {
		data = nil
	}
	body := appIconState{AppID: state.AppID, Mime: state.Mime, Data: data, Emoji: state.Emoji}
	encoded, _ := json.Marshal(body)
	body.Revision = fmt.Sprintf("%x", sha256.Sum256(encoded))
	return body
}

func (s *Server) handleGetAppIconState(w http.ResponseWriter, r *http.Request) {
	slug := chi.URLParam(r, "slug")
	app, ok := s.requireManageApp(w, r, slug)
	if !ok {
		return
	}
	state, err := s.store.GetAppIconState(slug)
	if errors.Is(err, db.ErrNotFound) {
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	if err != nil {
		writeInternal(w, r, "internal server error", err)
		return
	}
	if state.AppID != app.ID {
		writeError(w, http.StatusConflict, "This app changed while loading its icon. Reload before changing it.")
		return
	}
	w.Header().Set("Cache-Control", "private, no-store")
	writeJSON(w, http.StatusOK, iconStateResponse(*state))
}

func (s *Server) handlePutAppIconState(w http.ResponseWriter, r *http.Request) {
	slug := chi.URLParam(r, "slug")
	app, ok := s.requireManageApp(w, r, slug)
	if !ok {
		return
	}
	var request struct {
		AppID    int64           `json:"app_id"`
		Mime     *string         `json:"mime"`
		Data     json.RawMessage `json:"data"`
		Emoji    *string         `json:"emoji"`
		Revision string          `json:"revision"`
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxAppIconStateBytes))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			writeError(w, http.StatusRequestEntityTooLarge, "icon must be 512 KB or smaller")
		} else {
			writeError(w, http.StatusBadRequest, "invalid icon state; provide app_id, mime, base64 data, emoji and revision")
		}
		return
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			writeError(w, http.StatusRequestEntityTooLarge, "icon must be 512 KB or smaller")
		} else {
			writeError(w, http.StatusBadRequest, "provide one icon state object")
		}
		return
	}
	if request.AppID <= 0 || request.Revision == "" {
		writeError(w, http.StatusBadRequest, "load the current icon state before changing it")
		return
	}
	if request.Mime == nil || request.Emoji == nil || len(request.Data) == 0 {
		writeError(w, http.StatusBadRequest, "provide the complete icon state: mime, data and emoji")
		return
	}
	desired := appIconState{AppID: request.AppID, Mime: *request.Mime, Emoji: *request.Emoji, Revision: request.Revision}
	if err := json.Unmarshal(request.Data, &desired.Data); err != nil {
		writeError(w, http.StatusBadRequest, "icon data must be base64 image bytes or null")
		return
	}
	if len(desired.Data) > maxAppIconBytes {
		writeError(w, http.StatusRequestEntityTooLarge, "icon must be 512 KB or smaller")
		return
	}
	if desired.Emoji != "" {
		if err := deploy.ValidateIconEmoji(desired.Emoji); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
	}
	if len(desired.Data) == 0 {
		if desired.Mime != "" {
			writeError(w, http.StatusBadRequest, "icon MIME requires image data")
			return
		}
		desired.Data = nil
	} else {
		mime, err := classifyIcon(desired.Mime, desired.Data)
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		desired.Mime = mime
	}
	expected, err := s.store.GetAppIconState(slug)
	if errors.Is(err, db.ErrNotFound) {
		writeError(w, http.StatusConflict, "This app or its icon changed. Reload before changing or undoing the icon.")
		return
	}
	if err != nil {
		writeInternal(w, r, "internal server error", err)
		return
	}
	if desired.AppID != app.ID || expected.AppID != app.ID || desired.Revision != iconStateResponse(*expected).Revision {
		writeError(w, http.StatusConflict, "This app or its icon changed. Reload before changing or undoing the icon.")
		return
	}
	next := db.AppIconState{AppID: desired.AppID, Mime: desired.Mime, Data: desired.Data, Emoji: desired.Emoji}
	if err := s.store.CompareAndSwapAppIconState(slug, *expected, next); err != nil {
		if errors.Is(err, db.ErrAppIconConflict) {
			writeError(w, http.StatusConflict, "This app or its icon changed. Reload before changing or undoing the icon.")
		} else {
			writeInternal(w, r, "internal server error", err)
		}
		return
	}
	action := db.AuditAppIconSet
	if next.Mime == "" && next.Emoji == "" {
		action = db.AuditAppIconCleared
	}
	s.auditIcon(r, action, slug, map[string]any{"restored_state": true, "mime": next.Mime, "size": len(next.Data), "emoji": next.Emoji})
	writeJSON(w, http.StatusOK, map[string]any{"app_id": next.AppID, "icon_mime": next.Mime, "icon_emoji": next.Emoji, "revision": iconStateResponse(next).Revision})
}

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
		writeInternal(w, r, "internal server error", err)
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
		writeInternal(w, r, "internal server error", err)
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
		writeInternal(w, r, "internal server error", err)
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
	s.logAuditEvent(r, db.AuditEventParams{
		UserID:       userID,
		Action:       action,
		ResourceType: "app",
		ResourceID:   slug,
		Detail:       string(detail),
		IPAddress:    s.ClientIP(r),
		RunID:        s.knownFleetRunID(r),
	})
}
