package main

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/rvben/shinyhub/internal/db"
)

type fakeAppFaviconStore struct {
	app      *db.App
	mime     string
	data     []byte
	appErr   error
	iconErr  error
	iconGets int
}

func (s *fakeAppFaviconStore) GetAppBySlug(string) (*db.App, error) { return s.app, s.appErr }
func (s *fakeAppFaviconStore) GetAppIcon(string) (string, []byte, error) {
	s.iconGets++
	return s.mime, s.data, s.iconErr
}

func requestAppFavicon(t *testing.T, store appFaviconStore) *httptest.ResponseRecorder {
	t.Helper()
	fallback := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "image/x-icon")
		_, _ = w.Write([]byte("platform"))
	})
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/app/demo/.shinyhub/favicon", nil)
	req.SetPathValue("slug", "demo")
	appFaviconHandler(store, fallback).ServeHTTP(rr, req)
	return rr
}

func TestAppFaviconEmojiWinsWithoutReadingImage(t *testing.T) {
	store := &fakeAppFaviconStore{app: &db.App{Slug: "demo", IconEmoji: "📊", IconMime: "image/png"}}
	rr := requestAppFavicon(t, store)
	if rr.Code != http.StatusOK || rr.Header().Get("Content-Type") != "image/svg+xml" || !strings.Contains(rr.Body.String(), "📊") {
		t.Fatalf("emoji response = %d %q %q", rr.Code, rr.Header().Get("Content-Type"), rr.Body.String())
	}
	if store.iconGets != 0 {
		t.Fatal("uploaded icon was read even though emoji must win")
	}
}

func TestAppFaviconServesUploadedImage(t *testing.T) {
	store := &fakeAppFaviconStore{
		app:  &db.App{Slug: "demo", IconMime: "image/png"},
		mime: "image/png", data: []byte("PNG"),
	}
	rr := requestAppFavicon(t, store)
	if rr.Code != http.StatusOK || rr.Header().Get("Content-Type") != "image/png" || rr.Body.String() != "PNG" {
		t.Fatalf("image response = %d %q %q", rr.Code, rr.Header().Get("Content-Type"), rr.Body.String())
	}
}

func TestAppFaviconFallsBackToPlatform(t *testing.T) {
	rr := requestAppFavicon(t, &fakeAppFaviconStore{app: &db.App{Slug: "demo"}})
	if rr.Code != http.StatusOK || rr.Body.String() != "platform" {
		t.Fatalf("fallback response = %d %q", rr.Code, rr.Body.String())
	}
}

func TestAppFaviconDoesNotHideStoreFailure(t *testing.T) {
	rr := requestAppFavicon(t, &fakeAppFaviconStore{appErr: errors.New("database unavailable")})
	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rr.Code)
	}
}
