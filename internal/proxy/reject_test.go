package proxy_test

import (
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/rvben/shinyhub/internal/proxy"
)

// occupyPoolToCap registers replica 0 and pins it at the per-replica cap (1) by
// holding one in-flight request open, so the pool reports saturated. The backend
// blocks ONLY its first request (the held one); any later request it receives -
// e.g. a sticky-cookie hit that is forwarded despite saturation - is served
// immediately, so a caller that forwards a second request does not deadlock.
// size is the configured pool size: pass size > 1 with this single live replica
// to exercise the degraded branch (live count < configured size). Returns a
// release func that unblocks the held connection and waits for it to drain.
func occupyPoolToCap(t *testing.T, p *proxy.Proxy, slug string, size int) func() {
	t.Helper()
	release := make(chan struct{})
	var holdOnce sync.Once
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		held := false
		holdOnce.Do(func() { held = true })
		if held {
			<-release // pin exactly one connection at the cap until released
		}
		w.WriteHeader(http.StatusOK)
	}))
	p.SetPoolSize(slug, size)
	p.SetPoolCap(slug, 1) // per-replica cap = 1
	if err := p.RegisterReplica(slug, 0, backend.URL, nil); err != nil {
		t.Fatalf("register: %v", err)
	}
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		req := httptest.NewRequest(http.MethodGet, "/app/"+slug+"/", nil)
		p.ServeHTTP(httptest.NewRecorder(), req)
	}()
	// Wait until the held request is actually in flight (activeConns==1) so the
	// pool is observably at cap before the caller probes it.
	waitForCount(p, slug, func(c []int64) bool { return len(c) >= 1 && c[0] == 1 })
	return func() {
		close(release)
		wg.Wait()
		backend.Close()
	}
}

func TestServeHTTP_RejectHeader_PoolSaturated(t *testing.T) {
	p := proxy.New()
	var entry proxy.AccessLogEntry
	var mu sync.Mutex
	p.SetAccessLogger(func(e proxy.AccessLogEntry) {
		if e.Status == http.StatusServiceUnavailable {
			mu.Lock()
			entry = e
			mu.Unlock()
		}
	})
	done := occupyPoolToCap(t, p, "demo", 1) // size==1, one live replica at cap
	defer done()

	req := httptest.NewRequest(http.MethodGet, "/app/demo/", nil)
	rec := httptest.NewRecorder()
	p.ServeHTTP(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", rec.Code)
	}
	if got := rec.Header().Get("X-Shinyhub-Reject"); got != "pool-saturated" {
		t.Errorf("X-Shinyhub-Reject = %q, want pool-saturated", got)
	}
	mu.Lock()
	defer mu.Unlock()
	if entry.Reject != proxy.ReasonPoolSaturated {
		t.Errorf("access-log Reject = %q, want pool-saturated", entry.Reject)
	}
}

func TestServeHTTP_RejectHeader_PoolDegraded(t *testing.T) {
	p := proxy.New()
	var entry proxy.AccessLogEntry
	var mu sync.Mutex
	p.SetAccessLogger(func(e proxy.AccessLogEntry) {
		if e.Status == http.StatusServiceUnavailable {
			mu.Lock()
			entry = e
			mu.Unlock()
		}
	})
	// Configured size 2 but only replica 0 is live and at cap -> degraded.
	done := occupyPoolToCap(t, p, "demo", 2)
	defer done()

	req := httptest.NewRequest(http.MethodGet, "/app/demo/", nil)
	rec := httptest.NewRecorder()
	p.ServeHTTP(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", rec.Code)
	}
	if got := rec.Header().Get("X-Shinyhub-Reject"); got != "pool-degraded" {
		t.Errorf("X-Shinyhub-Reject = %q, want pool-degraded", got)
	}
	mu.Lock()
	defer mu.Unlock()
	if entry.Reject != proxy.ReasonPoolDegraded {
		t.Errorf("access-log Reject = %q, want pool-degraded", entry.Reject)
	}
}

func TestServeHTTP_RejectHeader_UnknownSlug(t *testing.T) {
	p := proxy.New()
	var entry proxy.AccessLogEntry
	var mu sync.Mutex
	p.SetAccessLogger(func(e proxy.AccessLogEntry) {
		if e.Status == http.StatusNotFound {
			mu.Lock()
			entry = e
			mu.Unlock()
		}
	})
	p.SetSlugExists(func(slug string) (bool, error) { return slug == "known", nil })

	req := httptest.NewRequest(http.MethodGet, "/app/missing/", nil)
	rec := httptest.NewRecorder()
	p.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
	if got := rec.Header().Get("X-Shinyhub-Reject"); got != "unknown-slug" {
		t.Errorf("X-Shinyhub-Reject = %q, want unknown-slug", got)
	}
	mu.Lock()
	defer mu.Unlock()
	if entry.Reject != proxy.ReasonUnknownSlug {
		t.Errorf("access-log Reject = %q, want unknown-slug", entry.Reject)
	}
}

func TestServeHTTP_RejectHeader_AppNotReady(t *testing.T) {
	p := proxy.New()
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer backend.Close()
	if err := p.Register("demo", backend.URL); err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodGet, "/app/demo/.shinyhub/ready", nil)
	rec := httptest.NewRecorder()
	p.ServeHTTP(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", rec.Code)
	}
	if got := rec.Header().Get("X-Shinyhub-Reject"); got != "app-not-ready" {
		t.Errorf("X-Shinyhub-Reject = %q, want app-not-ready", got)
	}
	// Readiness rejections must NOT produce an access-log entry on this path.
	var logged bool
	p.SetAccessLogger(func(proxy.AccessLogEntry) { logged = true })
	p.ServeHTTP(httptest.NewRecorder(), req)
	if logged {
		t.Error("readiness probe rejection should not be access-logged")
	}
}

func TestServeHTTP_ReadyProbe_FailOpenCollapsesToSentinel(t *testing.T) {
	p := proxy.New()
	// No SetSlugExists (nil predicate => fail-open: slugConfidentlyUnknown is
	// false) and no Register (no pool). A readiness poll for an unregistered slug
	// must still 503 app-not-ready, but the count must collapse to the sentinel
	// so a junk-slug flood cannot create unbounded keys / Prometheus series.
	req := httptest.NewRequest(http.MethodGet, "/app/ghost/.shinyhub/ready", nil)
	rec := httptest.NewRecorder()
	p.ServeHTTP(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", rec.Code)
	}
	if got := rec.Header().Get("X-Shinyhub-Reject"); got != "app-not-ready" {
		t.Errorf("X-Shinyhub-Reject = %q, want app-not-ready", got)
	}
	// The raw slug must NOT be a counter key (would be attacker-controlled).
	if got := p.RejectsByReason("ghost", 10*time.Minute); got != nil {
		t.Errorf("raw slug recorded: %v, want nil (collapsed to sentinel)", got)
	}
	// The event is recorded under the sentinel instead.
	if got := p.RejectsByReason("__unknown__", 10*time.Minute); got[proxy.ReasonAppNotReady] != 1 {
		t.Errorf("sentinel app-not-ready = %d, want 1", got[proxy.ReasonAppNotReady])
	}
}

func TestServeHTTP_StickyHit_NoRejectHeader(t *testing.T) {
	p := proxy.New()
	done := occupyPoolToCap(t, p, "demo", 1)
	defer done()

	// A request carrying the sticky cookie for the live replica is forwarded
	// even though the pool is saturated (the held connection occupies the cap),
	// so it reaches the backend (served immediately as the non-held request) and
	// must NOT be tagged as rejected.
	req := httptest.NewRequest(http.MethodGet, "/app/demo/", nil)
	req.AddCookie(&http.Cookie{Name: "shinyhub_rep_demo", Value: "0"})
	rec := httptest.NewRecorder()
	p.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (sticky request forwarded to backend)", rec.Code)
	}
	if got := rec.Header().Get("X-Shinyhub-Reject"); got != "" {
		t.Errorf("X-Shinyhub-Reject = %q, want empty (forwarded)", got)
	}
}
