package proxy

import (
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rvben/shinyhub/internal/config"
)

// TestElasticAccounting_ConcurrentConnCycles hammers the per-client accounting
// hot path (clientConnOpened / clientConnClosed) from many goroutines across
// distinct slugs and clients with a very short grace TTL, so grace-timer expiry
// races reconnects continuously. It is the primary -race gate for moving the
// open/close bookkeeping off the global write lock onto the shared read lock
// plus per-client cs.mu. A data race, a negative counter, or a deadlock fails it.
//
//	GOWORK=off go test -race ./internal/proxy/ -run ConcurrentConnCycles -count=5
func TestElasticAccounting_ConcurrentConnCycles(t *testing.T) {
	old := clientGraceTTL
	clientGraceTTL = 1 * time.Millisecond
	t.Cleanup(func() { clientGraceTTL = old })

	const nSlugs = 8
	const clientsPerSlug = 6
	const goroutines = 48

	p := New()
	var terminates int64
	p.SetTerminateFunc(func(string, int) { atomic.AddInt64(&terminates, 1) })

	for s := 0; s < nSlugs; s++ {
		slug := "app" + strconv.Itoa(s)
		// maxWorkers >= clientsPerSlug so each setup reserveWorker call succeeds
		// (reserveWorker allocates a fresh slot; it does not pack).
		p.SetPoolMode(slug, config.IsolationGrouped, 1000, clientsPerSlug)
		for c := 0; c < clientsPerSlug; c++ {
			cid := "c" + strconv.Itoa(c)
			slot := p.reserveWorker(slug, cid)
			if slot < 0 {
				t.Fatalf("reserveWorker %s/%s: -1", slug, cid)
			}
			p.bindClient(slug, cid, slot)
		}
	}

	stop := make(chan struct{})
	var wg sync.WaitGroup
	for g := 0; g < goroutines; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			slug := "app" + strconv.Itoa(g%nSlugs)
			cid := "c" + strconv.Itoa(g%clientsPerSlug)
			for {
				select {
				case <-stop:
					return
				default:
				}
				// A full open/close cycle: the reconnect (open) must be able to
				// cancel a grace timer armed by the previous close before the
				// timer's callback (which takes the write lock) removes the slot.
				p.clientConnOpened(slug, cid)
				p.clientConnClosed(slug, cid)
			}
		}(g)
	}

	time.Sleep(750 * time.Millisecond)
	close(stop)
	wg.Wait()

	// Quiesce: let any in-flight grace timers fire, then assert consistency under
	// the exclusive write lock (no concurrent accountants remain).
	time.Sleep(50 * time.Millisecond)
	p.mu.Lock()
	defer p.mu.Unlock()
	for s := 0; s < nSlugs; s++ {
		slug := "app" + strconv.Itoa(s)
		pool := p.pools[slug]
		// Count client slots per worker and compare to assignedClients.
		perWorker := map[int]int{}
		for _, cs := range p.clients[slug] {
			if cs.liveConns < 0 {
				t.Fatalf("%s: negative liveConns %d", slug, cs.liveConns)
			}
			perWorker[cs.slotID]++
		}
		for slotID, w := range pool.workers {
			if w.assignedClients < 0 {
				t.Fatalf("%s slot %d: negative assignedClients %d", slug, slotID, w.assignedClients)
			}
			if w.assignedClients != perWorker[slotID] {
				t.Fatalf("%s slot %d: assignedClients=%d but %d client slots reference it", slug, slotID, w.assignedClients, perWorker[slotID])
			}
		}
	}
}

// TestElasticRouting_ConcurrentServeHTTP drives the full elastic ServeHTTP hot
// path concurrently: many goroutines route requests (cid cookies reused so most
// are steady-state, some fresh so they bind) to a grouped pool with a real
// backend, while short grace timers reclaim idle clients. Exercises the
// read-lock steady-state branch, the write-lock bind branch, cs.open() under
// both, and the grace/terminate path end-to-end. Run under -race.
func TestElasticRouting_ConcurrentServeHTTP(t *testing.T) {
	old := clientGraceTTL
	clientGraceTTL = 2 * time.Millisecond
	t.Cleanup(func() { clientGraceTTL = old })

	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	}))
	defer backend.Close()

	const nSlugs = 4
	const goroutines = 48
	p := New()
	p.SetSpawnFunc(func(string, int) {}) // no allocate expected (workers pre-registered)
	p.SetTerminateFunc(func(string, int) {})

	for s := 0; s < nSlugs; s++ {
		slug := "app" + strconv.Itoa(s)
		// Grouped with a huge group size and one worker: every client packs onto
		// slot 0, so requests are either a first-time bind or a steady-state route.
		p.SetPoolMode(slug, config.IsolationGrouped, 100000, 1)
		if err := p.RegisterElasticWorker(slug, 0, backend.URL, nil, 1); err != nil {
			t.Fatalf("RegisterElasticWorker %s: %v", slug, err)
		}
	}

	stop := make(chan struct{})
	var wg sync.WaitGroup
	var panics int64
	for g := 0; g < goroutines; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			defer func() {
				if r := recover(); r != nil {
					atomic.AddInt64(&panics, 1)
					t.Errorf("ServeHTTP panicked: %v", r)
				}
			}()
			slug := "app" + strconv.Itoa(g%nSlugs)
			// A small pool of cids per goroutine: reuse drives steady-state routes;
			// the short grace TTL reclaims them between requests so they re-bind.
			cids := []string{
				"aaaaaaaaaaaaaaaaaaaaaaaaaaaa" + strconv.Itoa(1000+g),
				"bbbbbbbbbbbbbbbbbbbbbbbbbbbb" + strconv.Itoa(1000+g),
			}
			for i := 0; ; i++ {
				select {
				case <-stop:
					return
				default:
				}
				cid := cids[i%len(cids)]
				req := httptest.NewRequest("GET", "/app/"+slug+"/", nil)
				req.AddCookie(&http.Cookie{Name: clientCookiePrefix + slug, Value: cid})
				rec := httptest.NewRecorder()
				p.ServeHTTP(rec, req)
			}
		}(g)
	}

	time.Sleep(750 * time.Millisecond)
	close(stop)
	wg.Wait()

	if panics != 0 {
		t.Fatalf("%d ServeHTTP panics", panics)
	}

	// Consistency after quiescing.
	time.Sleep(50 * time.Millisecond)
	p.mu.Lock()
	defer p.mu.Unlock()
	for s := 0; s < nSlugs; s++ {
		slug := "app" + strconv.Itoa(s)
		perWorker := map[int]int{}
		for _, cs := range p.clients[slug] {
			if cs.liveConns < 0 {
				t.Fatalf("%s: negative liveConns", slug)
			}
			perWorker[cs.slotID]++
		}
		for slotID, w := range p.pools[slug].workers {
			if w.assignedClients != perWorker[slotID] {
				t.Fatalf("%s slot %d: assignedClients=%d but %d client slots reference it", slug, slotID, w.assignedClients, perWorker[slotID])
			}
		}
	}
}
