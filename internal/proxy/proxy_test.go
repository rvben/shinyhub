package proxy_test

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
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

func TestProxySetsForwardingHeaders(t *testing.T) {
	var (
		gotRealIP       string
		gotForwardedFor string
		gotProto        string
		gotForwHost     string
	)
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotRealIP = r.Header.Get("X-Real-IP")
		gotForwardedFor = r.Header.Get("X-Forwarded-For")
		gotProto = r.Header.Get("X-Forwarded-Proto")
		gotForwHost = r.Header.Get("X-Forwarded-Host")
	}))
	defer backend.Close()

	p := proxy.New()
	if err := p.Register("app", backend.URL); err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest("GET", "/app/app/", nil)
	req.RemoteAddr = "203.0.113.5:54321"
	req.Host = "shinyhub.example.com"
	rec := httptest.NewRecorder()
	p.ServeHTTP(rec, req)

	if gotRealIP != "203.0.113.5" {
		t.Errorf("X-Real-IP: expected 203.0.113.5, got %q", gotRealIP)
	}
	if gotForwardedFor != "203.0.113.5" {
		t.Errorf("X-Forwarded-For: expected 203.0.113.5, got %q", gotForwardedFor)
	}
	if gotProto != "http" {
		t.Errorf("X-Forwarded-Proto: expected http, got %q", gotProto)
	}
	if gotForwHost != "shinyhub.example.com" {
		t.Errorf("X-Forwarded-Host: expected shinyhub.example.com, got %q", gotForwHost)
	}
}

func TestProxyEmitsAccessLog(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusCreated)
		w.Write([]byte("hi"))
	}))
	defer backend.Close()

	p := proxy.New()
	if err := p.Register("app", backend.URL); err != nil {
		t.Fatal(err)
	}

	var mu sync.Mutex
	var got proxy.AccessLogEntry
	var count int
	p.SetAccessLogger(func(e proxy.AccessLogEntry) {
		mu.Lock()
		got = e
		count++
		mu.Unlock()
	})

	req := httptest.NewRequest("GET", "/app/app/some/path", nil)
	req.RemoteAddr = "203.0.113.5:54321"
	rec := httptest.NewRecorder()
	p.ServeHTTP(rec, req)

	mu.Lock()
	defer mu.Unlock()
	if count != 1 {
		t.Fatalf("expected 1 access-log entry, got %d", count)
	}
	if got.Slug != "app" {
		t.Errorf("Slug: expected app, got %q", got.Slug)
	}
	if got.Method != "GET" {
		t.Errorf("Method: expected GET, got %q", got.Method)
	}
	if got.Path != "/app/app/some/path" {
		t.Errorf("Path: expected /app/app/some/path, got %q", got.Path)
	}
	if got.Status != http.StatusCreated {
		t.Errorf("Status: expected 201, got %d", got.Status)
	}
	if got.Bytes != 2 {
		t.Errorf("Bytes: expected 2, got %d", got.Bytes)
	}
	if got.ClientIP != "203.0.113.5" {
		t.Errorf("ClientIP: expected 203.0.113.5, got %q", got.ClientIP)
	}
	if got.Peer != "203.0.113.5:54321" {
		t.Errorf("Peer: expected 203.0.113.5:54321, got %q", got.Peer)
	}
	if got.Duration <= 0 {
		t.Errorf("Duration: expected > 0, got %v", got.Duration)
	}
}

func TestProxyAccessLogUsesClientIPResolver(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer backend.Close()

	p := proxy.New()
	if err := p.Register("app", backend.URL); err != nil {
		t.Fatal(err)
	}
	// Simulates a trusted-proxy-aware resolver that trusts the peer and
	// returns the leftmost X-Forwarded-For IP as the real client.
	p.SetClientIPResolver(func(r *http.Request) string {
		if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
			return strings.TrimSpace(strings.SplitN(xff, ",", 2)[0])
		}
		return ""
	})

	var mu sync.Mutex
	var got proxy.AccessLogEntry
	p.SetAccessLogger(func(e proxy.AccessLogEntry) {
		mu.Lock()
		got = e
		mu.Unlock()
	})

	req := httptest.NewRequest("GET", "/app/app/", nil)
	req.RemoteAddr = "10.0.0.1:45678"
	req.Header.Set("X-Forwarded-For", "198.51.100.77")
	rec := httptest.NewRecorder()
	p.ServeHTTP(rec, req)

	mu.Lock()
	defer mu.Unlock()
	if got.ClientIP != "198.51.100.77" {
		t.Errorf("ClientIP: expected resolver output 198.51.100.77, got %q", got.ClientIP)
	}
	if got.Peer != "10.0.0.1:45678" {
		t.Errorf("Peer: expected raw RemoteAddr 10.0.0.1:45678, got %q", got.Peer)
	}
}

func TestProxyAccessLogOnLoadingPage(t *testing.T) {
	p := proxy.New()

	var mu sync.Mutex
	var got proxy.AccessLogEntry
	var count int
	p.SetAccessLogger(func(e proxy.AccessLogEntry) {
		mu.Lock()
		got = e
		count++
		mu.Unlock()
	})

	req := httptest.NewRequest("GET", "/app/unknown/", nil)
	req.RemoteAddr = "203.0.113.9:12345"
	rec := httptest.NewRecorder()
	p.ServeHTTP(rec, req)

	mu.Lock()
	defer mu.Unlock()
	if count != 1 {
		t.Fatalf("expected 1 access-log entry for loading page, got %d", count)
	}
	if got.Slug != "unknown" {
		t.Errorf("Slug: expected unknown, got %q", got.Slug)
	}
	if got.Status != http.StatusOK {
		t.Errorf("Status: expected 200, got %d", got.Status)
	}
	if got.ReplicaIndex != -1 {
		t.Errorf("ReplicaIndex: expected -1 (no replica), got %d", got.ReplicaIndex)
	}
}

func TestProxySetsForwardedHeader(t *testing.T) {
	var gotForwarded string
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotForwarded = r.Header.Get("Forwarded")
	}))
	defer backend.Close()

	p := proxy.New()
	if err := p.Register("app", backend.URL); err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest("GET", "/app/app/", nil)
	req.RemoteAddr = "203.0.113.5:54321"
	req.Host = "apps.example.com"
	rec := httptest.NewRecorder()
	p.ServeHTTP(rec, req)

	if !strings.Contains(gotForwarded, `for="203.0.113.5:54321"`) {
		t.Errorf("Forwarded missing for=...: %q", gotForwarded)
	}
	if !strings.Contains(gotForwarded, "proto=http") {
		t.Errorf("Forwarded missing proto: %q", gotForwarded)
	}
	if !strings.Contains(gotForwarded, `host="apps.example.com"`) {
		t.Errorf("Forwarded missing host: %q", gotForwarded)
	}
}

func TestProxyPreservesIncomingForwarded(t *testing.T) {
	var gotForwarded string
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotForwarded = r.Header.Get("Forwarded")
	}))
	defer backend.Close()

	p := proxy.New()
	if err := p.Register("app", backend.URL); err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest("GET", "/app/app/", nil)
	req.RemoteAddr = "10.0.0.1:8888"
	req.Host = "internal-shinyhub.lan"
	req.Header.Set("Forwarded", `for="203.0.113.5:443";proto=https;host="edge.example.com"`)
	rec := httptest.NewRecorder()
	p.ServeHTTP(rec, req)

	if !strings.Contains(gotForwarded, `for="203.0.113.5:443"`) {
		t.Errorf("Forwarded should be preserved with upstream for=..., got %q", gotForwarded)
	}
	if !strings.Contains(gotForwarded, "proto=https") {
		t.Errorf("Forwarded should preserve proto=https, got %q", gotForwarded)
	}
}

func TestProxyAccessLogCapturesBackendStatus(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "nope", http.StatusInternalServerError)
	}))
	defer backend.Close()

	p := proxy.New()
	if err := p.Register("app", backend.URL); err != nil {
		t.Fatal(err)
	}

	var mu sync.Mutex
	var got proxy.AccessLogEntry
	p.SetAccessLogger(func(e proxy.AccessLogEntry) {
		mu.Lock()
		got = e
		mu.Unlock()
	})

	req := httptest.NewRequest("GET", "/app/app/", nil)
	req.RemoteAddr = "203.0.113.1:5555"
	rec := httptest.NewRecorder()
	p.ServeHTTP(rec, req)

	mu.Lock()
	defer mu.Unlock()
	if got.Status != http.StatusInternalServerError {
		t.Errorf("Status: expected 500, got %d", got.Status)
	}
	if got.Bytes == 0 {
		t.Errorf("Bytes: expected > 0 for error body, got %d", got.Bytes)
	}
}

func TestProxyAccessLogStickyReplica(t *testing.T) {
	var serves atomic.Int64
	backend0 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		serves.Add(1)
		w.Write([]byte("r0"))
	}))
	defer backend0.Close()
	backend1 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		serves.Add(1)
		w.Write([]byte("r1"))
	}))
	defer backend1.Close()

	p := proxy.New()
	p.SetPoolSize("app", 2)
	if err := p.RegisterReplica("app", 0, backend0.URL); err != nil {
		t.Fatal(err)
	}
	if err := p.RegisterReplica("app", 1, backend1.URL); err != nil {
		t.Fatal(err)
	}

	var mu sync.Mutex
	entries := make([]proxy.AccessLogEntry, 0, 2)
	p.SetAccessLogger(func(e proxy.AccessLogEntry) {
		mu.Lock()
		entries = append(entries, e)
		mu.Unlock()
	})

	// First request: no cookie → sticky must be false.
	req1 := httptest.NewRequest("GET", "/app/app/", nil)
	rec1 := httptest.NewRecorder()
	p.ServeHTTP(rec1, req1)

	var cookieVal string
	for _, c := range rec1.Result().Cookies() {
		if strings.HasPrefix(c.Name, "shinyhub_rep_") {
			cookieVal = c.Value
		}
	}
	if cookieVal == "" {
		t.Fatal("expected sticky cookie on first response")
	}

	// Second request: present the cookie → sticky must be true and route
	// to the same replica the cookie pins.
	req2 := httptest.NewRequest("GET", "/app/app/", nil)
	req2.AddCookie(&http.Cookie{Name: "shinyhub_rep_app", Value: cookieVal})
	rec2 := httptest.NewRecorder()
	p.ServeHTTP(rec2, req2)

	mu.Lock()
	defer mu.Unlock()
	if len(entries) != 2 {
		t.Fatalf("expected 2 access-log entries, got %d", len(entries))
	}
	if entries[0].Sticky {
		t.Errorf("first request: Sticky should be false (no cookie)")
	}
	if entries[0].ReplicaIndex < 0 || entries[0].ReplicaIndex > 1 {
		t.Errorf("first request: ReplicaIndex out of range: %d", entries[0].ReplicaIndex)
	}
	if !entries[1].Sticky {
		t.Errorf("second request: Sticky should be true (cookie present)")
	}
	if strconvItoa(entries[1].ReplicaIndex) != cookieVal {
		t.Errorf("second request: ReplicaIndex %d should match cookie %q", entries[1].ReplicaIndex, cookieVal)
	}
}

func strconvItoa(i int) string { return fmt.Sprintf("%d", i) }

func TestProxyForwardedIPv6(t *testing.T) {
	var gotForwarded, gotRealIP string
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotForwarded = r.Header.Get("Forwarded")
		gotRealIP = r.Header.Get("X-Real-IP")
	}))
	defer backend.Close()

	p := proxy.New()
	if err := p.Register("app", backend.URL); err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest("GET", "/app/app/", nil)
	req.RemoteAddr = "[2001:db8::1]:54321"
	rec := httptest.NewRecorder()
	p.ServeHTTP(rec, req)

	// RFC 7239 §6: IPv6 addresses MUST be bracketed inside the quoted for= value.
	if !strings.Contains(gotForwarded, `for="[2001:db8::1]:54321"`) {
		t.Errorf("Forwarded: expected bracketed IPv6, got %q", gotForwarded)
	}
	// X-Real-IP is the IP without brackets or port.
	if gotRealIP != "2001:db8::1" {
		t.Errorf("X-Real-IP: expected 2001:db8::1, got %q", gotRealIP)
	}
}

func TestProxyAccessLogNilLoggerIsSafe(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer backend.Close()

	p := proxy.New()
	if err := p.Register("app", backend.URL); err != nil {
		t.Fatal(err)
	}
	// Never calling SetAccessLogger → logger remains nil; must not panic.
	req := httptest.NewRequest("GET", "/app/app/", nil)
	rec := httptest.NewRecorder()
	p.ServeHTTP(rec, req)

	// Explicitly clearing with nil must also be safe.
	p.SetAccessLogger(func(proxy.AccessLogEntry) {})
	p.SetAccessLogger(nil)
	p.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/app/app/", nil))
}

func TestProxyAccessLogConcurrent(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("ok"))
	}))
	defer backend.Close()

	p := proxy.New()
	if err := p.Register("app", backend.URL); err != nil {
		t.Fatal(err)
	}

	var count atomic.Int64
	p.SetAccessLogger(func(e proxy.AccessLogEntry) {
		if e.Slug != "app" || e.Status != http.StatusOK {
			t.Errorf("unexpected entry: %+v", e)
		}
		count.Add(1)
	})

	const N = 50
	var wg sync.WaitGroup
	wg.Add(N)
	for i := 0; i < N; i++ {
		go func() {
			defer wg.Done()
			req := httptest.NewRequest("GET", "/app/app/", nil)
			p.ServeHTTP(httptest.NewRecorder(), req)
		}()
	}
	wg.Wait()
	if got := count.Load(); got != N {
		t.Errorf("expected %d entries, got %d", N, got)
	}
}

func TestProxyPreservesIncomingForwardingHeaders(t *testing.T) {
	var (
		gotRealIP   string
		gotProto    string
		gotForwHost string
	)
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotRealIP = r.Header.Get("X-Real-IP")
		gotProto = r.Header.Get("X-Forwarded-Proto")
		gotForwHost = r.Header.Get("X-Forwarded-Host")
	}))
	defer backend.Close()

	p := proxy.New()
	if err := p.Register("app", backend.URL); err != nil {
		t.Fatal(err)
	}

	// Simulate an edge proxy (nginx/caddy) that already terminated TLS
	// and populated the forwarding headers for us.
	req := httptest.NewRequest("GET", "/app/app/", nil)
	req.RemoteAddr = "10.0.0.1:45678"
	req.Host = "internal-shinyhub.lan"
	req.Header.Set("X-Real-IP", "203.0.113.5")
	req.Header.Set("X-Forwarded-Proto", "https")
	req.Header.Set("X-Forwarded-Host", "apps.example.com")
	rec := httptest.NewRecorder()
	p.ServeHTTP(rec, req)

	if gotRealIP != "203.0.113.5" {
		t.Errorf("X-Real-IP should be preserved, got %q", gotRealIP)
	}
	if gotProto != "https" {
		t.Errorf("X-Forwarded-Proto should be preserved, got %q", gotProto)
	}
	if gotForwHost != "apps.example.com" {
		t.Errorf("X-Forwarded-Host should be preserved, got %q", gotForwHost)
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
	if !strings.Contains(body, "window.location.reload") {
		t.Errorf("loading page missing client-side reload script: %s", body)
	}
	if !strings.Contains(body, "shinyhub-retry") {
		t.Errorf("loading page missing retry button: %s", body)
	}
	if !strings.Contains(body, `<noscript><meta http-equiv="refresh"`) {
		t.Errorf("loading page missing noscript meta refresh fallback: %s", body)
	}
}

func TestProxy_ReturnsNotFoundForUnknownSlug(t *testing.T) {
	p := proxy.New()
	p.SetSlugExists(func(slug string) (bool, error) { return slug == "known", nil })
	var onMissCalled bool
	p.SetOnMiss(func(string) { onMissCalled = true })

	req := httptest.NewRequest("GET", "/app/typo/", nil)
	rec := httptest.NewRecorder()
	p.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Errorf("expected 404 for unknown slug, got %d (body=%q)", rec.Code, rec.Body.String())
	}
	if onMissCalled {
		t.Error("onMiss should not fire for an unknown slug — wakeup is only meaningful for slugs that exist")
	}
}

// TestProxy_LookupErrorFallsThroughToLoadingPage guards against the bug where
// any GetAppBySlug failure (DB unavailable, context cancelled, etc.) was
// mapped to "slug missing" and returned 404 — so a momentary database hiccup
// looked like a permanently deleted app. The predicate's err return must NOT
// be conflated with !exists; the proxy must fall through to the loading page
// (legacy default) and let the caller log the lookup error.
func TestProxy_LookupErrorFallsThroughToLoadingPage(t *testing.T) {
	p := proxy.New()
	p.SetSlugExists(func(slug string) (bool, error) {
		return false, errors.New("database is locked")
	})
	done := make(chan struct{})
	p.SetOnMiss(func(string) { close(done) })

	req := httptest.NewRequest("GET", "/app/maybe-real/", nil)
	rec := httptest.NewRecorder()
	p.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("expected loading page (200) on lookup error, got %d (body=%q)", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "Starting app") {
		t.Errorf("expected loading page body, got %q", rec.Body.String())
	}
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Error("onMiss should still fire on lookup error — the slug might exist; we just couldn't tell")
	}
}

func TestProxy_ServesLoadingPageWhenSlugKnown(t *testing.T) {
	p := proxy.New()
	p.SetSlugExists(func(slug string) (bool, error) { return true, nil })
	done := make(chan struct{})
	p.SetOnMiss(func(string) { close(done) })

	req := httptest.NewRequest("GET", "/app/sleeping/", nil)
	rec := httptest.NewRecorder()
	p.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("expected loading page (200) for known hibernated slug, got %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "Starting app") {
		t.Errorf("expected loading page body, got %q", rec.Body.String())
	}
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Error("onMiss should fire for known hibernated slug")
	}
}

func TestProxy_CallsOnMissCallback(t *testing.T) {
	p := proxy.New()
	var mu sync.Mutex
	var called []string
	done := make(chan struct{})
	p.SetOnMiss(func(slug string) {
		mu.Lock()
		called = append(called, slug)
		mu.Unlock()
		close(done)
	})

	req := httptest.NewRequest("GET", "/app/myapp/", nil)
	rec := httptest.NewRecorder()
	p.ServeHTTP(rec, req)

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("onMiss not called within timeout")
	}
	mu.Lock()
	defer mu.Unlock()
	if len(called) != 1 || called[0] != "myapp" {
		t.Errorf("expected onMiss('myapp') called once, got %v", called)
	}
}

func TestProxy_PoolRegister(t *testing.T) {
	p := proxy.New()
	p.SetPoolSize("demo", 3)

	if err := p.RegisterReplica("demo", 0, "http://127.0.0.1:20001"); err != nil {
		t.Fatal(err)
	}
	if err := p.RegisterReplica("demo", 1, "http://127.0.0.1:20002"); err != nil {
		t.Fatal(err)
	}
	if err := p.RegisterReplica("demo", 3, "http://x"); err == nil {
		t.Fatal("expected error for out-of-range index")
	}
}

func TestProxy_DeregisterReplica(t *testing.T) {
	p := proxy.New()
	p.SetPoolSize("demo", 2)
	_ = p.RegisterReplica("demo", 0, "http://127.0.0.1:20001")
	_ = p.RegisterReplica("demo", 1, "http://127.0.0.1:20002")

	p.DeregisterReplica("demo", 0)
	if !p.HasLiveReplica("demo") {
		t.Fatal("expected at least one live replica")
	}

	p.Deregister("demo")
	if p.HasLiveReplica("demo") {
		t.Fatal("expected empty pool")
	}
}

func TestProxy_StickyCookie(t *testing.T) {
	b0 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "rep0")
	}))
	defer b0.Close()
	b1 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "rep1")
	}))
	defer b1.Close()

	p := proxy.New()
	p.SetPoolSize("demo", 2)
	_ = p.RegisterReplica("demo", 0, b0.URL)
	_ = p.RegisterReplica("demo", 1, b1.URL)

	req := httptest.NewRequest(http.MethodGet, "/app/demo/", nil)
	rec := httptest.NewRecorder()
	p.ServeHTTP(rec, req)
	cookies := rec.Result().Cookies()
	var rep string
	for _, c := range cookies {
		if c.Name == "shinyhub_rep_demo" {
			rep = c.Value
		}
	}
	if rep == "" {
		t.Fatal("expected shinyhub_rep_demo cookie")
	}
	first := rec.Body.String()

	req2 := httptest.NewRequest(http.MethodGet, "/app/demo/", nil)
	req2.AddCookie(&http.Cookie{Name: "shinyhub_rep_demo", Value: rep})
	rec2 := httptest.NewRecorder()
	p.ServeHTTP(rec2, req2)
	if rec2.Body.String() != first {
		t.Fatalf("sticky failure: first=%s second=%s", first, rec2.Body.String())
	}
}

func TestProxy_StickyCookieStale(t *testing.T) {
	b0 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "rep0")
	}))
	defer b0.Close()
	p := proxy.New()
	p.SetPoolSize("demo", 2)
	_ = p.RegisterReplica("demo", 0, b0.URL)

	req := httptest.NewRequest(http.MethodGet, "/app/demo/", nil)
	req.AddCookie(&http.Cookie{Name: "shinyhub_rep_demo", Value: "1"})
	rec := httptest.NewRecorder()
	p.ServeHTTP(rec, req)
	if rec.Body.String() != "rep0" {
		t.Fatalf("stale cookie not ignored: %s", rec.Body.String())
	}
	newCookie := ""
	for _, c := range rec.Result().Cookies() {
		if c.Name == "shinyhub_rep_demo" {
			newCookie = c.Value
		}
	}
	if newCookie != "0" {
		t.Fatalf("expected new cookie=0, got %q", newCookie)
	}
}

func TestProxy_BeginHibernate_AbortsIfActivityRecordedAfterSnapshot(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer backend.Close()

	p := proxy.New()
	if err := p.Register("app", backend.URL); err != nil {
		t.Fatal(err)
	}

	// Snapshot lastSeen, then simulate a request landing after the snapshot.
	snapshot := time.Now()
	time.Sleep(2 * time.Millisecond)
	p.RecordActivity("app")

	if p.BeginHibernate("app", snapshot) {
		t.Fatal("BeginHibernate returned true after activity recorded since snapshot")
	}
	if !p.HasLiveReplica("app") {
		t.Error("pool was removed despite aborted hibernate")
	}
	if p.LastSeen("app").IsZero() {
		t.Error("lastSeen was cleared despite aborted hibernate")
	}
}

func TestProxy_BeginHibernate_RemovesPoolWhenIdle(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer backend.Close()

	p := proxy.New()
	if err := p.Register("app", backend.URL); err != nil {
		t.Fatal(err)
	}
	p.RecordActivity("app")
	time.Sleep(2 * time.Millisecond)
	snapshot := time.Now()

	if !p.BeginHibernate("app", snapshot) {
		t.Fatal("BeginHibernate returned false when no activity since snapshot")
	}
	if p.HasLiveReplica("app") {
		t.Error("pool was not removed after successful hibernate")
	}
	if !p.LastSeen("app").IsZero() {
		t.Error("lastSeen was not cleared after successful hibernate")
	}
}

func TestProxy_BeginHibernate_AbortsWhileRequestInFlight(t *testing.T) {
	hold := make(chan struct{})
	released := make(chan struct{})
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(hold)
		<-released
	}))
	// release the handler before backend.Close so the test does not deadlock.
	defer backend.Close()
	defer close(released)

	p := proxy.New()
	if err := p.Register("app", backend.URL); err != nil {
		t.Fatal(err)
	}

	go func() {
		req := httptest.NewRequest(http.MethodGet, "/app/app/", nil)
		p.ServeHTTP(httptest.NewRecorder(), req)
	}()

	// Wait until the request is mid-proxy (backend handler entered).
	select {
	case <-hold:
	case <-time.After(2 * time.Second):
		t.Fatal("backend never received in-flight request")
	}

	// Future-dated snapshot defeats the lastSeen check; only the in-flight
	// activeConns counter should now block hibernation.
	snapshot := time.Now().Add(time.Hour)
	if p.BeginHibernate("app", snapshot) {
		t.Error("BeginHibernate returned true while a request was in flight")
	}
	if !p.HasLiveReplica("app") {
		t.Error("pool was removed despite in-flight request")
	}
}

// pausingWriter wraps an httptest.ResponseRecorder and blocks the first
// call to Header() until release is closed. Used to pause ServeHTTP at the
// SetCookie call site, which sits between pickReplica and the activeConns
// bump — exactly the window where a stale read of activeConns could let
// BeginHibernate succeed concurrently.
type pausingWriter struct {
	*httptest.ResponseRecorder
	once    sync.Once
	paused  chan struct{}
	release chan struct{}
}

func (p *pausingWriter) Header() http.Header {
	p.once.Do(func() {
		close(p.paused)
		<-p.release
	})
	return p.ResponseRecorder.Header()
}

// TestProxy_ServeHTTP_HoldsRouteLockThroughActiveConnsBump asserts that
// ServeHTTP holds the route-table read lock from the moment it consults
// p.pools through the increment of activeConns. Without this lock window,
// the watchdog can call BeginHibernate after pickReplica has chosen a
// replica but before activeConns has been bumped — winning the race and
// stopping the backend processes that the in-flight request is about to
// reach.
func TestProxy_ServeHTTP_HoldsRouteLockThroughActiveConnsBump(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer backend.Close()

	p := proxy.New()
	if err := p.Register("app", backend.URL); err != nil {
		t.Fatal(err)
	}

	pw := &pausingWriter{
		ResponseRecorder: httptest.NewRecorder(),
		paused:           make(chan struct{}),
		release:          make(chan struct{}),
	}

	served := make(chan struct{})
	go func() {
		defer close(served)
		req := httptest.NewRequest(http.MethodGet, "/app/app/", nil)
		p.ServeHTTP(pw, req)
	}()

	// Wait until ServeHTTP is paused inside SetCookie — the request has
	// passed pickReplica but has not yet bumped activeConns.
	select {
	case <-pw.paused:
	case <-time.After(2 * time.Second):
		t.Fatal("ServeHTTP never reached the SetCookie call")
	}

	// Future-dated snapshot defeats the lastSeen check; the only thing
	// that can keep BeginHibernate from succeeding here is the route-lock
	// window held by ServeHTTP across the activeConns bump.
	hibernateResult := make(chan bool, 1)
	go func() {
		hibernateResult <- p.BeginHibernate("app", time.Now().Add(time.Hour))
	}()

	select {
	case ok := <-hibernateResult:
		t.Fatalf("BeginHibernate returned %v during the route-decision window — the route lock is not held across activeConns bump", ok)
	case <-time.After(75 * time.Millisecond):
		// Expected: BeginHibernate is blocked on p.mu.Lock() while
		// ServeHTTP holds the read lock.
	}

	// Release ServeHTTP. After this it bumps activeConns, releases the
	// read lock, and forwards to the backend. BeginHibernate then
	// acquires the write lock, observes activeConns>0, and returns false.
	close(pw.release)

	select {
	case <-served:
	case <-time.After(2 * time.Second):
		t.Fatal("ServeHTTP did not complete after release")
	}

	select {
	case ok := <-hibernateResult:
		if ok {
			t.Error("BeginHibernate returned true after racing with an in-flight request")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("BeginHibernate did not return after ServeHTTP released the route lock")
	}

	if !p.HasLiveReplica("app") {
		t.Error("pool was removed despite a request that was in flight when BeginHibernate was attempted")
	}
}

func TestProxy_LeastConnectionsDistribution(t *testing.T) {
	var hits0, hits1 atomic.Int64
	b0 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hits0.Add(1) }))
	b1 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hits1.Add(1) }))
	defer b0.Close()
	defer b1.Close()

	p := proxy.New()
	p.SetPoolSize("demo", 2)
	_ = p.RegisterReplica("demo", 0, b0.URL)
	_ = p.RegisterReplica("demo", 1, b1.URL)

	for i := 0; i < 20; i++ {
		req := httptest.NewRequest(http.MethodGet, "/app/demo/", nil)
		p.ServeHTTP(httptest.NewRecorder(), req)
	}
	if hits0.Load() == 0 || hits1.Load() == 0 {
		t.Fatalf("expected both replicas to be hit: %d / %d", hits0.Load(), hits1.Load())
	}
}

// blockingBackend returns a test server whose handler blocks on the provided
// release channel. Callers must close(release) to let in-flight requests
// drain before the test returns; otherwise goroutines leak past the test.
func blockingBackend(release chan struct{}) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-release
	}))
}

// waitForCount polls ReplicaSessionCounts until the predicate matches or the
// deadline elapses; returns the last sampled counts either way.
func waitForCount(p *proxy.Proxy, slug string, pred func([]int64) bool) []int64 {
	deadline := time.Now().Add(2 * time.Second)
	var counts []int64
	for time.Now().Before(deadline) {
		counts = p.ReplicaSessionCounts(slug)
		if pred(counts) {
			return counts
		}
		time.Sleep(5 * time.Millisecond)
	}
	return counts
}

// TestProxy_SessionCap_Sheds503WhenAllSaturated verifies that when every
// replica in the pool has reached the per-replica cap, a new cookie-less
// request is rejected with 503 + Retry-After rather than forwarded.
func TestProxy_SessionCap_Sheds503WhenAllSaturated(t *testing.T) {
	release := make(chan struct{})
	b0 := blockingBackend(release)
	defer b0.Close()
	b1 := blockingBackend(release)
	defer b1.Close()

	p := proxy.New()
	p.SetPoolSize("demo", 2)
	_ = p.RegisterReplica("demo", 0, b0.URL)
	_ = p.RegisterReplica("demo", 1, b1.URL)
	p.SetPoolCap("demo", 1) // 1 in-flight per replica = 2 total capacity

	// Launch 2 requests that will block in the backend handlers, pinning
	// activeConns at 1 per replica. Each is cookie-less so the least-
	// connections picker routes them to distinct replicas.
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			req := httptest.NewRequest(http.MethodGet, "/app/demo/", nil)
			p.ServeHTTP(httptest.NewRecorder(), req)
		}()
	}
	counts := waitForCount(p, "demo", func(c []int64) bool {
		return len(c) == 2 && c[0] >= 1 && c[1] >= 1
	})
	if len(counts) != 2 || counts[0] < 1 || counts[1] < 1 {
		close(release)
		wg.Wait()
		t.Fatalf("pool not saturated: %v", counts)
	}

	// Now the assertion under test: a new cookie-less request must be shed.
	req := httptest.NewRequest(http.MethodGet, "/app/demo/", nil)
	rec := httptest.NewRecorder()
	p.ServeHTTP(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("expected 503 when pool saturated, got %d", rec.Code)
	}
	if ra := rec.Header().Get("Retry-After"); ra == "" {
		t.Error("expected Retry-After header on 503 shedding response")
	}

	close(release)
	wg.Wait()
}

// TestProxy_SessionCap_StickyCookieBypassesCap verifies that a request with a
// valid sticky cookie is forwarded even when the chosen replica is at or
// above the cap — the cap exists to stop *new* sessions overwhelming the
// pool; dropping an established session would kill a live WS connection.
func TestProxy_SessionCap_StickyCookieBypassesCap(t *testing.T) {
	release := make(chan struct{})
	b0 := blockingBackend(release)
	defer b0.Close()
	b1 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "sticky-ok")
	}))
	defer b1.Close()

	p := proxy.New()
	p.SetPoolSize("demo", 2)
	_ = p.RegisterReplica("demo", 0, b0.URL)
	_ = p.RegisterReplica("demo", 1, b1.URL)
	p.SetPoolCap("demo", 1)

	// Pin one in-flight request against replica 0 so it sits at cap.
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		req := httptest.NewRequest(http.MethodGet, "/app/demo/", nil)
		req.AddCookie(&http.Cookie{Name: "shinyhub_rep_demo", Value: "0"})
		p.ServeHTTP(httptest.NewRecorder(), req)
	}()
	counts := waitForCount(p, "demo", func(c []int64) bool { return len(c) == 2 && c[0] >= 1 })
	if counts[0] < 1 {
		close(release)
		wg.Wait()
		t.Fatalf("replica 0 never saturated: %v", counts)
	}

	// Sticky cookie to replica 1 must short-circuit the saturation check
	// and forward — without the sticky bypass the test would 503.
	req := httptest.NewRequest(http.MethodGet, "/app/demo/", nil)
	req.AddCookie(&http.Cookie{Name: "shinyhub_rep_demo", Value: "1"})
	rec := httptest.NewRecorder()
	p.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Errorf("expected sticky hit to forward (200), got %d body=%q", rec.Code, rec.Body.String())
	}
	if rec.Body.String() != "sticky-ok" {
		t.Errorf("expected sticky reply from replica 1, got %q", rec.Body.String())
	}

	close(release)
	wg.Wait()
}

// TestProxy_SessionCap_ZeroMeansUnlimited verifies that cap=0 disables the
// saturation check even when connection counts are high: a new cookie-less
// request is forwarded rather than shed.
func TestProxy_SessionCap_ZeroMeansUnlimited(t *testing.T) {
	release := make(chan struct{})
	b0 := blockingBackend(release)
	defer b0.Close()

	p := proxy.New()
	p.SetPoolSize("demo", 1)
	_ = p.RegisterReplica("demo", 0, b0.URL)
	p.SetPoolCap("demo", 0) // explicit "unlimited"

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		req := httptest.NewRequest(http.MethodGet, "/app/demo/", nil)
		p.ServeHTTP(httptest.NewRecorder(), req)
	}()
	if c := waitForCount(p, "demo", func(c []int64) bool { return len(c) == 1 && c[0] >= 1 }); c[0] < 1 {
		close(release)
		wg.Wait()
		t.Fatalf("replica 0 never became busy: %v", c)
	}

	// With cap=0, a cookie-less request must not 503. It will block in the
	// backend handler (because cap=0 lets us past the gate and the backend
	// is still blocked on release), so drive it on a goroutine and assert
	// the recorded status is NOT 503 after a brief settling delay.
	req := httptest.NewRequest(http.MethodGet, "/app/demo/", nil)
	rec := httptest.NewRecorder()
	wg.Add(1)
	go func() {
		defer wg.Done()
		p.ServeHTTP(rec, req)
	}()
	// A 503 shed happens synchronously before the backend is touched, so
	// 50 ms is ample to rule it out without race-ing with a slow backend.
	time.Sleep(50 * time.Millisecond)
	if rec.Code == http.StatusServiceUnavailable {
		t.Errorf("cap=0 must not shed: got 503")
	}

	close(release)
	wg.Wait()
}

// TestProxy_ReplicaSessionCounts_ReflectsInFlight verifies that
// ReplicaSessionCounts returns -1 for empty slots and tracks live connections.
func TestProxy_ReplicaSessionCounts_ReflectsInFlight(t *testing.T) {
	release := make(chan struct{})
	b0 := blockingBackend(release)
	defer b0.Close()

	p := proxy.New()
	p.SetPoolSize("demo", 2) // 2 slots, but only slot 0 is registered
	_ = p.RegisterReplica("demo", 0, b0.URL)

	counts := p.ReplicaSessionCounts("demo")
	if len(counts) != 2 {
		close(release)
		t.Fatalf("expected 2 slots, got %d", len(counts))
	}
	if counts[0] != 0 {
		t.Errorf("expected slot 0 idle with count=0, got %d", counts[0])
	}
	if counts[1] != -1 {
		t.Errorf("expected nil slot 1 to return -1, got %d", counts[1])
	}

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		req := httptest.NewRequest(http.MethodGet, "/app/demo/", nil)
		p.ServeHTTP(httptest.NewRecorder(), req)
	}()
	got := waitForCount(p, "demo", func(c []int64) bool { return len(c) == 2 && c[0] == 1 })
	if got[0] != 1 {
		t.Errorf("expected slot 0 to show 1 in-flight, got %d", got[0])
	}
	if got[1] != -1 {
		t.Errorf("nil slot 1 should still be -1, got %d", got[1])
	}

	close(release)
	wg.Wait()
}

// TestProxy_PoolCap_ReadsBack verifies PoolCap returns whatever SetPoolCap
// last stored (and 0 for unknown slugs).
func TestProxy_PoolCap_ReadsBack(t *testing.T) {
	p := proxy.New()
	if got := p.PoolCap("nope"); got != 0 {
		t.Errorf("unknown slug should return 0, got %d", got)
	}
	p.SetPoolCap("demo", 10)
	if got := p.PoolCap("demo"); got != 10 {
		t.Errorf("expected cap=10, got %d", got)
	}
	p.SetPoolCap("demo", 0)
	if got := p.PoolCap("demo"); got != 0 {
		t.Errorf("expected cap=0 after reset, got %d", got)
	}
}
