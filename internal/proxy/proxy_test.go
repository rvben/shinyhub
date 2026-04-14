package proxy_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

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
	if rec.Code != http.StatusOK {
		t.Errorf("expected 200 (loading page) for unknown slug, got %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "Starting app") {
		t.Errorf("expected loading page body for unknown slug")
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
	if rec.Code != http.StatusOK {
		t.Errorf("expected 200 (loading page) after deregister, got %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "Starting app") {
		t.Errorf("expected loading page body after deregister")
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

func TestProxy_RecordsActivity(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
	}))
	defer backend.Close()

	p := proxy.New()
	if err := p.Register("app", backend.URL); err != nil {
		t.Fatal(err)
	}

	before := time.Now()
	req := httptest.NewRequest("GET", "/app/app/", nil)
	rec := httptest.NewRecorder()
	p.ServeHTTP(rec, req)

	last := p.LastSeen("app")
	if last.Before(before) {
		t.Errorf("LastSeen not updated after proxy: got %v, before was %v", last, before)
	}
}

func TestProxy_ServesLoadingPageOnMiss(t *testing.T) {
	p := proxy.New()
	req := httptest.NewRequest("GET", "/app/missing/", nil)
	rec := httptest.NewRecorder()
	p.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("expected 200 (loading page), got %d", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "Starting app") {
		t.Errorf("loading page missing 'Starting app': %s", body)
	}
	if !strings.Contains(body, `http-equiv="refresh"`) {
		t.Errorf("loading page missing meta refresh tag: %s", body)
	}
}

func TestProxy_CallsOnMissCallback(t *testing.T) {
	p := proxy.New()
	var mu sync.Mutex
	var called []string
	p.SetOnMiss(func(slug string) {
		mu.Lock()
		called = append(called, slug)
		mu.Unlock()
	})

	req := httptest.NewRequest("GET", "/app/myapp/", nil)
	rec := httptest.NewRecorder()
	p.ServeHTTP(rec, req)

	// onMiss runs in a goroutine; give it time to execute.
	time.Sleep(20 * time.Millisecond)
	mu.Lock()
	defer mu.Unlock()
	if len(called) != 1 || called[0] != "myapp" {
		t.Errorf("expected onMiss('myapp') called once, got %v", called)
	}
}
