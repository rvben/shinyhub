package proxy

import (
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rvben/shinyhub/internal/admission"
)

// emptyLimiter builds a limiter whose shared bucket is permanently empty: burst
// 0 means it starts with no tokens and rate 0 means it never earns one. It is
// the "app is out of render capacity" fixture, with no timing dependency.
func emptyLimiter() *admission.AppLimiter {
	return admission.NewAppLimiter(0, 0, 1, 3, 4096)
}

// pageLoadRequest builds a top-level browser navigation the way a real browser
// sends one.
func pageLoadRequest(target string) *http.Request {
	r := httptest.NewRequest("GET", target, nil)
	r.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8")
	r.Header.Set("Sec-Fetch-Dest", "document")
	r.Header.Set("Sec-Fetch-Mode", "navigate")
	return r
}

func TestIsPageLoad_Classification(t *testing.T) {
	cases := []struct {
		name    string
		method  string
		dest    string
		accept  string
		ws      bool
		want    bool
		because string
	}{
		{
			name: "browser navigation", method: "GET", dest: "document",
			accept: "text/html,application/xhtml+xml", want: true,
			because: "the request that renders the app shell is exactly what the gate defers",
		},
		{
			name: "stylesheet", method: "GET", dest: "style",
			accept: "text/css,*/*;q=0.1", want: false,
			because: "serving HTML to a stylesheet request would break the page it styles",
		},
		{
			name: "xhr", method: "GET", dest: "empty", accept: "*/*", want: false,
			because: "a fetch expecting JSON must not receive an HTML wait page",
		},
		{
			name: "sec-fetch-dest overrides a text/html Accept", method: "GET",
			dest: "image", accept: "text/html", want: false,
			because: "Sec-Fetch-Dest is authoritative and cannot be forged by a page",
		},
		{
			name: "legacy client, html Accept, no sec-fetch-dest", method: "GET",
			dest: "", accept: "text/html,application/xhtml+xml;q=0.9", want: true,
			because: "clients without Sec-Fetch-Dest still signal a document via Accept",
		},
		{
			name: "curl", method: "GET", dest: "", accept: "*/*", want: false,
			because: "a non-browser client gets the app, not a page it cannot act on",
		},
		{
			name: "no accept header at all", method: "GET", dest: "", accept: "", want: false,
			because: "absent signals must not be read as a navigation",
		},
		{
			name: "form post", method: "POST", dest: "document",
			accept: "text/html", want: false,
			because: "replacing a submitted POST with a wait page would silently drop the submission",
		},
		{
			name: "websocket upgrade", method: "GET", dest: "empty",
			accept: "text/html", ws: true, want: false,
			because: "the upgrade is the render itself and is charged at the charge point, not deferred here",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := httptest.NewRequest(c.method, "/app/demo/", nil)
			if c.dest != "" {
				r.Header.Set("Sec-Fetch-Dest", c.dest)
			}
			if c.accept != "" {
				r.Header.Set("Accept", c.accept)
			}
			if c.ws {
				r.Header.Set("Upgrade", "websocket")
				r.Header.Set("Connection", "Upgrade")
			}
			if got := isPageLoad(r); got != c.want {
				t.Fatalf("isPageLoad = %v, want %v: %s", got, c.want, c.because)
			}
		})
	}
}

func TestIsPageLoad_SecFetchDestIsCaseInsensitive(t *testing.T) {
	r := httptest.NewRequest("GET", "/app/demo/", nil)
	r.Header.Set("Sec-Fetch-Dest", "Document")
	if !isPageLoad(r) {
		t.Fatal("Sec-Fetch-Dest matching must not depend on case")
	}
}

func TestRenderGate_PacingDisabledNeverBlocks(t *testing.T) {
	p := New() // no limiter for the slug: pacing disabled
	if p.renderGateBlocks(pageLoadRequest("/app/demo/"), "demo") {
		t.Fatal("with pacing disabled the gate must never block")
	}
}

func TestRenderGate_BlocksOnlyPageLoads(t *testing.T) {
	p := New()
	p.SetAppLimiter("demo", emptyLimiter())
	if !p.renderGateBlocks(pageLoadRequest("/app/demo/"), "demo") {
		t.Fatal("an empty bucket must defer a page load")
	}
	sub := httptest.NewRequest("GET", "/app/demo/style.css", nil)
	sub.Header.Set("Sec-Fetch-Dest", "style")
	if p.renderGateBlocks(sub, "demo") {
		t.Fatal("a sub-resource must pass through even when the bucket is empty")
	}
}

func TestRenderGate_SpendsNoToken(t *testing.T) {
	// The gate is advisory: it must never consume the capacity it reports on,
	// or a page load would refuse the very session it precedes. With exactly one
	// token and no refill, any number of gate checks must leave that token intact
	// for the WebSocket upgrade that follows.
	p := New()
	p.SetAppLimiter("demo", admission.NewAppLimiter(0, 1, 1, 3, 4096))

	for i := 0; i < 50; i++ {
		if p.renderGateBlocks(pageLoadRequest("/app/demo/"), "demo") {
			t.Fatalf("gate check %d blocked while a token was still available", i)
		}
	}
	// The token survived every check, so the real session is admitted.
	if !p.chargeRenderAdmission(httptest.NewRecorder(), renderWSUpgradeRequest("/app/demo/"), "demo") {
		t.Fatal("the upgrade must get the token the gate only looked at")
	}
	// And now that the session spent it, the gate reports the app as busy.
	if !p.renderGateBlocks(pageLoadRequest("/app/demo/"), "demo") {
		t.Fatal("gate must block once the session has actually spent the token")
	}
}

func TestRenderGate_ReflectsRefill(t *testing.T) {
	// A gate that reported a stale count would leave a recovered app showing the
	// wait page until something else happened to touch the bucket. Rate 100/s
	// with burst 1: the single token is spent, and one refills within ~10ms.
	p := New()
	p.SetAppLimiter("demo", admission.NewAppLimiter(100, 1, 1, 3, 4096))
	if !p.chargeRenderAdmission(httptest.NewRecorder(), renderWSUpgradeRequest("/app/demo/"), "demo") {
		t.Fatal("precondition: first upgrade should take the burst token")
	}
	if !p.renderGateBlocks(pageLoadRequest("/app/demo/"), "demo") {
		t.Fatal("gate should block immediately after the token was spent")
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if !p.renderGateBlocks(pageLoadRequest("/app/demo/"), "demo") {
			return // the refill became visible without any other traffic
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("gate never observed the refill; it is reading a stale token count")
}

func TestRenderGate_WatermarkBlocks(t *testing.T) {
	// A saturated host has no render capacity even with a full bucket, and the
	// charge point would shed the session that follows. The gate must say so.
	p := New()
	p.SetAppLimiter("demo", admission.NewAppLimiter(1000, 1000, 20, 3, 4096)) // never binds
	wm := admission.NewWatermark(90, func() (float64, error) { return 0, nil })
	wm.SetReadingForTest(95)
	p.SetCPUWatermark(wm)
	if !p.renderGateBlocks(pageLoadRequest("/app/demo/"), "demo") {
		t.Fatal("a breached CPU watermark must defer a page load")
	}
	wm.SetReadingForTest(10)
	if p.renderGateBlocks(pageLoadRequest("/app/demo/"), "demo") {
		t.Fatal("gate must clear as soon as the watermark does")
	}
}

// registerGatedApp stands up a real backend registered under slug, and returns
// the proxy plus a counter of requests that reached the backend.
func registerGatedApp(t *testing.T, slug string) (*Proxy, *atomic.Int64) {
	t.Helper()
	var hits atomic.Int64
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.Write([]byte("real app content")) //nolint:errcheck
	}))
	t.Cleanup(backend.Close)
	p := New()
	if err := p.Register(slug, backend.URL); err != nil {
		t.Fatal(err)
	}
	return p, &hits
}

func TestServeHTTP_GateServesWaitingPage(t *testing.T) {
	p, hits := registerGatedApp(t, "demo")
	p.SetAppLimiter("demo", emptyLimiter())

	rec := httptest.NewRecorder()
	p.ServeHTTP(rec, pageLoadRequest("/app/demo/"))

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), WaitingPageSentinel) {
		t.Fatalf("body is not the capacity wait page: %.120q", rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), LoadingPageSentinel) {
		t.Fatal("a busy app must not be reported as still starting")
	}
	if got := rec.Header().Get("X-Shinyhub-Reject"); got != string(ReasonRenderDeferred) {
		t.Fatalf("reject reason = %q, want render-deferred", got)
	}
	if got := rec.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("Cache-Control = %q, want no-store: a cached wait page never stops waiting", got)
	}
	if rec.Header().Get("Retry-After") == "" {
		t.Fatal("a 503 wait page must advise Retry-After")
	}
	if n := hits.Load(); n != 0 {
		t.Fatalf("backend saw %d requests; a deferred page load must not reach the app", n)
	}
}

func TestServeHTTP_GateLetsSubResourcesThrough(t *testing.T) {
	// The gate defers the shell, not the page's parts. If it caught sub-resources
	// it would corrupt whatever it answered with HTML, and the wait page's own
	// recovery would be the only thing that ever loaded.
	p, hits := registerGatedApp(t, "demo")
	p.SetAppLimiter("demo", emptyLimiter())

	r := httptest.NewRequest("GET", "/app/demo/static/app.css", nil)
	r.Header.Set("Sec-Fetch-Dest", "style")
	r.Header.Set("Accept", "text/css,*/*;q=0.1")
	rec := httptest.NewRecorder()
	p.ServeHTTP(rec, r)

	if rec.Code != http.StatusOK || rec.Body.String() != "real app content" {
		t.Fatalf("sub-resource got %d %q, want the app's own response", rec.Code, rec.Body.String())
	}
	if n := hits.Load(); n != 1 {
		t.Fatalf("backend saw %d requests, want 1", n)
	}
}

func TestServeHTTP_GateBindsNothing(t *testing.T) {
	// The load-bearing property of the whole design: a client held at the gate
	// costs the box strictly less than the render it is waiting for. It must not
	// reach the backend, must not occupy a replica connection slot, must not ask
	// for a worker, and must not hold a goroutine parked waiting for capacity.
	// If any of these regress, the gate stops being relief under load and becomes
	// a second source of the exhaustion it exists to prevent.
	p, hits := registerGatedApp(t, "demo")
	p.SetAppLimiter("demo", emptyLimiter())

	var spawns atomic.Int64
	p.SetSpawnFunc(func(slug string, slotID int) { spawns.Add(1) })

	// A long park TTL makes any accidental parking observable as elapsed time.
	saveTTL := renderParkTTL
	defer func() { renderParkTTL = saveTTL }()
	renderParkTTL = 10 * time.Second

	runtime.GC()
	before := runtime.NumGoroutine()

	start := time.Now()
	for i := 0; i < 20; i++ {
		rec := httptest.NewRecorder()
		p.ServeHTTP(rec, pageLoadRequest("/app/demo/"))
		if rec.Code != http.StatusServiceUnavailable {
			t.Fatalf("request %d: status %d, want 503", i, rec.Code)
		}
	}
	elapsed := time.Since(start)

	if elapsed > 2*time.Second {
		t.Fatalf("20 gated page loads took %v; the gate is parking, not answering", elapsed)
	}
	if n := hits.Load(); n != 0 {
		t.Fatalf("backend saw %d requests; gated loads must never reach the app", n)
	}
	if n := spawns.Load(); n != 0 {
		t.Fatalf("gate requested %d worker spawns; a deferred page load must not grow the pool", n)
	}

	p.mu.RLock()
	pool := p.pools["demo"]
	var active int64
	for _, rb := range pool.replicas {
		if rb != nil {
			active += rb.activeConns.Load()
		}
	}
	p.mu.RUnlock()
	if active != 0 {
		t.Fatalf("replica activeConns = %d after gated loads; a deferred load must bind no slot", active)
	}

	// Goroutines settle asynchronously; allow a brief window before concluding
	// that the gate leaked one.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if runtime.NumGoroutine() <= before+2 {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("goroutines %d -> %d after 20 gated page loads; the gate is holding goroutines",
		before, runtime.NumGoroutine())
}

func TestServeHTTP_GateDoesNotPreemptUnknownSlug(t *testing.T) {
	// Precedence: the gate runs last. An app that does not exist is not "busy",
	// and answering a 404 with a wait page would loop the client forever.
	p := New()
	p.SetAppLimiter("ghost", emptyLimiter())
	p.SetSlugExists(func(string) (bool, error) { return false, nil })

	rec := httptest.NewRecorder()
	p.ServeHTTP(rec, pageLoadRequest("/app/ghost/"))

	if rec.Code == http.StatusServiceUnavailable && strings.Contains(rec.Body.String(), WaitingPageSentinel) {
		t.Fatal("an unknown slug must 404, not report the app as at capacity")
	}
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
}

func TestServeHTTP_GateDoesNotPreemptColdStart(t *testing.T) {
	// Precedence: an app with no live replica is starting, not busy. Telling the
	// user it is at capacity would send them to shed load that would not help,
	// and it would skip the wake this path fires.
	p := New()
	p.SetAppLimiter("demo", emptyLimiter())
	p.SetSlugExists(func(string) (bool, error) { return true, nil })
	woke := make(chan string, 1)
	p.SetWakeTrigger(func(slug string) { woke <- slug })
	p.SetWakeHoldTimeout(10 * time.Millisecond)

	rec := httptest.NewRecorder()
	p.ServeHTTP(rec, pageLoadRequest("/app/demo/"))

	if !strings.Contains(rec.Body.String(), LoadingPageSentinel) {
		t.Fatalf("a cold app must get the starting page, got %.120q", rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), WaitingPageSentinel) {
		t.Fatal("a cold app must not be reported as at capacity")
	}
	select {
	case <-woke:
	case <-time.After(2 * time.Second):
		t.Fatal("the wake never fired; the gate swallowed the cold-start path")
	}
}

func TestServeHTTP_GateDoesNotPreemptDeploying(t *testing.T) {
	// Precedence: a deploy tears the pool down before the new one boots. That is
	// not a capacity shortfall, and the deploying page is the one with no give-up
	// countdown, which a multi-minute build needs.
	p := New()
	p.SetAppLimiter("demo", emptyLimiter())
	p.SetSlugExists(func(string) (bool, error) { return true, nil })
	p.SetAppStatusLookup(func(string) (string, string) { return "deploying", "" })
	p.SetWakeHoldTimeout(10 * time.Millisecond)

	rec := httptest.NewRecorder()
	p.ServeHTTP(rec, pageLoadRequest("/app/demo/"))

	if !strings.Contains(rec.Body.String(), DeployingPageSentinel) {
		t.Fatalf("a deploying app must get the deploying page, got %.120q", rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), WaitingPageSentinel) {
		t.Fatal("a deploying app must not be reported as at capacity")
	}
}

func TestWaitPages_AreNotStored(t *testing.T) {
	// Every auto-refreshing wait page must be uncacheable. The starting and
	// deploying pages are served with 200, which RFC 9111 makes heuristically
	// cacheable: without no-store a shared cache may replay "Starting app…" long
	// after the app started, and the page's own refresh then loads the cached
	// copy forever.
	cases := []struct {
		name    string
		setup   func(p *Proxy)
		request func() *http.Request
		want    string
	}{
		{
			name: "starting",
			setup: func(p *Proxy) {
				p.SetSlugExists(func(string) (bool, error) { return true, nil })
				p.SetWakeHoldTimeout(10 * time.Millisecond)
			},
			request: func() *http.Request { return pageLoadRequest("/app/demo/") },
			want:    LoadingPageSentinel,
		},
		{
			name: "deploying",
			setup: func(p *Proxy) {
				p.SetSlugExists(func(string) (bool, error) { return true, nil })
				p.SetAppStatusLookup(func(string) (string, string) { return "deploying", "" })
				p.SetWakeHoldTimeout(10 * time.Millisecond)
			},
			request: func() *http.Request { return pageLoadRequest("/app/demo/") },
			want:    DeployingPageSentinel,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p := New()
			c.setup(p)
			rec := httptest.NewRecorder()
			p.ServeHTTP(rec, c.request())
			if !strings.Contains(rec.Body.String(), c.want) {
				t.Fatalf("precondition: body is not the %s page: %.120q", c.name, rec.Body.String())
			}
			if got := rec.Header().Get("Cache-Control"); got != "no-store" {
				t.Fatalf("%s page Cache-Control = %q, want no-store", c.name, got)
			}
		})
	}
}

// TestWaitingPage_DoesNotBlameTheDeploy pins the copy that separates the two
// waits a user can land on. The behaviour of the capacity page - its budget,
// its jitter, and what it leaves in sessionStorage - is covered by
// internal/ui/jstests/wait-page-scripts.test.js, which runs the real script;
// what is left here is the thing that is a property of the PAGE rather than of
// the script, and that no JS test can see: the two pages must not borrow each
// other's diagnosis.
func TestWaitingPage_DoesNotBlameTheDeploy(t *testing.T) {
	// Telling a user their app failed to deploy when it is running and merely
	// busy sends them to debug a deployment that is fine.
	if strings.Contains(waitingPage, "App did not start") {
		t.Error("the capacity page must not blame a failed deploy for a running, busy app")
	}
	if !strings.Contains(waitingPage, "Still at capacity") {
		t.Error("the capacity page's give-up state must name capacity")
	}
	if strings.Contains(loadingPage, "Still at capacity") {
		t.Error("the starting page must not report capacity for an app that has not come up")
	}
}

func TestRenderDeferred_IsDistinctFromRenderPaced(t *testing.T) {
	// One paced visit re-polls every ~1.75 s, so folding deferred page loads into
	// render-paced would drown the count of sessions actually refused.
	if ReasonRenderDeferred == ReasonRenderPaced {
		t.Fatal("deferred page loads and refused sessions must be countable apart")
	}
	if ReasonRenderDeferred != "render-deferred" {
		t.Fatalf("ReasonRenderDeferred = %q, want render-deferred", ReasonRenderDeferred)
	}
}

func TestServeHTTP_GateRecordsRejectForOperators(t *testing.T) {
	// A gate nobody can see is a gate nobody can tune. The deferral must reach
	// the per-app rollup the API and dashboard read.
	p, _ := registerGatedApp(t, "demo")
	p.SetAppLimiter("demo", emptyLimiter())

	rec := httptest.NewRecorder()
	p.ServeHTTP(rec, pageLoadRequest("/app/demo/"))

	counts := p.RejectsByReason("demo", time.Minute)
	if counts[ReasonRenderDeferred] != 1 {
		t.Fatalf("render-deferred count = %d, want 1 (counts: %v)",
			counts[ReasonRenderDeferred], counts)
	}
}
