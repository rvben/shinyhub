package proxy

import (
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/rvben/shinyhub/internal/config"
)

// TestReserveWorker_CeilingRaceProof spins 20 goroutines all racing to call
// reserveWorker on a pool capped at 3. Exactly 3 must succeed (>= 0) and 17
// must get -1. The 3 returned slot IDs must be distinct. Run with -race.
func TestReserveWorker_CeilingRaceProof(t *testing.T) {
	const slug = "myapp"
	const maxWorkers = 3
	const goroutines = 20

	p := New()
	p.SetPoolMode(slug, config.IsolationPerSession, 0, maxWorkers)

	var (
		mu      sync.Mutex
		succIDs []int
		failCnt int
	)

	var wg sync.WaitGroup
	wg.Add(goroutines)
	for i := 0; i < goroutines; i++ {
		clientID := "client" + string(rune('A'+i))
		go func(cid string) {
			defer wg.Done()
			slotID := p.reserveWorker(slug, cid)
			mu.Lock()
			defer mu.Unlock()
			if slotID >= 0 {
				succIDs = append(succIDs, slotID)
			} else {
				failCnt++
			}
		}(clientID)
	}
	wg.Wait()

	if len(succIDs) != maxWorkers {
		t.Errorf("got %d successful reservations, want %d", len(succIDs), maxWorkers)
	}
	if failCnt != goroutines-maxWorkers {
		t.Errorf("got %d -1 returns, want %d", failCnt, goroutines-maxWorkers)
	}

	// Slot IDs must be distinct.
	sort.Ints(succIDs)
	for i := 1; i < len(succIDs); i++ {
		if succIDs[i] == succIDs[i-1] {
			t.Errorf("duplicate slot ID %d in successful reservations", succIDs[i])
		}
	}
}

// TestReserveWorker_NonElasticReturnsMinusOne verifies that reserveWorker
// returns -1 for a multiplex pool (not elastic).
func TestReserveWorker_NonElasticReturnsMinusOne(t *testing.T) {
	p := New()
	p.SetPoolMode("app", config.IsolationMultiplex, 0, 0)

	if got := p.reserveWorker("app", "client1"); got != -1 {
		t.Errorf("expected -1 for multiplex pool, got %d", got)
	}
}

// TestClientConnOpened_CancelsReleaseTimer verifies that clientConnOpened
// stops a pending release timer so a reconnecting client does not get its
// worker terminated mid-session.
func TestClientConnOpened_CancelsReleaseTimer(t *testing.T) {
	// Shorten the grace TTL so the test completes quickly.
	old := clientGraceTTL
	clientGraceTTL = 50 * time.Millisecond
	t.Cleanup(func() { clientGraceTTL = old })

	const slug = "myapp"
	const clientID = "c1"

	terminateCalled := make(chan struct{}, 1)
	p := New()
	p.SetPoolMode(slug, config.IsolationPerSession, 0, 5)
	p.SetTerminateFunc(func(s string, slotID int) {
		terminateCalled <- struct{}{}
	})

	slotID := p.reserveWorker(slug, clientID)
	if slotID < 0 {
		t.Fatal("reserveWorker returned -1 unexpectedly")
	}
	p.bindClient(slug, clientID, slotID)

	// Open a connection, then close it (arms the timer), then open again.
	p.clientConnOpened(slug, clientID)
	p.clientConnClosed(slug, clientID) // timer starts
	// Immediately reopen before grace window expires.
	p.clientConnOpened(slug, clientID) // must stop and nil the timer

	// Wait well past the original grace TTL.
	select {
	case <-terminateCalled:
		t.Error("terminate was called after clientConnOpened cancelled the timer")
	case <-time.After(200 * time.Millisecond):
		// Good: no termination fired.
	}

	// Verify assignedClients is still 1 (binding is intact).
	p.mu.RLock()
	pool := p.pools[slug]
	w := pool.workers[slotID]
	ac := w.assignedClients
	p.mu.RUnlock()
	if ac != 1 {
		t.Errorf("assignedClients = %d, want 1 (binding still live)", ac)
	}
}

// TestClientConnClosed_RetirePathFiresTerminate verifies the full retire path:
// bind, open, close, wait past TTL -> terminate fires exactly once and
// assignedClients returns to 0.
func TestClientConnClosed_RetirePathFiresTerminate(t *testing.T) {
	old := clientGraceTTL
	clientGraceTTL = 50 * time.Millisecond
	t.Cleanup(func() { clientGraceTTL = old })

	const slug = "myapp"
	const clientID = "c1"

	var terminateCount int
	var terminateMu sync.Mutex
	terminateDone := make(chan struct{})

	p := New()
	p.SetPoolMode(slug, config.IsolationPerSession, 0, 5)
	p.SetTerminateFunc(func(s string, slotID int) {
		terminateMu.Lock()
		terminateCount++
		terminateMu.Unlock()
		close(terminateDone)
	})

	slotID := p.reserveWorker(slug, clientID)
	if slotID < 0 {
		t.Fatal("reserveWorker returned -1 unexpectedly")
	}
	p.bindClient(slug, clientID, slotID)
	p.clientConnOpened(slug, clientID)
	p.clientConnClosed(slug, clientID) // arms timer

	select {
	case <-terminateDone:
		// Good.
	case <-time.After(500 * time.Millisecond):
		t.Fatal("terminate was not called within grace window")
	}

	terminateMu.Lock()
	count := terminateCount
	terminateMu.Unlock()
	if count != 1 {
		t.Errorf("terminate called %d times, want 1", count)
	}

	// assignedClients must be back to 0 after retire.
	p.mu.RLock()
	pool := p.pools[slug]
	w := pool.workers[slotID]
	var ac int
	if w != nil {
		ac = w.assignedClients
	}
	p.mu.RUnlock()
	if ac != 0 {
		t.Errorf("assignedClients = %d after retire, want 0", ac)
	}
}

// TestReleaseReservation_RemovesBootingWorker verifies that ReleaseReservation
// removes a booting slot from the pool (used by boot-timeout cleanup).
func TestReleaseReservation_RemovesBootingWorker(t *testing.T) {
	const slug = "myapp"

	p := New()
	p.SetPoolMode(slug, config.IsolationPerSession, 0, 5)

	slotID := p.reserveWorker(slug, "client1")
	if slotID < 0 {
		t.Fatal("reserveWorker returned -1 unexpectedly")
	}

	// Confirm it's in the pool.
	p.mu.RLock()
	_, inPool := p.pools[slug].workers[slotID]
	p.mu.RUnlock()
	if !inPool {
		t.Fatal("worker not found in pool after reserveWorker")
	}

	p.ReleaseReservation(slug, slotID)

	p.mu.RLock()
	_, stillIn := p.pools[slug].workers[slotID]
	p.mu.RUnlock()
	if stillIn {
		t.Error("worker still in pool after ReleaseReservation")
	}
}

// TestReleaseReservation_CleansUpClientSlots verifies that ReleaseReservation
// also removes any client slots bound to the failing slotID, so a pre-bound
// client is not left referencing a dead worker.
func TestReleaseReservation_CleansUpClientSlots(t *testing.T) {
	const slug = "myapp"
	const clientID = "c1"

	p := New()
	p.SetPoolMode(slug, config.IsolationPerSession, 0, 5)

	slotID := p.reserveWorker(slug, clientID)
	if slotID < 0 {
		t.Fatal("reserveWorker returned -1 unexpectedly")
	}
	// Bind a client to the booting slot.
	p.bindClient(slug, clientID, slotID)

	p.mu.RLock()
	_, clientBound := p.clients[slug][clientID]
	p.mu.RUnlock()
	if !clientBound {
		t.Fatal("client slot not present after bindClient")
	}

	// Release the booting reservation on failure.
	p.ReleaseReservation(slug, slotID)

	p.mu.RLock()
	_, workerStillIn := p.pools[slug].workers[slotID]
	_, clientStillIn := p.clients[slug][clientID]
	p.mu.RUnlock()

	if workerStillIn {
		t.Error("booting slot still in pool after ReleaseReservation")
	}
	if clientStillIn {
		t.Error("client slot still present after ReleaseReservation; should have been cleaned up")
	}
}

// TestDeregisterElasticWorker_RemovesWorkerAndClient verifies that
// DeregisterElasticWorker removes a running worker and all bound client slots.
func TestDeregisterElasticWorker_RemovesWorkerAndClient(t *testing.T) {
	const slug = "myapp"

	p := New()
	p.SetPoolMode(slug, config.IsolationPerSession, 0, 5)

	// Register a running worker directly.
	if err := p.RegisterElasticWorker(slug, 7, "http://127.0.0.1:9991", nil, 42); err != nil {
		t.Fatalf("RegisterElasticWorker: %v", err)
	}
	// Bind two clients to it.
	p.bindClient(slug, "cA", 7)
	p.bindClient(slug, "cB", 7)

	p.mu.RLock()
	_, workerIn := p.pools[slug].workers[7]
	_, cAIn := p.clients[slug]["cA"]
	_, cBIn := p.clients[slug]["cB"]
	p.mu.RUnlock()
	if !workerIn || !cAIn || !cBIn {
		t.Fatal("precondition: worker and clients must be present before deregister")
	}

	p.DeregisterElasticWorker(slug, 7)

	p.mu.RLock()
	_, workerStill := p.pools[slug].workers[7]
	_, cAStill := p.clients[slug]["cA"]
	_, cBStill := p.clients[slug]["cB"]
	p.mu.RUnlock()

	if workerStill {
		t.Error("worker still in pool after DeregisterElasticWorker")
	}
	if cAStill || cBStill {
		t.Error("client slots still present after DeregisterElasticWorker")
	}
}

// TestClientConnClosed_DoubleCloseIsSafe verifies that calling clientConnClosed
// twice (spurious extra close) does not drive liveConns below 0, does not
// arm a second timer, and fires terminate exactly once after the grace TTL.
func TestClientConnClosed_DoubleCloseIsSafe(t *testing.T) {
	old := clientGraceTTL
	clientGraceTTL = 50 * time.Millisecond
	t.Cleanup(func() { clientGraceTTL = old })

	const slug = "myapp"
	const clientID = "c-double"

	var terminateCount int
	var terminateMu sync.Mutex
	terminateDone := make(chan struct{}, 2) // buffered so goroutine never blocks

	p := New()
	p.SetPoolMode(slug, config.IsolationPerSession, 0, 5)
	p.SetTerminateFunc(func(s string, slotID int) {
		terminateMu.Lock()
		terminateCount++
		terminateMu.Unlock()
		select {
		case terminateDone <- struct{}{}:
		default:
		}
	})

	slotID := p.reserveWorker(slug, clientID)
	if slotID < 0 {
		t.Fatal("reserveWorker returned -1 unexpectedly")
	}
	p.bindClient(slug, clientID, slotID)
	p.clientConnOpened(slug, clientID)

	// First close: arms the grace-period timer normally.
	p.clientConnClosed(slug, clientID)

	// Second close: spurious extra close; must be a no-op (floor guard).
	p.clientConnClosed(slug, clientID)

	// liveConns must not have gone below 0.
	p.mu.RLock()
	cs := p.lookupClientSlot(slug, clientID)
	var lc int
	if cs != nil {
		lc = cs.liveConns
	}
	p.mu.RUnlock()
	if lc < 0 {
		t.Errorf("liveConns = %d after double-close, want >= 0", lc)
	}

	// Wait for the grace period to fire.
	select {
	case <-terminateDone:
		// Good.
	case <-time.After(500 * time.Millisecond):
		t.Fatal("terminate was not called within grace window")
	}

	// Allow any second (erroneous) timer to fire before we count.
	time.Sleep(200 * time.Millisecond)

	terminateMu.Lock()
	count := terminateCount
	terminateMu.Unlock()
	if count != 1 {
		t.Errorf("terminate called %d times, want exactly 1", count)
	}

	// assignedClients must be back to 0.
	p.mu.RLock()
	pool := p.pools[slug]
	w := pool.workers[slotID]
	var ac int
	if w != nil {
		ac = w.assignedClients
	}
	p.mu.RUnlock()
	if ac != 0 {
		t.Errorf("assignedClients = %d after retire, want 0", ac)
	}
}

// TestReserveWorker_DrainingWorkerDoesNotCountTowardCeiling verifies that
// draining workers do not consume capacity (ceiling is active-worker count).
func TestReserveWorker_DrainingWorkerDoesNotCountTowardCeiling(t *testing.T) {
	const slug = "myapp"

	p := New()
	p.SetPoolMode(slug, config.IsolationPerSession, 0, 2)

	// Fill the pool.
	id1 := p.reserveWorker(slug, "c1")
	id2 := p.reserveWorker(slug, "c2")
	if id1 < 0 || id2 < 0 {
		t.Fatal("expected two successful reservations")
	}

	// Mark one worker as draining.
	p.mu.Lock()
	p.pools[slug].workers[id1].status = workerDraining
	p.mu.Unlock()

	// Now a third reservation should succeed (only 1 active worker).
	id3 := p.reserveWorker(slug, "c3")
	if id3 < 0 {
		t.Error("reserveWorker returned -1 when a draining slot should free up capacity")
	}
}

// TestGhostClient_ReclaimedAfterWorkerReady verifies that a client which
// reserved+bound a slot but never opened a connection (ghost client) is
// reclaimed via the grace timer armed in RegisterElasticWorker. Without
// Fix 1, assignedClients stays at 1 forever because clientConnClosed (which
// arms the timer) is never called when liveConns was always 0.
func TestGhostClient_ReclaimedAfterWorkerReady(t *testing.T) {
	old := clientGraceTTL
	clientGraceTTL = 50 * time.Millisecond
	t.Cleanup(func() { clientGraceTTL = old })

	const slug = "ghostapp"
	const clientID = "ghost-c1"

	var terminateCount int
	var terminateMu sync.Mutex
	terminateDone := make(chan struct{})

	p := New()
	p.SetPoolMode(slug, config.IsolationPerSession, 0, 5)
	p.SetTerminateFunc(func(s string, slotID int) {
		terminateMu.Lock()
		terminateCount++
		terminateMu.Unlock()
		close(terminateDone)
	})

	slotID := p.reserveWorker(slug, clientID)
	if slotID < 0 {
		t.Fatal("reserveWorker returned -1 unexpectedly")
	}
	p.bindClient(slug, clientID, slotID)
	// Do NOT call clientConnOpened: the client received the loading page but
	// never reconnected (ghost-client scenario).

	// Registering the worker as ready must arm the grace timer for the ghost client.
	if err := p.RegisterElasticWorker(slug, slotID, "http://127.0.0.1:19999", nil, 1); err != nil {
		t.Fatalf("RegisterElasticWorker: %v", err)
	}

	// Wait past the grace window: terminate should fire exactly once.
	select {
	case <-terminateDone:
		// Good: ghost client was reclaimed.
	case <-time.After(500 * time.Millisecond):
		t.Fatal("terminate was not called within grace window after RegisterElasticWorker")
	}

	terminateMu.Lock()
	count := terminateCount
	terminateMu.Unlock()
	if count != 1 {
		t.Errorf("terminate called %d times, want 1", count)
	}

	// assignedClients must be 0 after reclaim.
	p.mu.RLock()
	w := p.pools[slug].workers[slotID]
	var ac int
	if w != nil {
		ac = w.assignedClients
	}
	p.mu.RUnlock()
	if ac != 0 {
		t.Errorf("assignedClients = %d after ghost reclaim, want 0", ac)
	}
}

// TestGhostClient_TimerCancelledByRealConnect verifies that when a ghost
// client (bound but liveConns==0) opens a real connection within the grace
// window, the grace timer is cancelled and the worker is not terminated.
func TestGhostClient_TimerCancelledByRealConnect(t *testing.T) {
	old := clientGraceTTL
	clientGraceTTL = 100 * time.Millisecond
	t.Cleanup(func() { clientGraceTTL = old })

	const slug = "ghostapp2"
	const clientID = "ghost-c2"

	terminated := make(chan struct{}, 1)
	p := New()
	p.SetPoolMode(slug, config.IsolationPerSession, 0, 5)
	p.SetTerminateFunc(func(s string, slotID int) {
		select {
		case terminated <- struct{}{}:
		default:
		}
	})

	slotID := p.reserveWorker(slug, clientID)
	if slotID < 0 {
		t.Fatal("reserveWorker returned -1 unexpectedly")
	}
	p.bindClient(slug, clientID, slotID)
	// Do NOT open a connection yet.

	// Worker becomes ready: grace timer is armed for the ghost client.
	if err := p.RegisterElasticWorker(slug, slotID, "http://127.0.0.1:19998", nil, 1); err != nil {
		t.Fatalf("RegisterElasticWorker: %v", err)
	}

	// Real connection arrives within the grace window: must cancel the timer.
	p.clientConnOpened(slug, clientID)

	// Wait well past the grace TTL; terminate must NOT have fired.
	select {
	case <-terminated:
		t.Error("terminate was called after clientConnOpened cancelled the ghost timer")
	case <-time.After(400 * time.Millisecond):
		// Good: no termination fired.
	}

	// assignedClients must still be 1 (binding is intact, connection is live).
	p.mu.RLock()
	w := p.pools[slug].workers[slotID]
	ac := w.assignedClients
	p.mu.RUnlock()
	if ac != 1 {
		t.Errorf("assignedClients = %d, want 1 (real connection is live)", ac)
	}
}
