package proxy

import (
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/rvben/shinyhub/internal/admission"
	"github.com/rvben/shinyhub/internal/auth"
)

func TestRenderPrincipal_PublicKeysByIP(t *testing.T) {
	p := New()
	p.SetAppAccessLookup(func(slug string) string { return "public" })
	r := httptest.NewRequest("GET", "/app/demo/", nil)
	r.RemoteAddr = "203.0.113.7:5555"
	// A public app keys on the trusted client IP, regardless of any user.
	got := p.renderPrincipal(r, "demo")
	if got != "ip:203.0.113.7" {
		t.Fatalf("public principal = %q, want ip:203.0.113.7", got)
	}
}

func TestRenderPrincipal_PublicIgnoresUser(t *testing.T) {
	p := New()
	p.SetAppAccessLookup(func(slug string) string { return "public" })
	r := httptest.NewRequest("GET", "/app/demo/", nil)
	r.RemoteAddr = "203.0.113.7:5555"
	r = r.WithContext(auth.WithUser(r.Context(), &auth.ContextUser{ID: 42}))
	// Even with an authenticated user, a public app keys on IP so the client
	// cannot opt into a second bucket by presenting or withholding a token.
	if got := p.renderPrincipal(r, "demo"); got != "ip:203.0.113.7" {
		t.Fatalf("public+user principal = %q, want ip:203.0.113.7", got)
	}
}

func TestRenderPrincipal_PrivateKeysByUser(t *testing.T) {
	p := New()
	p.SetAppAccessLookup(func(slug string) string { return "private" })
	r := httptest.NewRequest("GET", "/app/demo/", nil)
	r.RemoteAddr = "203.0.113.7:5555"
	r = r.WithContext(auth.WithUser(r.Context(), &auth.ContextUser{ID: 42}))
	if got := p.renderPrincipal(r, "demo"); got != "u:42" {
		t.Fatalf("private principal = %q, want u:42", got)
	}
}

func TestRenderPrincipal_UnwiredLookupUsesBothGates(t *testing.T) {
	p := New() // no SetAppAccessLookup: conservative both-gates keying
	r := httptest.NewRequest("GET", "/app/demo/", nil)
	r.RemoteAddr = "203.0.113.7:5555"
	r = r.WithContext(auth.WithUser(r.Context(), &auth.ContextUser{ID: 42}))
	if got := p.renderPrincipal(r, "demo"); got != "ip:203.0.113.7|u:42" {
		t.Fatalf("unwired principal = %q, want ip:203.0.113.7|u:42", got)
	}
}

func TestParkBudget_PerAppAndGlobalCeilings(t *testing.T) {
	b := newParkBudget(2, 3) // 2 per app, 3 global
	// Two parks for app-a succeed, the third fails the per-app ceiling.
	if !b.acquire("app-a") || !b.acquire("app-a") {
		t.Fatal("first two app-a parks should succeed")
	}
	if b.acquire("app-a") {
		t.Fatal("third app-a park should fail the per-app ceiling of 2")
	}
	// app-b can still park (its own per-app count is 0), up to the global 3.
	if !b.acquire("app-b") {
		t.Fatal("app-b first park should succeed under the global ceiling")
	}
	if b.acquire("app-b") {
		t.Fatal("app-b second park should fail: global ceiling of 3 reached")
	}
	// Releasing an app-a slot frees room under both ceilings.
	b.release("app-a")
	if !b.acquire("app-b") {
		t.Fatal("after releasing app-a, app-b should park under the freed global slot")
	}
}

func TestParkBudget_UnlimitedWhenNonPositive(t *testing.T) {
	b := newParkBudget(0, 0) // both unlimited
	for i := 0; i < 1000; i++ {
		if !b.acquire("x") {
			t.Fatalf("unlimited budget refused acquire %d", i)
		}
	}
}

func TestParkBudget_ConcurrentNeverExceedsGlobal(t *testing.T) {
	b := newParkBudget(0, 50) // per-app unlimited, global 50
	var granted int64
	var mu sync.Mutex
	var wg sync.WaitGroup
	for i := 0; i < 500; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if b.acquire("app") {
				mu.Lock()
				granted++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if granted != 50 {
		t.Fatalf("concurrent acquires granted %d, want exactly 50 (global ceiling)", granted)
	}
}

// renderWSUpgradeRequest builds a bare WebSocket upgrade request for the
// charge-admission tests. Named distinctly from elastic_ws_park_test.go's
// wsUpgradeRequest (which also carries cookies and a fixed path), since both
// live in the same test package.
func renderWSUpgradeRequest(target string) *http.Request {
	r := httptest.NewRequest("GET", target, nil)
	r.Header.Set("Upgrade", "websocket")
	r.Header.Set("Connection", "Upgrade")
	return r
}

func TestChargeRenderAdmission_NonWSForwards(t *testing.T) {
	p := New()
	p.SetAppLimiter("demo", admission.NewAppLimiter(1, 1, 20, 3, 4096))
	r := httptest.NewRequest("GET", "/app/demo/", nil) // not a WS upgrade
	rec := httptest.NewRecorder()
	if !p.chargeRenderAdmission(rec, r, "demo") {
		t.Fatal("a non-WebSocket request must forward without charging")
	}
}

func TestChargeRenderAdmission_DisabledForwards(t *testing.T) {
	p := New() // no limiter for the slug: pacing disabled
	r := renderWSUpgradeRequest("/app/demo/")
	rec := httptest.NewRecorder()
	if !p.chargeRenderAdmission(rec, r, "demo") {
		t.Fatal("with no limiter (pacing disabled), a WS upgrade must forward")
	}
}

func TestChargeRenderAdmission_ChargesAndSheds(t *testing.T) {
	p := New()
	// Shared burst of 1, rate 0 so no refill; per-app divisor high so the shared
	// bucket is what binds. One upgrade is admitted, the next is shed.
	p.SetAppLimiter("demo", admission.NewAppLimiter(0, 1, 1, 5, 4096))
	p.SetRenderParkBudget(0, 0) // unlimited budget; TTL is what ends the park
	saveTTL, saveInterval := renderParkTTL, renderParkInterval
	defer func() { renderParkTTL, renderParkInterval = saveTTL, saveInterval }()
	renderParkTTL = 20 * time.Millisecond
	renderParkInterval = 5 * time.Millisecond

	r1 := renderWSUpgradeRequest("/app/demo/")
	r1.RemoteAddr = "203.0.113.1:1"
	if !p.chargeRenderAdmission(httptest.NewRecorder(), r1, "demo") {
		t.Fatal("first upgrade should be admitted (burst 1)")
	}
	// Second upgrade from a DIFFERENT principal: its own bucket is fine, but the
	// shared bucket is empty and never refills, so after the park TTL it sheds.
	r2 := renderWSUpgradeRequest("/app/demo/")
	r2.RemoteAddr = "203.0.113.2:2"
	rec := httptest.NewRecorder()
	if p.chargeRenderAdmission(rec, r2, "demo") {
		t.Fatal("second upgrade should be shed: shared bucket empty, no refill")
	}
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("shed status = %d, want 503", rec.Code)
	}
	if got := rec.Header().Get("X-Shinyhub-Reject"); got != string(ReasonRenderPaced) {
		t.Fatalf("reject reason = %q, want render-paced", got)
	}
}

func TestChargeRenderAdmission_WatermarkSheds(t *testing.T) {
	p := New()
	p.SetAppLimiter("demo", admission.NewAppLimiter(1000, 1000, 20, 3, 4096)) // limiter never binds
	// A watermark that is always over threshold sheds with cpu-saturation.
	wm := admission.NewWatermark(90, func() (float64, error) { return 0, nil })
	wm.SetReadingForTest(95) // test hook: records a fresh 95% reading
	p.SetCPUWatermark(wm)
	saveTTL, saveInterval := renderParkTTL, renderParkInterval
	defer func() { renderParkTTL, renderParkInterval = saveTTL, saveInterval }()
	renderParkTTL = 20 * time.Millisecond
	renderParkInterval = 5 * time.Millisecond
	r := renderWSUpgradeRequest("/app/demo/")
	rec := httptest.NewRecorder()
	if p.chargeRenderAdmission(rec, r, "demo") {
		t.Fatal("upgrade should be shed by the CPU watermark")
	}
	if got := rec.Header().Get("X-Shinyhub-Reject"); got != string(ReasonCPUSaturation) {
		t.Fatalf("reject reason = %q, want cpu-saturation", got)
	}
}

func TestSetRenderParkTTL(t *testing.T) {
	orig := renderParkTTL
	defer func() { renderParkTTL = orig }()
	p := New()
	p.SetRenderParkTTL(7 * time.Second)
	if renderParkTTL != 7*time.Second {
		t.Fatalf("renderParkTTL = %v, want 7s", renderParkTTL)
	}
}

func TestChargeRenderAdmission_WatermarkBlockSpendsNoToken(t *testing.T) {
	// A request shed purely by the CPU watermark must not consume the app's scarce
	// shared token; once the watermark clears, a later request still gets it. This
	// pins the watermark-before-limiter ordering: a limiter-first charge would burn
	// the token during the first request's park retries.
	p := New()
	p.SetAppLimiter("demo", admission.NewAppLimiter(0, 1, 1, 3, 4096)) // one shared token, no refill
	wm := admission.NewWatermark(90, func() (float64, error) { return 0, nil })
	wm.SetReadingForTest(95) // saturated: blocks admission
	p.SetCPUWatermark(wm)
	saveTTL, saveInterval := renderParkTTL, renderParkInterval
	defer func() { renderParkTTL, renderParkInterval = saveTTL, saveInterval }()
	renderParkTTL = 20 * time.Millisecond
	renderParkInterval = 5 * time.Millisecond

	// First upgrade: the watermark blocks it for the whole TTL, then it sheds with
	// cpu-saturation. It must NOT have spent the single shared token.
	rec1 := httptest.NewRecorder()
	if p.chargeRenderAdmission(rec1, renderWSUpgradeRequest("/app/demo/"), "demo") {
		t.Fatal("first upgrade should be shed by the saturated watermark")
	}
	if got := rec1.Header().Get("X-Shinyhub-Reject"); got != string(ReasonCPUSaturation) {
		t.Fatalf("first shed reason = %q, want cpu-saturation", got)
	}
	// Clear the watermark; the untouched shared token is still available.
	wm.SetReadingForTest(10)
	rec2 := httptest.NewRecorder()
	if !p.chargeRenderAdmission(rec2, renderWSUpgradeRequest("/app/demo/"), "demo") {
		t.Fatalf("second upgrade should get the untouched shared token; got shed %q",
			rec2.Header().Get("X-Shinyhub-Reject"))
	}
}

func TestChargeRenderAdmission_ParkWaitsForSharedNotPrincipal(t *testing.T) {
	// End-to-end proof through the real park loop: a request parked on a shared
	// capacity dip is admitted once shared capacity returns, WITHOUT its principal
	// bucket being drained by the park. If the park re-ran the full Admit each tick
	// (the pre-fix bug), the principal's small burst would empty within a couple of
	// ticks and the request would be shed before shared capacity recovered.
	p := New()
	// Shared burst 1, refilling one token about every 50ms (20/s). Principal burst 2
	// with a negligible refill (divisor 1000), so a drained principal stays drained
	// for the whole test: only re-checking the SHARED bucket (the fix) can admit here.
	p.SetAppLimiter("demo", admission.NewAppLimiter(20, 1, 1000, 2, 4096))
	p.SetRenderParkBudget(0, 0)
	saveTTL, saveInterval := renderParkTTL, renderParkInterval
	defer func() { renderParkTTL, renderParkInterval = saveTTL, saveInterval }()
	renderParkTTL = 500 * time.Millisecond
	renderParkInterval = 5 * time.Millisecond

	// A hog drains the single shared token, so the victim must park.
	hog := renderWSUpgradeRequest("/app/demo/")
	hog.RemoteAddr = "203.0.113.9:9"
	if !p.chargeRenderAdmission(httptest.NewRecorder(), hog, "demo") {
		t.Fatal("hog should take the shared token")
	}
	// The victim parks (shared empty) and must be admitted once shared refills, well
	// within the TTL. A pre-fix park (re-Admit per tick) would instead drain the
	// victim's 2-token principal burst in a couple of ticks and shed it first.
	victim := renderWSUpgradeRequest("/app/demo/")
	victim.RemoteAddr = "203.0.113.10:10"
	rec := httptest.NewRecorder()
	start := time.Now()
	if !p.chargeRenderAdmission(rec, victim, "demo") {
		t.Fatalf("victim should be admitted after shared capacity returns; got shed %d %q after %v",
			rec.Code, rec.Header().Get("X-Shinyhub-Reject"), time.Since(start))
	}
}

func TestChargeRenderAdmission_NonWSNeverCharges(t *testing.T) {
	// Non-WebSocket requests must never be charged. With a single shared token and
	// no refill, a charged second request would shed; all must forward instead.
	p := New()
	p.SetAppLimiter("demo", admission.NewAppLimiter(0, 1, 1, 3, 4096))
	for i := 0; i < 5; i++ {
		r := httptest.NewRequest("GET", "/app/demo/", nil) // not a WS upgrade
		rec := httptest.NewRecorder()
		if !p.chargeRenderAdmission(rec, r, "demo") {
			t.Fatalf("non-WS request %d must forward uncharged; got shed %d", i, rec.Code)
		}
	}
}

func TestChargeRenderAdmission_FullBudgetShedsImmediately(t *testing.T) {
	// When the park budget is full, a would-be parker sheds immediately rather than
	// waiting out the TTL. A long TTL makes the immediacy observable.
	p := New()
	p.SetAppLimiter("demo", admission.NewAppLimiter(0, 1, 1, 5, 4096)) // one shared token, then empty
	p.SetRenderParkBudget(1, 1)                                        // a single park slot
	saveTTL := renderParkTTL
	defer func() { renderParkTTL = saveTTL }()
	renderParkTTL = 10 * time.Second // long: proves the shed is budget-driven, not TTL-driven

	// Take the one shared token so any later upgrade must park.
	r0 := renderWSUpgradeRequest("/app/demo/")
	r0.RemoteAddr = "203.0.113.1:1"
	if !p.chargeRenderAdmission(httptest.NewRecorder(), r0, "demo") {
		t.Fatal("first upgrade should take the shared token")
	}
	// Pre-occupy the single park slot, as a concurrent parker would.
	budget := p.renderPark.Load()
	if budget == nil || !budget.acquire("demo") {
		t.Fatal("precondition: could not occupy the sole park slot")
	}
	defer budget.release("demo")

	// A second upgrade (different principal) is within its share but finds the
	// shared bucket empty and the park budget full: it must shed at once.
	r := renderWSUpgradeRequest("/app/demo/")
	r.RemoteAddr = "203.0.113.2:2"
	rec := httptest.NewRecorder()
	start := time.Now()
	if p.chargeRenderAdmission(rec, r, "demo") {
		t.Fatal("second upgrade should shed: park budget full")
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("shed took %v; a full-budget shed must be immediate, not TTL-driven", elapsed)
	}
	if got := rec.Header().Get("X-Shinyhub-Reject"); got != string(ReasonRenderPaced) {
		t.Fatalf("shed reason = %q, want render-paced", got)
	}
}
