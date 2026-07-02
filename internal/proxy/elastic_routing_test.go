package proxy

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rvben/shinyhub/internal/config"
)

// extractCookies returns all Set-Cookie values from a recorder as a slice.
func extractCookies(rec *httptest.ResponseRecorder) []*http.Cookie {
	resp := rec.Result()
	return resp.Cookies()
}

// cookieHeader builds a Cookie header value from a slice of *http.Cookie
// (as returned by http.Response.Cookies).
func cookieHeader(cookies []*http.Cookie) string {
	var parts []string
	for _, c := range cookies {
		parts = append(parts, c.Name+"="+c.Value)
	}
	return strings.Join(parts, "; ")
}

// findCookie returns the first cookie with the given name, or nil.
func findCookie(cookies []*http.Cookie, name string) *http.Cookie {
	for _, c := range cookies {
		if c.Name == name {
			return c
		}
	}
	return nil
}

// TestElasticRouting_FirstRequest verifies that a cookie-less request to an
// elastic (per_session, maxWorkers=1) pool:
//   - Receives the loading page (HTTP 200 with "Starting app" body).
//   - Gets a cid cookie set in the response.
//   - Gets a rep-sticky cookie pinning slot 0 in the response.
//   - Causes the spawn callback to be invoked for slug+slot 0.
func TestElasticRouting_FirstRequest(t *testing.T) {
	// Use a channel so the test goroutine can safely observe the spawn call
	// from the goroutine launched inside ServeHTTP, avoiding data races.
	type spawnCall struct {
		slug   string
		slotID int
	}
	spawnCh := make(chan spawnCall, 4)

	p := New()
	p.SetPoolMode("myapp", config.IsolationPerSession, 0, 1)
	p.SetSpawnFunc(func(slug string, slotID int) {
		spawnCh <- spawnCall{slug, slotID}
	})

	req := httptest.NewRequest("GET", "/app/myapp/", nil)
	rec := httptest.NewRecorder()
	p.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("want 200 (loading page), got %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "Starting app") {
		t.Error("expected loading page body")
	}

	cookies := extractCookies(rec)
	cidName := clientCookiePrefix + "myapp"
	repName := cookiePrefix + "myapp"

	cidCookie := findCookie(cookies, cidName)
	if cidCookie == nil {
		t.Fatal("cid cookie not set in response")
	}
	if cidCookie.Value == "" {
		t.Error("cid cookie value is empty")
	}

	repCookie := findCookie(cookies, repName)
	if repCookie == nil {
		t.Fatal("rep sticky cookie not set in response")
	}
	// In unsigned mode the value is "<slotID>.<deploymentID>".
	// Slot 0 with deploymentID 0 -> "0.0".
	if !strings.HasPrefix(repCookie.Value, "0.") {
		t.Errorf("rep cookie %q does not pin slot 0", repCookie.Value)
	}

	// Wait for the spawn goroutine to deliver its call (channel avoids the race).
	select {
	case call := <-spawnCh:
		if call.slug != "myapp" {
			t.Errorf("spawn slug = %q, want %q", call.slug, "myapp")
		}
		if call.slotID != 0 {
			t.Errorf("spawn slotID = %d, want 0", call.slotID)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("spawn callback not invoked within 2s")
	}
}

// TestElasticRouting_SecondClientWhileBooting verifies that a second cookie-less
// request (different client) arriving while slot 0 is still booting receives
// 503 with a Retry-After header, because maxWorkers=1 is already consumed.
func TestElasticRouting_SecondClientWhileBooting(t *testing.T) {
	p := New()
	p.SetPoolMode("myapp", config.IsolationPerSession, 0, 1)
	p.SetSpawnFunc(func(string, int) {}) // no-op so we don't need a real spawner

	// First client reserves slot 0.
	req1 := httptest.NewRequest("GET", "/app/myapp/", nil)
	rec1 := httptest.NewRecorder()
	p.ServeHTTP(rec1, req1)
	if rec1.Code != http.StatusOK {
		t.Fatalf("first request: want 200, got %d", rec1.Code)
	}

	// Second client (no cid/pin cookies -> fresh client) arrives while slot 0 is booting.
	req2 := httptest.NewRequest("GET", "/app/myapp/", nil)
	rec2 := httptest.NewRecorder()
	p.ServeHTTP(rec2, req2)

	if rec2.Code != http.StatusServiceUnavailable {
		t.Fatalf("second request: want 503, got %d", rec2.Code)
	}
	if ra := rec2.Header().Get("Retry-After"); ra == "" {
		t.Error("expected Retry-After header on 503")
	}
	if !strings.Contains(rec2.Body.String(), MsgPoolSaturated) {
		t.Errorf("expected %q in 503 body, got %q", MsgPoolSaturated, rec2.Body.String())
	}
}

// TestElasticRouting_ForwardsAfterWorkerReady verifies that once RegisterElasticWorker
// installs a running reverse proxy for slot 0, a request from the same client
// carrying the rep pin and cid cookie is forwarded to that worker.
func TestElasticRouting_ForwardsAfterWorkerReady(t *testing.T) {
	// Backend server that records received requests.
	backendHit := make(chan struct{}, 1)
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		backendHit <- struct{}{}
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("from backend")) //nolint:errcheck
	}))
	defer backend.Close()

	p := New()
	p.SetPoolMode("myapp", config.IsolationPerSession, 0, 1)
	p.SetSpawnFunc(func(string, int) {}) // no-op

	// First request: reserve slot 0 and capture the cookies.
	req1 := httptest.NewRequest("GET", "/app/myapp/", nil)
	rec1 := httptest.NewRecorder()
	p.ServeHTTP(rec1, req1)
	if rec1.Code != http.StatusOK {
		t.Fatalf("first request: want 200 (loading), got %d", rec1.Code)
	}
	firstCookies := extractCookies(rec1)

	// Register the elastic worker for slot 0. Use deploymentID=0 so it is
	// consistent with the pin cookie that was set during decisionAllocate.
	if err := p.RegisterElasticWorker("myapp", 0, backend.URL, nil, 0); err != nil {
		t.Fatalf("RegisterElasticWorker: %v", err)
	}

	// Second request (same client, now with cookies) should forward to the backend.
	req2 := httptest.NewRequest("GET", "/app/myapp/", nil)
	req2.Header.Set("Cookie", cookieHeader(firstCookies))
	rec2 := httptest.NewRecorder()
	p.ServeHTTP(rec2, req2)

	if rec2.Code != http.StatusOK {
		t.Fatalf("second request: want 200 (from backend), got %d: body=%q", rec2.Code, rec2.Body.String())
	}

	select {
	case <-backendHit:
		// good
	case <-time.After(2 * time.Second):
		t.Fatal("backend was not reached within 2s")
	}
	if !strings.Contains(rec2.Body.String(), "from backend") {
		t.Errorf("expected backend body, got %q", rec2.Body.String())
	}
}

// TestElasticRouting_MultiplexUnchanged is a regression guard: a multiplex pool
// (mode unset / zero value) still routes exactly as before via pickReplicaLocked.
func TestElasticRouting_MultiplexUnchanged(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("multiplex-backend")) //nolint:errcheck
	}))
	defer backend.Close()

	p := New()
	// Register via the legacy helper (creates a size-1 multiplex pool).
	if err := p.Register("testapp", backend.URL); err != nil {
		t.Fatalf("Register: %v", err)
	}

	req := httptest.NewRequest("GET", "/app/testapp/some/path", nil)
	rec := httptest.NewRecorder()
	p.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("multiplex: want 200, got %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "multiplex-backend") {
		t.Errorf("multiplex: unexpected body %q", rec.Body.String())
	}
}

// TestElasticRouting_Deregister_TerminatesAllWorkers verifies that Deregister
// on an elastic pool with multiple running workers invokes the terminate callback
// exactly once per worker.
func TestElasticRouting_Deregister_TerminatesAllWorkers(t *testing.T) {
	var terminateCalls int32
	p := New()
	p.SetPoolMode("slugX", config.IsolationPerSession, 0, 5)
	p.SetTerminateFunc(func(slug string, slotID int) {
		atomic.AddInt32(&terminateCalls, 1)
	})

	// Manually install two running workers into the pool.
	p.mu.Lock()
	pool := p.pools["slugX"]
	pool.workers[0] = &replicaBackend{slotID: 0, status: workerRunning}
	pool.workers[1] = &replicaBackend{slotID: 1, status: workerRunning}
	p.mu.Unlock()

	p.Deregister("slugX")

	// Allow goroutines to fire.
	time.Sleep(30 * time.Millisecond)
	if n := atomic.LoadInt32(&terminateCalls); n != 2 {
		t.Errorf("terminate calls = %d, want 2", n)
	}
}

// TestElasticRouting_PinnedSlotBooting verifies that a request with a valid
// rep pin for a booting slot gets the loading page (not an error).
func TestElasticRouting_PinnedSlotBooting(t *testing.T) {
	p := New()
	p.SetPoolMode("app2", config.IsolationPerSession, 0, 2)
	p.SetSpawnFunc(func(string, int) {})

	// First request: allocate slot 0.
	req1 := httptest.NewRequest("GET", "/app/app2/", nil)
	rec1 := httptest.NewRecorder()
	p.ServeHTTP(rec1, req1)
	if rec1.Code != http.StatusOK {
		t.Fatalf("first: want 200, got %d", rec1.Code)
	}
	firstCookies := extractCookies(rec1)

	// Second request WITH the pin cookie (slot 0 still booting) -> loading page.
	req2 := httptest.NewRequest("GET", "/app/app2/", nil)
	req2.Header.Set("Cookie", cookieHeader(firstCookies))
	rec2 := httptest.NewRecorder()
	p.ServeHTTP(rec2, req2)

	if rec2.Code != http.StatusOK {
		t.Fatalf("pinned+booting: want 200 (loading), got %d: %q", rec2.Code, rec2.Body.String())
	}
	if !strings.Contains(rec2.Body.String(), "Starting app") {
		t.Errorf("pinned+booting: expected loading page body, got %q", rec2.Body.String())
	}
}
