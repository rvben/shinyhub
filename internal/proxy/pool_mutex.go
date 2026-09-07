package proxy

import "sync"

// poolMutex preserves a single exclusive lifecycle lock while distributing
// connection-accounting readers across cache lines. Ordinary routing readers
// use RLock; accounting readers use accountingRLock. Writers exclude both.
//
// Lock order is the main RWMutex, accounting stripes, then clientSlot.mu.
// Accounting readers must never acquire the main lock while holding a stripe.
// Consequently all existing lifecycle mutations can continue to access client
// state without separately taking clientSlot.mu under the exclusive lock.
type poolMutex struct {
	sync.RWMutex
	accounting [32]accountingStripe
}

type accountingStripe struct {
	mu sync.RWMutex
	// Keep adjacent reader counters on separate cache lines, including on
	// Apple silicon with 128-byte cache lines.
	_ [128]byte
}

func (m *poolMutex) Lock() {
	m.RWMutex.Lock()
	for i := range m.accounting {
		m.accounting[i].mu.Lock()
	}
}

func (m *poolMutex) Unlock() {
	for i := len(m.accounting) - 1; i >= 0; i-- {
		m.accounting[i].mu.Unlock()
	}
	m.RWMutex.Unlock()
}

func (m *poolMutex) accountingRLock(slug, clientID string) *sync.RWMutex {
	// A stable allocation-free hash spreads distinct apps without introducing
	// another shared atomic counter on the read path.
	var hash uint32 = 2166136261
	for i := 0; i < len(slug); i++ {
		hash = (hash ^ uint32(slug[i])) * 16777619
	}
	for i := 0; i < len(clientID); i++ {
		hash = (hash ^ uint32(clientID[i])) * 16777619
	}
	stripe := &m.accounting[hash%uint32(len(m.accounting))].mu
	stripe.RLock()
	return stripe
}
