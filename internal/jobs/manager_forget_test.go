package jobs

import (
	"sync"
	"testing"
)

// TestManager_ForgetSchedule_RemovesOnlyThatSchedulesEntries reproduces the
// unbounded growth: lockFor, queueChan, admissionLockFor, and producerGateFor
// lazily create a map entry per schedule ID and nothing ever removed one, so a
// server that creates and deletes many schedules over its lifetime grows these
// four maps forever. ForgetSchedule must drop exactly the target ID's entries
// from all four maps and leave any other schedule's entries untouched.
func TestManager_ForgetSchedule_RemovesOnlyThatSchedulesEntries(t *testing.T) {
	m := &Manager{
		locks:          map[int64]*schedLock{1: newSchedLock(), 2: newSchedLock()},
		queues:         map[int64]chan struct{}{1: make(chan struct{}, 1), 2: make(chan struct{}, 1)},
		admissionLocks: map[int64]*sync.Mutex{1: {}, 2: {}},
		producerGates:  map[int64]*sync.RWMutex{1: {}, 2: {}},
	}

	m.ForgetSchedule(1)

	if _, ok := m.locks[1]; ok {
		t.Error("locks[1] still present after ForgetSchedule(1)")
	}
	if _, ok := m.queues[1]; ok {
		t.Error("queues[1] still present after ForgetSchedule(1)")
	}
	if _, ok := m.admissionLocks[1]; ok {
		t.Error("admissionLocks[1] still present after ForgetSchedule(1)")
	}
	if _, ok := m.producerGates[1]; ok {
		t.Error("producerGates[1] still present after ForgetSchedule(1)")
	}
	if _, ok := m.locks[2]; !ok {
		t.Error("locks[2] removed by ForgetSchedule(1); must only affect its own ID")
	}
	if _, ok := m.queues[2]; !ok {
		t.Error("queues[2] removed by ForgetSchedule(1); must only affect its own ID")
	}
	if _, ok := m.admissionLocks[2]; !ok {
		t.Error("admissionLocks[2] removed by ForgetSchedule(1); must only affect its own ID")
	}
	if _, ok := m.producerGates[2]; !ok {
		t.Error("producerGates[2] removed by ForgetSchedule(1); must only affect its own ID")
	}
}

// TestManager_ForgetSchedule_SafeWithAConcurrentHolder proves that dropping the
// map entry does not disturb a goroutine that already holds a reference to the
// old lock: it keeps working exactly as before, and a fresh lookup afterward
// synthesizes an independent new lock rather than reusing or corrupting state.
func TestManager_ForgetSchedule_SafeWithAConcurrentHolder(t *testing.T) {
	m := &Manager{locks: map[int64]*schedLock{}}
	m.mu.Lock()
	held := m.lockFor(1)
	m.mu.Unlock()
	if !held.tryLock() {
		t.Fatal("acquire lock before forgetting")
	}

	m.ForgetSchedule(1)

	m.mu.Lock()
	fresh := m.lockFor(1)
	m.mu.Unlock()
	if fresh == held {
		t.Fatal("lockFor recreated the same object after ForgetSchedule; want an independent new lock")
	}
	if !fresh.tryLock() {
		t.Fatal("fresh lock for a forgotten ID must start unlocked")
	}
	held.unlock() // must not panic even though its map entry is long gone.
}

// TestManager_ForgetApp_RemovesOnlyThatAppsPublicationGate mirrors the
// schedule-scoped test for the app-keyed publicationGates map.
func TestManager_ForgetApp_RemovesOnlyThatAppsPublicationGate(t *testing.T) {
	m := &Manager{publicationGates: map[int64]*sync.RWMutex{10: {}, 20: {}}}

	m.ForgetApp(10)

	if _, ok := m.publicationGates[10]; ok {
		t.Error("publicationGates[10] still present after ForgetApp(10)")
	}
	if _, ok := m.publicationGates[20]; !ok {
		t.Error("publicationGates[20] removed by ForgetApp(10); must only affect its own ID")
	}
}
