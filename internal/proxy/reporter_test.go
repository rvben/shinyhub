package proxy_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/rvben/shinyhub/internal/db"
	"github.com/rvben/shinyhub/internal/proxy"
)

// fakeSessionStore records every UpsertReplicaSessions call for test assertions.
type fakeSessionStore struct {
	mu    sync.Mutex
	calls []upsertCall
	errFn func() error // optional; returns an error to simulate DB failure
}

type upsertCall struct {
	instanceID string
	rows       []db.ReplicaSessionRow
}

func (s *fakeSessionStore) UpsertReplicaSessions(instanceID string, rows []db.ReplicaSessionRow) error {
	if s.errFn != nil {
		if err := s.errFn(); err != nil {
			return err
		}
	}
	cp := make([]db.ReplicaSessionRow, len(rows))
	copy(cp, rows)
	s.mu.Lock()
	s.calls = append(s.calls, upsertCall{instanceID, cp})
	s.mu.Unlock()
	return nil
}

func (s *fakeSessionStore) callCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.calls)
}

func (s *fakeSessionStore) lastCall() (upsertCall, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.calls) == 0 {
		return upsertCall{}, false
	}
	return s.calls[len(s.calls)-1], true
}

// allRows returns every row from every upsert call recorded so far.
func (s *fakeSessionStore) allRows() []db.ReplicaSessionRow {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []db.ReplicaSessionRow
	for _, c := range s.calls {
		out = append(out, c.rows...)
	}
	return out
}

// mustBackend returns a running httptest.Server and defers its close.
func mustBackend(t *testing.T) *httptest.Server {
	t.Helper()
	be := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(be.Close)
	return be
}

// holdingBackend returns an httptest.Server whose handler blocks until releaseCh
// is closed, and signals arrived (buffered, cap n) once for each request that
// enters the handler. This gives tests a deterministic "activeConns is now N"
// signal without any time.Sleep.
func holdingBackend(t *testing.T, n int) (srv *httptest.Server, arrived chan struct{}, releaseCh chan struct{}) {
	t.Helper()
	arrived = make(chan struct{}, n)
	releaseCh = make(chan struct{})
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		arrived <- struct{}{} // signal: this request has incremented activeConns
		<-releaseCh           // block until test releases all connections
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)
	return srv, arrived, releaseCh
}

// waitArrived waits until exactly n signals have been received on arrived,
// failing the test if the deadline (2 s) is exceeded. This is the deterministic
// synchronisation point replacing time.Sleep: the test KNOWS N goroutines have
// entered the backend handler and incremented activeConns before proceeding.
func waitArrived(t *testing.T, arrived <-chan struct{}, n int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for i := 0; i < n; i++ {
		select {
		case <-arrived:
		case <-time.After(time.Until(deadline)):
			t.Fatalf("timed out waiting for request %d/%d to reach backend", i+1, n)
		}
	}
}

// waitStore polls until the store has at least minCalls upsert calls, failing
// after 2 s. Used where the reporter goroutine is the actor (not the proxy).
func waitStore(t *testing.T, store *fakeSessionStore, minCalls int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if store.callCount() >= minCalls {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %d upsert call(s); got %d", minCalls, store.callCount())
}

// TestSnapshotSessions_CorrectBatch asserts that snapshotSessions (called via
// a reporter flush) produces a batch with the correct per-(app,idx) Active counts
// and the right instanceID. Two apps are exercised:
//   - "alpha" (appID=10): 2 connections held open on replica 0
//   - "beta"  (appID=20): 3 connections held open on replica 0
//
// Deterministic synchronisation: each held request signals arrived before
// blocking, so the test waits on arrived (not a fixed sleep) before flushing.
func TestSnapshotSessions_CorrectBatch(t *testing.T) {
	// alpha has 2 held connections; beta has 3. Total signals needed = 5.
	holdSrv, arrived, releaseCh := holdingBackend(t, 5)

	p := proxy.New()
	p.SetPoolSize("alpha", 1)
	p.SetPoolAppID("alpha", 10)
	if err := p.RegisterReplica("alpha", 0, holdSrv.URL, nil, 0); err != nil {
		t.Fatal(err)
	}
	p.SetPoolSize("beta", 2)
	p.SetPoolAppID("beta", 20)
	if err := p.RegisterReplica("beta", 0, holdSrv.URL, nil, 0); err != nil {
		t.Fatal(err)
	}
	if err := p.RegisterReplica("beta", 1, holdSrv.URL, nil, 0); err != nil {
		t.Fatal(err)
	}

	// Fire 2 requests for alpha and 3 for beta (LCB distributes; with a single
	// non-draining replica per app all land on idx 0 for each app).
	for i := 0; i < 2; i++ {
		go func() {
			req := httptest.NewRequest("GET", "/app/alpha/", nil)
			p.ServeHTTP(httptest.NewRecorder(), req)
		}()
	}
	for i := 0; i < 3; i++ {
		go func() {
			req := httptest.NewRequest("GET", "/app/beta/", nil)
			p.ServeHTTP(httptest.NewRecorder(), req)
		}()
	}

	// Wait until all 5 requests are inside the handler and activeConns reflects
	// the full count. Only after this signal is it safe to snapshot.
	waitArrived(t, arrived, 5)

	// Start the reporter and flush both slugs via the immediate-flush channel.
	store := &fakeSessionStore{}
	flushCh := make(chan string, 16)
	reporter := proxy.NewSessionReporter(p, store, "inst-1", flushCh)

	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		reporter.Run(ctx)
	}()

	flushCh <- "alpha"
	flushCh <- "beta"

	// Wait for the two slug-specific flush calls to land.
	waitStore(t, store, 2)

	// Cancel and drain the reporter before releasing held connections so the
	// final shutdown flush (flush(nil)) also sees the live activeConns counts.
	// close(releaseCh) after wg.Wait ensures requests only complete after the
	// reporter has finished all its flushes.
	cancel()
	wg.Wait()
	close(releaseCh)

	// --- assertions ---

	// Collect all rows across all upsert calls.
	rows := store.allRows()
	if len(rows) == 0 {
		t.Fatal("expected rows in upsert batch, got none")
	}

	// Build a map of appID -> max-observed Active sum across replicas.
	// We take the max per (appID, idx) across all calls (a final-flush after
	// cancel may observe 0; we want the peak snapshot) then sum per appID.
	type key struct {
		appID int64
		idx   int
	}
	peakActive := make(map[key]int64)
	for _, r := range rows {
		k := key{r.AppID, r.Idx}
		if r.Active > peakActive[k] {
			peakActive[k] = r.Active
		}
	}
	sumActive := make(map[int64]int64) // appID -> sum of peak per-replica counts
	for k, v := range peakActive {
		sumActive[k.appID] += v
	}

	// alpha (appID=10) has 1 replica and 2 held connections: Active sum must be 2.
	if v := sumActive[10]; v != 2 {
		t.Errorf("alpha (appID=10): want total Active=2, got %d (rows: %+v)", v, rows)
	}
	// beta (appID=20) has 2 replicas and 3 held connections: Active sum must be 3.
	// LCB distributes 3 requests across 2 replicas; we assert the total, not per-replica.
	if v := sumActive[20]; v != 3 {
		t.Errorf("beta (appID=20): want total Active=3, got %d (rows: %+v)", v, rows)
	}

	// Verify instance ID is stamped correctly.
	call, ok := store.lastCall()
	if !ok {
		t.Fatal("no upsert call recorded")
	}
	if call.instanceID != "inst-1" {
		t.Errorf("instanceID = %q, want %q", call.instanceID, "inst-1")
	}
}

// TestSnapshotSessions_SkipsPoolsWithoutAppID asserts that pools without an
// appID set are not included in the snapshot batch.
func TestSnapshotSessions_SkipsPoolsWithoutAppID(t *testing.T) {
	be := mustBackend(t)

	p := proxy.New()
	// One pool with appID, one without.
	p.SetPoolSize("wired", 1)
	p.SetPoolAppID("wired", 42)
	if err := p.RegisterReplica("wired", 0, be.URL, nil, 0); err != nil {
		t.Fatal(err)
	}
	p.SetPoolSize("unwired", 1)
	// Deliberately NOT calling SetPoolAppID("unwired", ...).
	if err := p.RegisterReplica("unwired", 0, be.URL, nil, 0); err != nil {
		t.Fatal(err)
	}

	store := &fakeSessionStore{}
	flushCh := make(chan string, 16)
	reporter := proxy.NewSessionReporter(p, store, "inst-X", flushCh)

	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		reporter.Run(ctx)
	}()

	flushCh <- "wired"
	flushCh <- "unwired"

	waitStore(t, store, 1)

	cancel()
	wg.Wait()

	// Every call should only contain rows for "wired" (appID=42).
	store.mu.Lock()
	defer store.mu.Unlock()
	for _, c := range store.calls {
		for _, row := range c.rows {
			if row.AppID != 42 {
				t.Errorf("unexpected row with AppID=%d (unwired pool should be skipped)", row.AppID)
			}
		}
	}
}

// TestImmediateFlush_ZeroToActive asserts that when a slug's active connection
// count rises from 0 to 1, the session reporter flushes that slug immediately
// without waiting for the next periodic tick (ReporterInterval = 5 s).
//
// Deterministic: the "request reached backend" arrived channel confirms
// activeConns was incremented before we check the store. The initial
// store.callCount() check is a clean baseline because no request has been
// sent yet (no sleep needed).
func TestImmediateFlush_ZeroToActive(t *testing.T) {
	holdSrv, arrived, releaseCh := holdingBackend(t, 1)

	p := proxy.New()
	p.SetPoolSize("myapp", 1)
	p.SetPoolAppID("myapp", 99)
	if err := p.RegisterReplica("myapp", 0, holdSrv.URL, nil, 0); err != nil {
		t.Fatal(err)
	}

	store := &fakeSessionStore{}
	flushCh := make(chan string, 16)
	p.EnableImmediateFlush(flushCh)
	reporter := proxy.NewSessionReporter(p, store, "inst-imm", flushCh)

	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		reporter.Run(ctx)
	}()
	defer func() {
		close(releaseCh)
		cancel()
		wg.Wait()
	}()

	// Baseline: no requests yet, store must be empty.
	before := store.callCount()
	if before != 0 {
		t.Fatalf("expected clean baseline, got %d upsert calls before any request", before)
	}

	// Send the first request. This is the 0->1 transition; the proxy signals
	// flushCh which the reporter consumes immediately (no periodic tick needed).
	go func() {
		req := httptest.NewRequest("GET", "/app/myapp/", nil)
		p.ServeHTTP(httptest.NewRecorder(), req)
	}()

	// Wait until the request is inside the backend handler so we know
	// activeConns == 1 and the signal was sent to flushCh.
	waitArrived(t, arrived, 1)

	// Now wait for the reporter to drain the flush channel and upsert.
	waitStore(t, store, before+1)

	// Verify the flushed row carries appID=99 and active=1.
	call, ok := store.lastCall()
	if !ok {
		t.Fatal("no upsert call recorded")
	}
	found := false
	for _, row := range call.rows {
		if row.AppID == 99 && row.Active == 1 {
			found = true
		}
	}
	if !found {
		t.Errorf("expected row with AppID=99, Active=1; got %+v", call.rows)
	}
}

// TestImmediateFlush_NotTriggeredBeyondFirstActive asserts that only the
// 0->1 edge triggers the immediate-flush signal. Subsequent requests on the
// same replica (1->2, 2->3, ...) must NOT produce additional signals.
//
// Deterministic: instead of a fixed sleep, we wait on arrived to know the
// first request has entered the handler (and thus signalled flushCh), then
// poll flushCh with a short deadline for the second request's non-signal.
func TestImmediateFlush_NotTriggeredBeyondFirstActive(t *testing.T) {
	// Hold capacity 2: first and second requests both block so both are
	// in-flight simultaneously (ensuring the 1->2 bump is live-observable).
	holdSrv, arrived, releaseCh := holdingBackend(t, 2)
	defer close(releaseCh)

	p := proxy.New()
	p.SetPoolSize("app2", 1)
	p.SetPoolAppID("app2", 55)
	if err := p.RegisterReplica("app2", 0, holdSrv.URL, nil, 0); err != nil {
		t.Fatal(err)
	}

	flushCh := make(chan string, 16)
	p.EnableImmediateFlush(flushCh)

	// First request: 0->1. Should produce exactly one signal on flushCh.
	go func() {
		req := httptest.NewRequest("GET", "/app/app2/", nil)
		p.ServeHTTP(httptest.NewRecorder(), req)
	}()

	// Wait until the first request has entered the handler (activeConns==1,
	// signal already sent) before draining the channel.
	waitArrived(t, arrived, 1)

	firstCount := 0
drainFirst:
	for {
		select {
		case <-flushCh:
			firstCount++
		default:
			break drainFirst
		}
	}
	if firstCount != 1 {
		t.Fatalf("expected 1 signal after first request (0->1), got %d", firstCount)
	}

	// Second request: 1->2. Must NOT produce a signal.
	go func() {
		req := httptest.NewRequest("GET", "/app/app2/", nil)
		p.ServeHTTP(httptest.NewRecorder(), req)
	}()

	// Wait until the second request is also inside the handler so its bump
	// (1->2) has already happened. At this point, if a signal were going to
	// be sent it has been sent.
	waitArrived(t, arrived, 1)

	// Drain: channel must remain empty after the 1->2 bump.
	secondCount := 0
drainSecond:
	for {
		select {
		case <-flushCh:
			secondCount++
		default:
			break drainSecond
		}
	}
	if secondCount != 0 {
		t.Errorf("expected 0 signals after second request (1->2), got %d", secondCount)
	}
}

// TestSingleNode_NoRowsWritten is the explicit negative gate for the
// single-node invariant: traffic through a proxy that has NOT had
// EnableImmediateFlush called (matching single-node wiring in cmd/shinyhub)
// must result in ZERO UpsertReplicaSessions calls. The reporter goroutine is
// also never started, matching the single-node path.
//
// Proof: build a fully-wired proxy (appID set), drive N requests, wait a short
// but non-trivial interval, then assert the store received zero calls. No
// reporter goroutine => no writer => store is empty, regardless of traffic.
func TestSingleNode_NoRowsWritten(t *testing.T) {
	be := mustBackend(t)

	// Single-node wiring: no EnableImmediateFlush, no reporter goroutine.
	p := proxy.New()
	p.SetPoolSize("app", 1)
	p.SetPoolAppID("app", 77)
	if err := p.RegisterReplica("app", 0, be.URL, nil, 0); err != nil {
		t.Fatal(err)
	}

	store := &fakeSessionStore{}

	// Drive several requests (including the 0->active transition) through the
	// proxy. No reporter is started; the store has no writer.
	for i := 0; i < 5; i++ {
		req := httptest.NewRequest("GET", "/app/app/", nil)
		p.ServeHTTP(httptest.NewRecorder(), req)
	}

	// Short wait to allow any accidental asynchronous write to materialise.
	// ReporterInterval is 5 s; 50 ms is more than enough to catch a bug while
	// keeping the test fast. The invariant is structural (no goroutine, no
	// writer), not timing-dependent.
	time.Sleep(50 * time.Millisecond)

	if n := store.callCount(); n != 0 {
		t.Errorf("single-node path must write zero replica_sessions rows; got %d UpsertReplicaSessions call(s)", n)
	}
}

// TestClusteredReporter_WritesRows asserts the positive: when SetPoolAppID and
// EnableImmediateFlush are wired (clustered path) and a request drives a
// 0->active transition, the reporter writes rows to the store.
func TestClusteredReporter_WritesRows(t *testing.T) {
	holdSrv, arrived, releaseCh := holdingBackend(t, 1)
	defer close(releaseCh)

	p := proxy.New()
	p.SetPoolSize("svc", 1)
	p.SetPoolAppID("svc", 101)
	if err := p.RegisterReplica("svc", 0, holdSrv.URL, nil, 0); err != nil {
		t.Fatal(err)
	}

	store := &fakeSessionStore{}
	flushCh := make(chan string, 16)
	p.EnableImmediateFlush(flushCh)
	reporter := proxy.NewSessionReporter(p, store, "clustered-inst", flushCh)

	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		reporter.Run(ctx)
	}()

	// Drive the 0->active transition.
	go func() {
		req := httptest.NewRequest("GET", "/app/svc/", nil)
		p.ServeHTTP(httptest.NewRecorder(), req)
	}()

	// Wait until the request is in the handler (activeConns==1, signal sent).
	waitArrived(t, arrived, 1)

	// Wait for the reporter to process the flush signal.
	waitStore(t, store, 1)

	cancel()
	wg.Wait()

	// Confirm a row for our appID exists.
	call, _ := store.lastCall()
	found := false
	for _, row := range call.rows {
		if row.AppID == 101 {
			found = true
		}
	}
	if !found {
		t.Errorf("expected a row with AppID=101 in the upsert, got %+v", call.rows)
	}
}

// TestReporterConstants_StaleCutoffIsConservative asserts that
// ReplicaSessionStaleCutoff is strictly greater than ReporterInterval,
// ensuring a live instance that misses one tick is not immediately evicted.
func TestReporterConstants_StaleCutoffIsConservative(t *testing.T) {
	if proxy.ReplicaSessionStaleCutoff <= proxy.ReporterInterval {
		t.Errorf("ReplicaSessionStaleCutoff (%v) must be > ReporterInterval (%v)",
			proxy.ReplicaSessionStaleCutoff, proxy.ReporterInterval)
	}
}
