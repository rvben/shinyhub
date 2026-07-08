package api_test

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/rvben/shinyhub/internal/api"
	"github.com/rvben/shinyhub/internal/auth"
	"github.com/rvben/shinyhub/internal/db"
)

// pngBytes is the 8-byte PNG signature plus filler; http.DetectContentType
// classifies it as image/png from the signature alone (the handler never
// decodes it).
var pngBytes = []byte("\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR-icon-payload")

func iconReq(method, path string, body []byte, contentType, token string) *http.Request {
	var req *http.Request
	if body != nil {
		req = httptest.NewRequest(method, path, bytes.NewReader(body))
		if contentType != "" {
			req.Header.Set("Content-Type", contentType)
		}
	} else {
		req = httptest.NewRequest(method, path, nil)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	return req
}

func serveIcon(srv *api.Server, req *http.Request) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)
	return rec
}

func TestAppIcon_UploadServeDelete(t *testing.T) {
	srv, store := newTestServer(t)
	hash, _ := testHashPassword("pass")
	store.CreateUser(db.CreateUserParams{Username: "owner", PasswordHash: hash, Role: "developer"})
	owner, _ := store.GetUserByUsername("owner")
	store.CreateApp(db.CreateAppParams{Slug: "dash", Name: "Dash", OwnerID: owner.ID, Access: "public"})
	tok, _ := auth.IssueJWT(owner.ID, "owner", "developer", "test-secret")

	// Upload as the owner (a manager of the app).
	if rec := serveIcon(srv, iconReq("PUT", "/api/apps/dash/icon", pngBytes, "image/png", tok)); rec.Code != http.StatusOK {
		t.Fatalf("upload: expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if app, _ := store.GetAppBySlug("dash"); app.IconMime != "image/png" {
		t.Fatalf("after upload icon_mime = %q, want image/png", app.IconMime)
	}

	// Serve: correct type, exact bytes, hardening + caching headers.
	rec := serveIcon(srv, iconReq("GET", "/api/apps/dash/icon", nil, "", tok))
	if rec.Code != http.StatusOK {
		t.Fatalf("serve: expected 200, got %d", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "image/png" {
		t.Errorf("Content-Type = %q, want image/png", ct)
	}
	if !bytes.Equal(rec.Body.Bytes(), pngBytes) {
		t.Errorf("served bytes do not match the uploaded icon")
	}
	if csp := rec.Header().Get("Content-Security-Policy"); csp != "sandbox" {
		t.Errorf("CSP = %q, want sandbox (neutralizes a directly-opened SVG)", csp)
	}
	if rec.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Errorf("missing X-Content-Type-Options: nosniff")
	}
	etag := rec.Header().Get("ETag")
	if etag == "" {
		t.Fatal("missing ETag")
	}
	// Always revalidate: a fresh (max-age) window plus a second-resolution
	// updated_at cache-buster would serve a stale icon after a same-second replace.
	if cc := rec.Header().Get("Cache-Control"); !strings.Contains(cc, "no-cache") {
		t.Errorf("Cache-Control = %q, want no-cache", cc)
	}

	// Conditional GET with the matching ETag is a 304.
	cond := iconReq("GET", "/api/apps/dash/icon", nil, "", tok)
	cond.Header.Set("If-None-Match", etag)
	if rec := serveIcon(srv, cond); rec.Code != http.StatusNotModified {
		t.Errorf("If-None-Match: expected 304, got %d", rec.Code)
	}

	// Delete reverts to iconless; the next serve is a 404 (so the UI shows the monogram).
	if rec := serveIcon(srv, iconReq("DELETE", "/api/apps/dash/icon", nil, "", tok)); rec.Code != http.StatusOK {
		t.Fatalf("delete: expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if app, _ := store.GetAppBySlug("dash"); app.IconMime != "" {
		t.Errorf("after delete icon_mime = %q, want empty", app.IconMime)
	}
	if rec := serveIcon(srv, iconReq("GET", "/api/apps/dash/icon", nil, "", tok)); rec.Code != http.StatusNotFound {
		t.Errorf("serve after delete: expected 404, got %d", rec.Code)
	}
}

func TestAppIcon_AuthzAndValidation(t *testing.T) {
	srv, store := newTestServer(t)
	hash, _ := testHashPassword("pass")
	store.CreateUser(db.CreateUserParams{Username: "owner", PasswordHash: hash, Role: "developer"})
	owner, _ := store.GetUserByUsername("owner")
	store.CreateUser(db.CreateUserParams{Username: "viewer", PasswordHash: hash, Role: "viewer"})
	viewer, _ := store.GetUserByUsername("viewer")
	store.CreateApp(db.CreateAppParams{Slug: "pub", Name: "Pub", OwnerID: owner.ID, Access: "public"})
	store.CreateApp(db.CreateAppParams{Slug: "secret", Name: "Secret", OwnerID: owner.ID, Access: "private"})
	ownerTok, _ := auth.IssueJWT(owner.ID, "owner", "developer", "test-secret")
	viewerTok, _ := auth.IssueJWT(viewer.ID, "viewer", "viewer", "test-secret")

	// A viewer (non-manager) cannot upload or delete.
	if rec := serveIcon(srv, iconReq("PUT", "/api/apps/pub/icon", pngBytes, "image/png", viewerTok)); rec.Code != http.StatusForbidden {
		t.Errorf("viewer upload: expected 403, got %d", rec.Code)
	}
	if rec := serveIcon(srv, iconReq("DELETE", "/api/apps/pub/icon", nil, "", viewerTok)); rec.Code != http.StatusForbidden {
		t.Errorf("viewer delete: expected 403, got %d", rec.Code)
	}

	// Owner uploads to the public app; a viewer CAN see it (view access).
	serveIcon(srv, iconReq("PUT", "/api/apps/pub/icon", pngBytes, "image/png", ownerTok))
	if rec := serveIcon(srv, iconReq("GET", "/api/apps/pub/icon", nil, "", viewerTok)); rec.Code != http.StatusOK {
		t.Errorf("viewer serve public icon: expected 200, got %d", rec.Code)
	}

	// Private app: the owner can see the icon, a non-member viewer is 404 (no view access).
	serveIcon(srv, iconReq("PUT", "/api/apps/secret/icon", pngBytes, "image/png", ownerTok))
	if rec := serveIcon(srv, iconReq("GET", "/api/apps/secret/icon", nil, "", ownerTok)); rec.Code != http.StatusOK {
		t.Errorf("owner serve private icon: expected 200, got %d", rec.Code)
	}
	if rec := serveIcon(srv, iconReq("GET", "/api/apps/secret/icon", nil, "", viewerTok)); rec.Code != http.StatusNotFound {
		t.Errorf("non-member viewer serve private icon: expected 404, got %d", rec.Code)
	}

	// Validation (owner): non-image is rejected.
	if rec := serveIcon(srv, iconReq("PUT", "/api/apps/pub/icon", []byte("this is plain text, not an image"), "application/octet-stream", ownerTok)); rec.Code != http.StatusBadRequest {
		t.Errorf("non-image upload: expected 400, got %d", rec.Code)
	}
	// A script-bearing SVG is rejected.
	if rec := serveIcon(srv, iconReq("PUT", "/api/apps/pub/icon", []byte(`<svg xmlns="http://www.w3.org/2000/svg"><script>alert(1)</script></svg>`), "image/svg+xml", ownerTok)); rec.Code != http.StatusBadRequest {
		t.Errorf("script SVG: expected 400, got %d", rec.Code)
	}
	// A clean SVG is accepted and stored with the svg MIME.
	if rec := serveIcon(srv, iconReq("PUT", "/api/apps/pub/icon", []byte(`<svg xmlns="http://www.w3.org/2000/svg"><circle r="8"/></svg>`), "image/svg+xml", ownerTok)); rec.Code != http.StatusOK {
		t.Errorf("clean SVG: expected 200, got %d", rec.Code)
	}
	if app, _ := store.GetAppBySlug("pub"); app.IconMime != "image/svg+xml" {
		t.Errorf("after SVG upload icon_mime = %q, want image/svg+xml", app.IconMime)
	}
	// Oversize (> 512 KB) is rejected by the size guard.
	big := make([]byte, (512<<10)+1)
	copy(big, pngBytes)
	if rec := serveIcon(srv, iconReq("PUT", "/api/apps/pub/icon", big, "image/png", ownerTok)); rec.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("oversize upload: expected 413, got %d", rec.Code)
	}

	// A chunked upload (no Content-Length) is accepted - icons need no length for
	// quota math, and MaxBytesReader still caps the body.
	chunked := iconReq("PUT", "/api/apps/pub/icon", pngBytes, "image/png", ownerTok)
	chunked.ContentLength = -1
	if rec := serveIcon(srv, chunked); rec.Code != http.StatusOK {
		t.Errorf("chunked upload (no Content-Length): expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
}
