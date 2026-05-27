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
	if err := p.RegisterReplica("app", 0, backend0.URL, nil); err != nil {
		t.Fatal(err)
	}
	if err := p.RegisterReplica("app", 1, backend1.URL, nil); err != nil {
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

	if err := p.RegisterReplica("demo", 0, "http://127.0.0.1:20001", nil); err != nil {
		t.Fatal(err)
	}
	if err := p.RegisterReplica("demo", 1, "http://127.0.0.1:20002", nil); err != nil {
		t.Fatal(err)
	}
	if err := p.RegisterReplica("demo", 3, "http://x", nil); err == nil {
		t.Fatal("expected error for out-of-range index")
	}
}

func TestProxy_DeregisterReplica(t *testing.T) {
	p := proxy.New()
	p.SetPoolSize("demo", 2)
	_ = p.RegisterReplica("demo", 0, "http://127.0.0.1:20001", nil)
	_ = p.RegisterReplica("demo", 1, "http://127.0.0.1:20002", nil)

	if !p.DeregisterReplicaIfTarget("demo", 0, "http://127.0.0.1:20001") {
		t.Fatal("expected replica 0 to be deregistered")
	}
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
	_ = p.RegisterReplica("demo", 0, b0.URL, nil)
	_ = p.RegisterReplica("demo", 1, b1.URL, nil)

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
	_ = p.RegisterReplica("demo", 0, b0.URL, nil)

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
	_ = p.RegisterReplica("demo", 0, b0.URL, nil)
	_ = p.RegisterReplica("demo", 1, b1.URL, nil)

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
	_ = p.RegisterReplica("demo", 0, b0.URL, nil)
	_ = p.RegisterReplica("demo", 1, b1.URL, nil)
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
	_ = p.RegisterReplica("demo", 0, b0.URL, nil)
	_ = p.RegisterReplica("demo", 1, b1.URL, nil)
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
	_ = p.RegisterReplica("demo", 0, b0.URL, nil)
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

// TestProxy_DrainReplica_NoNewCookielessSessions verifies that once a slot is
// marked draining, the least-connections picker stops routing new cookie-less
// requests to it: every cookie-less request lands on the surviving replica.
// This is the routing half of graceful scale-down.
func TestProxy_DrainReplica_NoNewCookielessSessions(t *testing.T) {
	var hits0, hits1 atomic.Int64
	b0 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hits0.Add(1) }))
	b1 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hits1.Add(1) }))
	defer b0.Close()
	defer b1.Close()

	p := proxy.New()
	p.SetPoolSize("demo", 2)
	_ = p.RegisterReplica("demo", 0, b0.URL, nil)
	_ = p.RegisterReplica("demo", 1, b1.URL, nil)

	if !p.DrainReplica("demo", 0) {
		t.Fatal("DrainReplica returned false for a live slot")
	}
	if !p.IsDraining("demo", 0) {
		t.Fatal("IsDraining is false right after DrainReplica")
	}

	for i := 0; i < 20; i++ {
		req := httptest.NewRequest(http.MethodGet, "/app/demo/", nil)
		p.ServeHTTP(httptest.NewRecorder(), req)
	}
	if hits0.Load() != 0 {
		t.Errorf("draining replica 0 received %d new cookie-less requests; want 0", hits0.Load())
	}
	if hits1.Load() != 20 {
		t.Errorf("surviving replica 1 received %d requests; want all 20", hits1.Load())
	}
}

// TestProxy_DrainReplica_StickySessionsStillRouted verifies that a draining
// slot still serves requests carrying its sticky cookie: established sessions
// finish on the replica being drained rather than being severed. This is the
// session-preservation half of graceful scale-down.
func TestProxy_DrainReplica_StickySessionsStillRouted(t *testing.T) {
	b0 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "drained-replica-0")
	}))
	b1 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "replica-1")
	}))
	defer b0.Close()
	defer b1.Close()

	p := proxy.New()
	p.SetPoolSize("demo", 2)
	_ = p.RegisterReplica("demo", 0, b0.URL, nil)
	_ = p.RegisterReplica("demo", 1, b1.URL, nil)
	p.DrainReplica("demo", 0)

	req := httptest.NewRequest(http.MethodGet, "/app/demo/", nil)
	req.AddCookie(&http.Cookie{Name: "shinyhub_rep_demo", Value: "0"})
	rec := httptest.NewRecorder()
	p.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("sticky request to draining replica got %d, want 200", rec.Code)
	}
	if rec.Body.String() != "drained-replica-0" {
		t.Errorf("sticky session was not served by the draining replica: got %q", rec.Body.String())
	}
}

// TestProxy_DrainReplica_MissingSlotReturnsFalse verifies the primitive reports
// failure for an absent pool, an out-of-range index, and a nil slot, so callers
// can distinguish "marked draining" from "nothing to drain".
func TestProxy_DrainReplica_MissingSlotReturnsFalse(t *testing.T) {
	p := proxy.New()
	if p.DrainReplica("ghost", 0) {
		t.Error("DrainReplica returned true for an unregistered pool")
	}
	p.SetPoolSize("demo", 1) // slot 0 exists but is nil (no backend registered)
	if p.DrainReplica("demo", 0) {
		t.Error("DrainReplica returned true for a nil slot")
	}
	if p.DrainReplica("demo", 5) {
		t.Error("DrainReplica returned true for an out-of-range index")
	}
	if p.IsDraining("demo", 0) {
		t.Error("IsDraining should be false for a slot that was never drained")
	}
}

// TestProxy_UndrainReplica_RestoresRouting verifies that clearing the drain flag
// puts the slot back into the least-connections rotation. This is the rollback
// used when a scale-down aborts after marking a slot draining (e.g. the stop
// failed): the still-running replica must resume serving new cookie-less
// sessions rather than be left half-drained.
func TestProxy_UndrainReplica_RestoresRouting(t *testing.T) {
	var hits0, hits1 atomic.Int64
	b0 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hits0.Add(1) }))
	b1 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hits1.Add(1) }))
	defer b0.Close()
	defer b1.Close()

	p := proxy.New()
	p.SetPoolSize("demo", 2)
	_ = p.RegisterReplica("demo", 0, b0.URL, nil)
	_ = p.RegisterReplica("demo", 1, b1.URL, nil)

	p.DrainReplica("demo", 0)
	if !p.UndrainReplica("demo", 0) {
		t.Fatal("UndrainReplica returned false for a live drained slot")
	}
	if p.IsDraining("demo", 0) {
		t.Fatal("slot still reports draining after UndrainReplica")
	}

	// With the drain cleared both slots are eligible again, so least-connections
	// spreads cookie-less requests across both rather than avoiding slot 0.
	for i := 0; i < 20; i++ {
		req := httptest.NewRequest(http.MethodGet, "/app/demo/", nil)
		p.ServeHTTP(httptest.NewRecorder(), req)
	}
	if hits0.Load() == 0 {
		t.Errorf("undrained replica 0 received no requests; want it back in rotation")
	}
}

// TestProxy_UndrainReplica_MissingSlotReturnsFalse verifies the primitive reports
// failure for an absent pool, an out-of-range index, and a nil slot, mirroring
// DrainReplica so callers can treat a missing slot uniformly.
func TestProxy_UndrainReplica_MissingSlotReturnsFalse(t *testing.T) {
	p := proxy.New()
	if p.UndrainReplica("ghost", 0) {
		t.Error("UndrainReplica returned true for an unregistered pool")
	}
	p.SetPoolSize("demo", 1) // slot 0 exists but is nil
	if p.UndrainReplica("demo", 0) {
		t.Error("UndrainReplica returned true for a nil slot")
	}
	if p.UndrainReplica("demo", 5) {
		t.Error("UndrainReplica returned true for an out-of-range index")
	}
}

// TestProxy_ReplicaSessionCounts_ReflectsInFlight verifies that
// ReplicaSessionCounts returns -1 for empty slots and tracks live connections.
func TestProxy_ReplicaSessionCounts_ReflectsInFlight(t *testing.T) {
	release := make(chan struct{})
	b0 := blockingBackend(release)
	defer b0.Close()

	p := proxy.New()
	p.SetPoolSize("demo", 2) // 2 slots, but only slot 0 is registered
	_ = p.RegisterReplica("demo", 0, b0.URL, nil)

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

// TestProxy_ReadyProbe_BeforeWSUpgrade asserts that the readiness endpoint
// reports 503 before any WebSocket handshake has been observed for the slug,
// even when a backend is registered. "Process started" is not the same as
// "actually accepting WS connections" — this distinction is the whole point
// of the endpoint.
func TestProxy_ReadyProbe_BeforeWSUpgrade(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("ok"))
	}))
	defer backend.Close()

	p := proxy.New()
	if err := p.Register("demo", backend.URL); err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodGet, "/app/demo/.shinyhub/ready", nil)
	rec := httptest.NewRecorder()
	p.ServeHTTP(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503 before any WS upgrade", rec.Code)
	}
	if got := rec.Header().Get("Content-Type"); got != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", got)
	}
	if got := rec.Header().Get("Retry-After"); got != "1" {
		t.Errorf("Retry-After = %q, want 1", got)
	}
	if got := rec.Body.String(); got != `{"ready":false}` {
		t.Errorf("body = %q, want {\"ready\":false}", got)
	}
}

// TestProxy_ReadyProbe_AfterWSUpgrade asserts that once MarkWSReady is
// invoked for a slug, the readiness endpoint reports 200. The wire-up
// from the reverse-proxy 101 status to MarkWSReady is covered by
// TestStatusRecorderOnUpgradeFiresOn101 in recorder_test.go.
func TestProxy_ReadyProbe_AfterWSUpgrade(t *testing.T) {
	p := proxy.New()
	p.SetPoolSize("demo", 1)

	p.MarkWSReady("demo")

	probe := httptest.NewRequest(http.MethodGet, "/app/demo/.shinyhub/ready", nil)
	rec := httptest.NewRecorder()
	p.ServeHTTP(rec, probe)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 after MarkWSReady", rec.Code)
	}
	if got := rec.Body.String(); got != `{"ready":true}` {
		t.Errorf("body = %q, want {\"ready\":true}", got)
	}
	if got := rec.Header().Get("Cache-Control"); got != "no-store" {
		t.Errorf("Cache-Control = %q, want no-store", got)
	}
}

// TestProxy_ReadyProbe_ClearedByLifecycleEvents ensures readiness is dropped
// when a slug is deregistered, hibernated, or has a replica re-registered
// (hot redeploy). Stale "true" is the dangerous failure mode — it would let
// a CI script claim success after a deploy that hasn't actually accepted
// any WS traffic yet — so every lifecycle event resets the flag.
func TestProxy_ReadyProbe_ClearedByLifecycleEvents(t *testing.T) {
	cases := []struct {
		name  string
		reset func(p *proxy.Proxy)
	}{
		{"Deregister", func(p *proxy.Proxy) { p.Deregister("demo") }},
		{"DeregisterReplicaIfTarget", func(p *proxy.Proxy) { p.DeregisterReplicaIfTarget("demo", 0, "http://127.0.0.1:1") }},
		{"BeginHibernate", func(p *proxy.Proxy) {
			// Pass a future time so the lastSeen check passes
			// (no requests have been recorded for this slug).
			if !p.BeginHibernate("demo", time.Now().Add(time.Hour)) {
				t.Fatal("BeginHibernate returned false")
			}
		}},
		{"RegisterReplica swap", func(p *proxy.Proxy) {
			// Hot redeploy: re-register replica 0 in-place.
			if err := p.RegisterReplica("demo", 0, "http://127.0.0.1:1", nil); err != nil {
				t.Fatal(err)
			}
		}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := proxy.New()
			if err := p.Register("demo", "http://127.0.0.1:1"); err != nil {
				t.Fatal(err)
			}
			p.MarkWSReady("demo")
			if !p.IsWSReady("demo") {
				t.Fatalf("precondition: IsWSReady should be true after MarkWSReady")
			}

			tc.reset(p)

			if p.IsWSReady("demo") {
				t.Errorf("readiness should be cleared by %s", tc.name)
			}
		})
	}
}

// TestProxy_ReadyProbe_MethodNotAllowed rejects writes to the probe so a
// misconfigured client fails loudly rather than appearing to succeed.
func TestProxy_ReadyProbe_MethodNotAllowed(t *testing.T) {
	p := proxy.New()
	req := httptest.NewRequest(http.MethodPost, "/app/demo/.shinyhub/ready", nil)
	rec := httptest.NewRecorder()
	p.ServeHTTP(rec, req)

	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("status = %d, want 405", rec.Code)
	}
	if got := rec.Header().Get("Allow"); got != "GET, HEAD" {
		t.Errorf("Allow = %q, want GET, HEAD", got)
	}
}

// TestProxy_ReadyProbe_HEADHasNoBody ensures HEAD callers get the right
// status code without a body — required for any HTTP health checker that
// uses HEAD to minimise bandwidth.
func TestProxy_ReadyProbe_HEADHasNoBody(t *testing.T) {
	p := proxy.New()
	p.SetPoolSize("demo", 1)

	req := httptest.NewRequest(http.MethodHead, "/app/demo/.shinyhub/ready", nil)
	rec := httptest.NewRecorder()
	p.ServeHTTP(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503", rec.Code)
	}
	if rec.Body.Len() != 0 {
		t.Errorf("HEAD body should be empty, got %d bytes", rec.Body.Len())
	}
}

// TestProxy_ReadyProbe_DoesNotRecordActivity guards against the readiness
// endpoint inadvertently keeping the app alive — health-probe traffic must
// not defeat hibernation.
func TestProxy_ReadyProbe_DoesNotRecordActivity(t *testing.T) {
	p := proxy.New()
	p.SetPoolSize("demo", 1)

	before := p.LastSeen("demo")
	req := httptest.NewRequest(http.MethodGet, "/app/demo/.shinyhub/ready", nil)
	p.ServeHTTP(httptest.NewRecorder(), req)
	after := p.LastSeen("demo")

	if !before.Equal(after) {
		t.Errorf("ready probe must not update LastSeen: before=%v after=%v", before, after)
	}
}

// TestProxy_ReadyProbe_UnknownSlugReturns404 asserts that when the slugExists
// predicate confidently reports the slug is unknown, the readiness probe
// returns 404 (not 503). Collapsing "no such app" into the cold-start 503 lets
// a smoke test pass against a server whose registry is empty — exactly the
// deploy regression external monitoring is meant to catch. The body identifies
// the slug so the caller knows which app this server is missing.
func TestProxy_ReadyProbe_UnknownSlugReturns404(t *testing.T) {
	p := proxy.New()
	p.SetSlugExists(func(slug string) (bool, error) { return slug == "known", nil })

	req := httptest.NewRequest(http.MethodGet, "/app/missing/.shinyhub/ready", nil)
	rec := httptest.NewRecorder()
	p.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 for unknown slug", rec.Code)
	}
	if got := rec.Header().Get("Content-Type"); got != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", got)
	}
	if got := rec.Body.String(); got != `{"error":"unknown app","slug":"missing"}` {
		t.Errorf("body = %q, want {\"error\":\"unknown app\",\"slug\":\"missing\"}", got)
	}
}

// TestProxy_ReadyProbe_UnknownSlugReturns404RegardlessOfMethod pins the
// precedence: an unknown slug is 404 for any method, not 405. A method
// complaint about a resource that doesn't exist on this server is noise — the
// caller's first problem is that the app isn't here. Known slugs still get the
// 405 method gate (see TestProxy_ReadyProbe_MethodNotAllowed).
func TestProxy_ReadyProbe_UnknownSlugReturns404RegardlessOfMethod(t *testing.T) {
	p := proxy.New()
	p.SetSlugExists(func(slug string) (bool, error) { return false, nil })

	for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodDelete} {
		req := httptest.NewRequest(method, "/app/missing/.shinyhub/ready", nil)
		rec := httptest.NewRecorder()
		p.ServeHTTP(rec, req)

		if rec.Code != http.StatusNotFound {
			t.Errorf("%s: status = %d, want 404 for unknown slug", method, rec.Code)
		}
	}
}

// TestProxy_ReadyProbe_KnownNotReadyStays503 asserts that a known app that has
// not yet completed a WS handshake still returns the cold-start 503 — the 404
// path must fire only for slugs the predicate confidently rejects.
func TestProxy_ReadyProbe_KnownNotReadyStays503(t *testing.T) {
	p := proxy.New()
	p.SetSlugExists(func(slug string) (bool, error) { return true, nil })

	req := httptest.NewRequest(http.MethodGet, "/app/known/.shinyhub/ready", nil)
	rec := httptest.NewRecorder()
	p.ServeHTTP(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503 for known-but-not-ready app", rec.Code)
	}
	if got := rec.Body.String(); got != `{"ready":false}` {
		t.Errorf("body = %q, want {\"ready\":false}", got)
	}
}

// TestProxy_ReadyProbe_LookupErrorStays503 guards the fail-open contract: when
// the predicate cannot tell whether the slug exists (DB unavailable, ctx
// cancelled), the probe must NOT 404. A momentary lookup failure 404ing a real
// app's readiness probe would surface as a phantom "app deleted" during normal
// operation. Falling through to the cold-start 503 is the safe default.
func TestProxy_ReadyProbe_LookupErrorStays503(t *testing.T) {
	p := proxy.New()
	p.SetSlugExists(func(slug string) (bool, error) {
		return false, errors.New("database is locked")
	})

	req := httptest.NewRequest(http.MethodGet, "/app/maybe-real/.shinyhub/ready", nil)
	rec := httptest.NewRecorder()
	p.ServeHTTP(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503 on lookup error (fail-open)", rec.Code)
	}
}

// TestProxy_ReadyProbe_NilPredicateStays503 pins the legacy default: with no
// slugExists predicate wired the proxy cannot distinguish known from unknown,
// so it must preserve the pre-existing cold-start 503 rather than 404 every
// slug.
func TestProxy_ReadyProbe_NilPredicateStays503(t *testing.T) {
	p := proxy.New()

	req := httptest.NewRequest(http.MethodGet, "/app/whatever/.shinyhub/ready", nil)
	rec := httptest.NewRecorder()
	p.ServeHTTP(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503 with nil predicate", rec.Code)
	}
}

// TestProxy_ReadyProbe_UnknownSlugHEADHasNoBody ensures the 404 path honours
// HEAD: status only, no body, so HEAD-based health checkers still parse the
// status line correctly.
func TestProxy_ReadyProbe_UnknownSlugHEADHasNoBody(t *testing.T) {
	p := proxy.New()
	p.SetSlugExists(func(slug string) (bool, error) { return false, nil })

	req := httptest.NewRequest(http.MethodHead, "/app/missing/.shinyhub/ready", nil)
	rec := httptest.NewRecorder()
	p.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
	if rec.Body.Len() != 0 {
		t.Errorf("HEAD body should be empty, got %d bytes", rec.Body.Len())
	}
}

// TestProxy_ReadyProbe_ReadyShortCircuitsExistenceCheck pins the ordering
// decision: a slug that has handshaked a WebSocket is serving traffic and must
// report 200 even if the existence predicate would call it unknown. Readiness
// wins because a live replica is ground truth that the app is present — 404ing
// something actively serving would be a worse lie than any registry skew. This
// also keeps the steady-state 200 path off the predicate's database lookup.
func TestProxy_ReadyProbe_ReadyShortCircuitsExistenceCheck(t *testing.T) {
	p := proxy.New()
	var predicateCalls int
	p.SetSlugExists(func(slug string) (bool, error) {
		predicateCalls++
		return false, nil // would say "unknown" if consulted
	})
	p.MarkWSReady("demo")

	req := httptest.NewRequest(http.MethodGet, "/app/demo/.shinyhub/ready", nil)
	rec := httptest.NewRecorder()
	p.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 for a ready slug", rec.Code)
	}
	if predicateCalls != 0 {
		t.Errorf("existence predicate called %d times; a ready slug must skip the lookup", predicateCalls)
	}
}

// TestProxy_UnknownSlugBodyIdentifiesSlug asserts the regular /app/<slug>/ miss
// path returns the same slug-identifying JSON 404 as the probe, so any unknown
// slug under /app/ produces a consistent, machine-readable signal.
func TestProxy_UnknownSlugBodyIdentifiesSlug(t *testing.T) {
	p := proxy.New()
	p.SetSlugExists(func(slug string) (bool, error) { return slug == "known", nil })

	req := httptest.NewRequest(http.MethodGet, "/app/typo/", nil)
	rec := httptest.NewRecorder()
	p.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
	if got := rec.Header().Get("Content-Type"); got != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", got)
	}
	if got := rec.Body.String(); got != `{"error":"unknown app","slug":"typo"}` {
		t.Errorf("body = %q, want {\"error\":\"unknown app\",\"slug\":\"typo\"}", got)
	}
}

// TestRegisterReplica_PrependsTargetPathAndUsesTransport verifies that a remote
// tunnel URL's path prefix is prepended to the app-relative path and that the
// caller-supplied transport is used for all outbound requests.
func TestRegisterReplica_PrependsTargetPathAndUsesTransport(t *testing.T) {
	var gotPath string
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.WriteHeader(http.StatusOK)
	}))
	defer backend.Close()

	p := proxy.New()
	p.SetPoolSize("app", 1)

	target := backend.URL + "/v1/data/tok123"

	used := false
	tr := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		used = true
		return http.DefaultTransport.RoundTrip(r)
	})

	if err := p.RegisterReplica("app", 0, target, tr); err != nil {
		t.Fatalf("RegisterReplica: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/app/app/health/ready", nil)
	rec := httptest.NewRecorder()
	p.ServeHTTP(rec, req)

	if !used {
		t.Error("custom transport was not used")
	}
	if gotPath != "/v1/data/tok123/health/ready" {
		t.Errorf("backend path = %q, want /v1/data/tok123/health/ready", gotPath)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
