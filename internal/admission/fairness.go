package admission

import (
	"container/list"
	"sync"
	"time"
)

// AppLimiter admits new sessions for one app under a two-stage token scheme:
// each principal has its own small bucket, and only a principal within its own
// share may draw from the app's shared bucket. A principal over its share is
// refused without touching the shared bucket, so one principal flooding real
// sessions cannot starve the capacity other principals draw from.
//
// Per-principal state is a bounded LRU. Its capacity must be at least the
// share divisor, and eviction prefers full (unspent) buckets, so an attacker
// cannot reset a spent share by churning the LRU: a full bucket is
// indistinguishable from a fresh one, so evicting it costs the attacker nothing.
type AppLimiter struct {
	mu             sync.Mutex
	shared         *Pacer
	principalRate  float64
	principalBurst float64
	capacity       int
	nowFn          func() time.Time

	// LRU of principals. front is most-recently-used.
	order   *list.List
	buckets map[string]*principalEntry
}

type principalEntry struct {
	pacer *Pacer
	elem  *list.Element // position in order; elem.Value is the principal key
}

// NewAppLimiter builds a limiter whose shared bucket uses (rate, burst) and
// whose per-principal buckets use (rate/divisor, principalBurst). It panics if
// lruCapacity is below divisor: a smaller capacity would let eviction reduce
// the number of principals needed to consume the app rate below the divisor,
// silently weakening the guarantee.
func NewAppLimiter(rate, burst float64, divisor int, principalBurst float64, lruCapacity int) *AppLimiter {
	if lruCapacity < divisor {
		panic("admission: lruCapacity must be >= divisor")
	}
	nowFn := time.Now
	shared := NewPacer(rate, burst)
	shared.nowFn = nowFn
	return &AppLimiter{
		shared:         shared,
		principalRate:  rate / float64(divisor),
		principalBurst: principalBurst,
		capacity:       lruCapacity,
		nowFn:          nowFn,
		order:          list.New(),
		buckets:        make(map[string]*principalEntry),
	}
}

// setClock points every current and future pacer at fn. Test-only helper.
func (a *AppLimiter) setClock(fn func() time.Time) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.nowFn = fn
	a.shared.nowFn = fn
	for _, e := range a.buckets {
		e.pacer.nowFn = fn
	}
}

// sharedTokens returns the shared bucket's current token count. Test-only.
func (a *AppLimiter) sharedTokens() float64 {
	a.shared.mu.Lock()
	defer a.shared.mu.Unlock()
	return a.shared.tokens
}

// TryAdmit runs the two-stage check for principal. It returns true only when the
// principal is within its own share AND the shared bucket grants a token. An
// over-share principal is refused without debiting the shared bucket.
func (a *AppLimiter) TryAdmit(principal string) bool {
	a.mu.Lock()
	p := a.principalPacerLocked(principal)
	a.mu.Unlock()

	if !p.TryTake() {
		return false // over its own share; shared bucket untouched
	}
	return a.shared.TryTake()
}

// principalPacerLocked returns the pacer for principal, creating it (and
// evicting a full bucket if at capacity) when absent. Caller holds a.mu.
func (a *AppLimiter) principalPacerLocked(principal string) *Pacer {
	if e, ok := a.buckets[principal]; ok {
		a.order.MoveToFront(e.elem)
		return e.pacer
	}
	if len(a.buckets) >= a.capacity {
		a.evictOneLocked()
	}
	p := NewPacer(a.principalRate, a.principalBurst)
	p.nowFn = a.nowFn
	elem := a.order.PushFront(principal)
	a.buckets[principal] = &principalEntry{pacer: p, elem: elem}
	return p
}

// evictOneLocked removes one principal. It prefers a full (unspent) bucket,
// which is indistinguishable from a fresh one so its eviction is free. When no
// full bucket exists, every resident is at least partly spent, and it evicts
// the most-recently-touched one instead of the least-recently-used one: an
// attacker who churns brand-new identities to push an older, already-spent
// principal toward the LRU tail only ever displaces its OWN previous churn
// identity (which just took the front slot), never the older victim sitting at
// the back, so a spent share survives arbitrary churn instead of being reset.
// Caller holds a.mu.
func (a *AppLimiter) evictOneLocked() {
	// Scan for a full bucket; direction does not matter for correctness since
	// evicting any full bucket is free.
	for e := a.order.Back(); e != nil; e = e.Prev() {
		key := e.Value.(string)
		entry := a.buckets[key]
		entry.pacer.mu.Lock()
		full := entry.pacer.tokens >= entry.pacer.burst
		entry.pacer.mu.Unlock()
		if full {
			a.order.Remove(e)
			delete(a.buckets, key)
			return
		}
	}
	// No full bucket: evict the most-recently-used, not the least. Plain LRU
	// eviction is exactly what a churn attacker weaponizes, since touching new
	// identities naturally pushes the target to the tail; evicting the front
	// instead sacrifices the attacker's own newest churn identity every time.
	front := a.order.Front()
	if front == nil {
		return
	}
	key := front.Value.(string)
	a.order.Remove(front)
	delete(a.buckets, key)
}
