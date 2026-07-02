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
		mu       sync.Mutex
		succIDs  []int
		failCnt  int
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

// TestReleaseReservation_RemovesBootingWorker verifies that releaseReservation
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

	p.releaseReservation(slug, slotID)

	p.mu.RLock()
	_, stillIn := p.pools[slug].workers[slotID]
	p.mu.RUnlock()
	if stillIn {
		t.Error("worker still in pool after releaseReservation")
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
