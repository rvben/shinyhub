package proxy

import (
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rvben/shinyhub/internal/config"
)

// coldGet fires one cookie-less request for slug and returns the recorder.
func coldGet(p *Proxy, slug string) *httptest.ResponseRecorder {
	req := httptest.NewRequest("GET", "/app/"+slug+"/", nil)
	rec := httptest.NewRecorder()
	p.ServeHTTP(rec, req)
	return rec
}

// assertSplashPinned fails unless rec carries the loading page and a rep pin
// cookie for the given slot.
func assertSplashPinned(t *testing.T, rec *httptest.ResponseRecorder, slug string, slot int, label string) {
	t.Helper()
	if rec.Code != http.StatusOK {
		t.Fatalf("%s: want 200 loading page, got %d: %q", label, rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), LoadingPageSentinel) {
		t.Fatalf("%s: expected loading page body, got %q", label, rec.Body.String())
	}
	rep := findCookie(extractCookies(rec), cookiePrefix+slug)
	if rep == nil {
		t.Fatalf("%s: rep pin cookie not set", label)
	}
	if want := strconv.Itoa(slot) + "."; !strings.HasPrefix(rep.Value, want) {
		t.Fatalf("%s: rep cookie %q does not pin slot %d", label, rep.Value, slot)
	}
}

// TestElasticRouting_ColdBurstPacksBootingWorkers is the capacity-ramp P1
// regression: a burst of cold clients within the max_workers*grouped_size
// ceiling must all receive the loading page, packed onto booting workers up
// to grouped_size, spawning exactly ceil(clients/grouped_size) workers. No
// client may be shed while configured capacity remains.
func TestElasticRouting_ColdBurstPacksBootingWorkers(t *testing.T) {
	const (
		slug        = "burstapp"
		groupedSize = 4
		maxWorkers  = 5
		clients     = groupedSize * maxWorkers // exactly the documented ceiling
	)

	spawnCh := make(chan int, clients)
	p := New()
	p.SetPoolMode(slug, config.IsolationGrouped, groupedSize, maxWorkers)
	p.SetSpawnFunc(func(_ string, slotID int) { spawnCh <- slotID })

	var (
		wg       sync.WaitGroup
		splashes atomic.Int32
		rejects  atomic.Int32
		others   atomic.Int32
	)
	wg.Add(clients)
	for i := 0; i < clients; i++ {
		go func() {
			defer wg.Done()
			rec := coldGet(p, slug)
			switch {
			case rec.Code == http.StatusOK && strings.Contains(rec.Body.String(), LoadingPageSentinel):
				splashes.Add(1)
			case rec.Code == http.StatusServiceUnavailable:
				rejects.Add(1)
			default:
				others.Add(1)
			}
		}()
	}
	wg.Wait()

	if got := rejects.Load(); got != 0 {
		t.Errorf("cold burst within capacity shed %d/%d clients with 503; want 0", got, clients)
	}
	if got := others.Load(); got != 0 {
		t.Errorf("%d clients got a response that is neither loading page nor 503", got)
	}
	if got := splashes.Load(); got != clients {
		t.Errorf("splashes = %d, want %d", got, clients)
	}

	// Placement must be exact: ceil(clients/groupedSize) workers, each within
	// cap, every client bound. The write-locked placement serializes bursts,
	// so the worker count is deterministic, not merely bounded.
	p.mu.RLock()
	pool := p.pools[slug]
	workers := len(pool.workers)
	total := 0
	for _, w := range pool.workers {
		if w.assignedClients > groupedSize {
			t.Errorf("slot %d has %d assigned clients, above cap %d", w.slotID, w.assignedClients, groupedSize)
		}
		total += w.assignedClients
	}
	bound := len(p.clients[slug])
	p.mu.RUnlock()

	if workers != maxWorkers {
		t.Errorf("workers reserved = %d, want %d (ceil(%d/%d))", workers, maxWorkers, clients, groupedSize)
	}
	if total != clients {
		t.Errorf("sum of assignedClients = %d, want %d", total, clients)
	}
	if bound != clients {
		t.Errorf("bound clients = %d, want %d", bound, clients)
	}

	// Exactly one spawn per reserved worker, all slots distinct.
	seen := make(map[int]bool)
	for i := 0; i < maxWorkers; i++ {
		select {
		case slot := <-spawnCh:
			if seen[slot] {
				t.Errorf("slot %d spawned more than once", slot)
			}
			seen[slot] = true
		case <-time.After(2 * time.Second):
			t.Fatalf("only %d spawn calls observed, want %d", i, maxWorkers)
		}
	}
	time.Sleep(50 * time.Millisecond)
	select {
	case slot := <-spawnCh:
		t.Errorf("unexpected extra spawn for slot %d", slot)
	default:
	}

	// The ceiling still holds: one more fresh client is genuinely at capacity
	// and must be shed with Retry-After.
	rec := coldGet(p, slug)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("client beyond the ceiling: want 503, got %d", rec.Code)
	}
	if rec.Header().Get("Retry-After") == "" {
		t.Error("expected Retry-After on the at-capacity 503")
	}
	if got := rec.Header().Get("X-Shinyhub-Reject"); got != string(ReasonPoolSaturated) {
		t.Errorf("X-Shinyhub-Reject = %q, want %q", got, ReasonPoolSaturated)
	}
}

// TestElasticRouting_GroupedBindsToBootingWorker pins the sequential shape of
// the same fix: while slot 0 is still booting, subsequent fresh clients bind
// onto it (loading page, pinned to slot 0, no extra spawn) until grouped_size
// is reached; only then is a second worker allocated.
func TestElasticRouting_GroupedBindsToBootingWorker(t *testing.T) {
	const (
		slug        = "packapp"
		groupedSize = 3
	)

	spawnCh := make(chan int, 4)
	p := New()
	p.SetPoolMode(slug, config.IsolationGrouped, groupedSize, 2)
	p.SetSpawnFunc(func(_ string, slotID int) { spawnCh <- slotID })

	// Client 1 allocates slot 0.
	assertSplashPinned(t, coldGet(p, slug), slug, 0, "client 1")
	select {
	case slot := <-spawnCh:
		if slot != 0 {
			t.Fatalf("first spawn slot = %d, want 0", slot)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("spawn for slot 0 not invoked")
	}

	// Clients 2 and 3 bind onto the still-booting slot 0.
	assertSplashPinned(t, coldGet(p, slug), slug, 0, "client 2")
	assertSplashPinned(t, coldGet(p, slug), slug, 0, "client 3")

	p.mu.RLock()
	w := p.pools[slug].workers[0]
	ac := 0
	if w != nil {
		ac = w.assignedClients
	}
	nWorkers := len(p.pools[slug].workers)
	p.mu.RUnlock()
	if ac != groupedSize {
		t.Errorf("slot 0 assignedClients = %d, want %d", ac, groupedSize)
	}
	if nWorkers != 1 {
		t.Errorf("workers = %d, want 1 (no allocation while slot 0 has capacity)", nWorkers)
	}
	select {
	case slot := <-spawnCh:
		t.Fatalf("unexpected spawn for slot %d while slot 0 had capacity", slot)
	case <-time.After(50 * time.Millisecond):
	}

	// Client 4 finds slot 0 at cap and allocates slot 1.
	assertSplashPinned(t, coldGet(p, slug), slug, 1, "client 4")
	select {
	case slot := <-spawnCh:
		if slot != 1 {
			t.Fatalf("second spawn slot = %d, want 1", slot)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("spawn for slot 1 not invoked")
	}
}

// TestElasticRouting_SpawnFailureReleasesMultiBoundClients verifies the boot
// failure path with several clients bound to one booting slot: releasing the
// reservation must clean every binding, and a returning client (stale pin)
// must be re-placed on a fresh slot, triggering a new spawn.
func TestElasticRouting_SpawnFailureReleasesMultiBoundClients(t *testing.T) {
	const (
		slug        = "failapp"
		groupedSize = 3
	)

	spawnCh := make(chan int, 4)
	p := New()
	p.SetPoolMode(slug, config.IsolationGrouped, groupedSize, 2)
	p.SetSpawnFunc(func(_ string, slotID int) { spawnCh <- slotID })

	rec1 := coldGet(p, slug)
	assertSplashPinned(t, rec1, slug, 0, "client 1")
	client1Cookies := extractCookies(rec1)
	assertSplashPinned(t, coldGet(p, slug), slug, 0, "client 2")
	assertSplashPinned(t, coldGet(p, slug), slug, 0, "client 3")

	select {
	case <-spawnCh:
	case <-time.After(2 * time.Second):
		t.Fatal("spawn for slot 0 not invoked")
	}

	p.mu.RLock()
	preBound := len(p.clients[slug])
	p.mu.RUnlock()
	if preBound != 3 {
		t.Fatalf("precondition: bound clients = %d, want 3", preBound)
	}

	// The worker fails to boot; the spawner releases the reservation.
	p.ReleaseReservation(slug, 0)

	p.mu.RLock()
	postBound := len(p.clients[slug])
	nWorkers := len(p.pools[slug].workers)
	p.mu.RUnlock()
	if postBound != 0 {
		t.Errorf("bound clients after release = %d, want 0", postBound)
	}
	if nWorkers != 0 {
		t.Errorf("workers after release = %d, want 0", nWorkers)
	}

	// Client 1 retries with its (now stale) cookies: it must be re-placed on
	// a fresh slot and a new spawn dispatched, not left dangling.
	req := httptest.NewRequest("GET", "/app/"+slug+"/", nil)
	req.Header.Set("Cookie", cookieHeader(client1Cookies))
	rec := httptest.NewRecorder()
	p.ServeHTTP(rec, req)
	assertSplashPinned(t, rec, slug, 1, "client 1 retry")
	select {
	case slot := <-spawnCh:
		if slot != 1 {
			t.Fatalf("respawn slot = %d, want 1", slot)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("respawn not invoked after stale-pin retry")
	}
}

// TestPlaceClient_RebindFromDrainingWorkerMigratesCount verifies that a
// client whose pinned worker started draining is re-placed on a fresh worker
// with its accounting migrated: the draining worker's assignedClients is
// decremented, not leaked (a leak would keep the worker's count above zero
// forever and block its terminate-on-idle).
func TestPlaceClient_RebindFromDrainingWorkerMigratesCount(t *testing.T) {
	const slug = "drainapp"

	p := New()
	p.SetPoolMode(slug, config.IsolationGrouped, 2, 2)
	p.SetSpawnFunc(func(string, int) {})

	rec1 := coldGet(p, slug)
	assertSplashPinned(t, rec1, slug, 0, "initial bind")
	cookies := extractCookies(rec1)

	p.mu.Lock()
	p.pools[slug].workers[0].status = workerDraining
	p.mu.Unlock()

	// The pinned client returns; a draining worker takes no routing, so it is
	// re-placed on a freshly allocated slot.
	req := httptest.NewRequest("GET", "/app/"+slug+"/", nil)
	req.Header.Set("Cookie", cookieHeader(cookies))
	rec2 := httptest.NewRecorder()
	p.ServeHTTP(rec2, req)
	assertSplashPinned(t, rec2, slug, 1, "rebind after drain")

	p.mu.RLock()
	oldAC := p.pools[slug].workers[0].assignedClients
	newAC := p.pools[slug].workers[1].assignedClients
	p.mu.RUnlock()
	if oldAC != 0 {
		t.Errorf("draining worker assignedClients = %d, want 0 (count must migrate)", oldAC)
	}
	if newAC != 1 {
		t.Errorf("new worker assignedClients = %d, want 1", newAC)
	}
}

// TestMemoryGuard_GroupedBindToBootingAllowedUnderPressure verifies the guard
// gates only NEW worker allocation: binding a fresh client onto an existing
// booting worker adds no process and must not be shed, while a client that
// genuinely needs a new worker still is.
func TestMemoryGuard_GroupedBindToBootingAllowedUnderPressure(t *testing.T) {
	const (
		slug        = "mgapp"
		groupedSize = 2
	)

	memLow := atomic.Bool{}
	spawnCh := make(chan int, 4)
	p := New()
	p.SetPoolMode(slug, config.IsolationGrouped, groupedSize, 2)
	p.SetSpawnFunc(func(_ string, slotID int) { spawnCh <- slotID })
	p.SetMemoryGuard(512, func() (int, bool) {
		if memLow.Load() {
			return 100, true
		}
		return 4096, true
	})

	// Client A allocates slot 0 while memory is fine.
	assertSplashPinned(t, coldGet(p, slug), slug, 0, "client A")

	memLow.Store(true)

	// Client B binds onto booting slot 0 (1/2): no new process, not shed.
	assertSplashPinned(t, coldGet(p, slug), slug, 0, "client B under pressure")

	// Client C would need a new worker: shed with the memory-pressure reason.
	rec := coldGet(p, slug)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("client C under pressure: want 503, got %d", rec.Code)
	}
	if got := rec.Header().Get("X-Shinyhub-Reject"); got != string(ReasonMemoryPressure) {
		t.Errorf("X-Shinyhub-Reject = %q, want %q", got, ReasonMemoryPressure)
	}

	// Exactly one spawn happened (slot 0); binding and shedding spawn nothing.
	select {
	case <-spawnCh:
	case <-time.After(2 * time.Second):
		t.Fatal("spawn for slot 0 not invoked")
	}
	select {
	case slot := <-spawnCh:
		t.Errorf("unexpected spawn for slot %d", slot)
	case <-time.After(50 * time.Millisecond):
	}
}
