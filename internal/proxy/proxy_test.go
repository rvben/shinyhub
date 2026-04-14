package proxy_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/rvben/shinyhub/internal/proxy"
)

func TestProxyRoutesKnownSlug(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("hello from app"))
	}))
	defer backend.Close()

	p := proxy.New()
	if err := p.Register("my-app", backend.URL); err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest("GET", "/app/my-app/some/path", nil)
	rec := httptest.NewRecorder()
	p.ServeHTTP(rec, req)

	if rec.Code != 200 {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if rec.Body.String() != "hello from app" {
		t.Errorf("unexpected body: %s", rec.Body.String())
	}
}

func TestProxyUnknownSlug(t *testing.T) {
	p := proxy.New()
	req := httptest.NewRequest("GET", "/app/unknown/", nil)
	rec := httptest.NewRecorder()
	p.ServeHTTP(rec, req)
	if rec.Code != 404 {
		t.Errorf("expected 404, got %d", rec.Code)
	}
}

func TestProxySwap(t *testing.T) {
	backend1 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("v1"))
	}))
	defer backend1.Close()
	backend2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("v2"))
	}))
	defer backend2.Close()

	p := proxy.New()
	if err := p.Register("app", backend1.URL); err != nil {
		t.Fatal(err)
	}
	req1 := httptest.NewRequest("GET", "/app/app/", nil)
	rec1 := httptest.NewRecorder()
	p.ServeHTTP(rec1, req1)
	if rec1.Body.String() != "v1" {
		t.Fatalf("expected v1, got %s", rec1.Body.String())
	}

	if err := p.Register("app", backend2.URL); err != nil { // atomic swap
		t.Fatal(err)
	}
	req2 := httptest.NewRequest("GET", "/app/app/", nil)
	rec2 := httptest.NewRecorder()
	p.ServeHTTP(rec2, req2)
	if rec2.Body.String() != "v2" {
		t.Fatalf("expected v2, got %s", rec2.Body.String())
	}
}

func TestProxyDeregister(t *testing.T) {
	p := proxy.New()
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer backend.Close()
	if err := p.Register("app", backend.URL); err != nil {
		t.Fatal(err)
	}
	p.Deregister("app")
	req := httptest.NewRequest("GET", "/app/app/", nil)
	rec := httptest.NewRecorder()
	p.ServeHTTP(rec, req)
	if rec.Code != 404 {
		t.Errorf("expected 404 after deregister, got %d", rec.Code)
	}
}

func TestProxyRegisterInvalidURL(t *testing.T) {
	p := proxy.New()
	if err := p.Register("app", "not-a-url"); err == nil {
		t.Error("expected error for URL with no scheme/host")
	}
}

func TestProxyStripsPrefix(t *testing.T) {
	var receivedPath string
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedPath = r.URL.Path
		w.WriteHeader(200)
	}))
	defer backend.Close()

	p := proxy.New()
	if err := p.Register("my-app", backend.URL); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest("GET", "/app/my-app/dashboard", nil)
	rec := httptest.NewRecorder()
	p.ServeHTTP(rec, req)

	if receivedPath != "/dashboard" {
		t.Errorf("expected backend to receive /dashboard, got %s", receivedPath)
	}
}
