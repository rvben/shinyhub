package proxy_test

import (
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
