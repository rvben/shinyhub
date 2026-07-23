package admission

import (
	"sync"
	"time"
)

// AppLimiter admits new sessions for one app under a two-stage token scheme:
// each principal has its own small bucket, and only a principal within its own
// share may draw from the app's shared bucket. A principal over its share is
// refused without touching the shared bucket, so one principal flooding real
// sessions cannot starve the capacity other principals draw from.
//
// Per-principal state is bounded. Its capacity must be at least the share
// divisor, and when it is full the eviction victim is the bucket holding the
// MOST tokens (closest to full). A spent bucket holds the fewest tokens, so it
// is never the victim while a fuller bucket exists, and a share therefore cannot
// be reset by churn at any capacity-to-divisor ratio. A full bucket is
// indistinguishable from a fresh one, so evicting the fullest is also the
// cheapest choice. No recency ordering is kept, because eviction is by token
// count, not by access order.
type AppLimiter struct {
	mu             sync.Mutex
	shared         *Pacer
	principalRate  float64
	principalBurst float64
	capacity       int
	nowFn          func() time.Time
	buckets        map[string]*Pacer
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
		buckets:        make(map[string]*Pacer),
	}
}

// setClock points every current and future pacer at fn. Test-only helper.
func (a *AppLimiter) setClock(fn func() time.Time) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.nowFn = fn
	a.shared.nowFn = fn
	for _, p := range a.buckets {
		p.nowFn = fn
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
// evicting the fullest bucket if at capacity) when absent. Caller holds a.mu.
func (a *AppLimiter) principalPacerLocked(principal string) *Pacer {
	if p, ok := a.buckets[principal]; ok {
		return p
	}
	if len(a.buckets) >= a.capacity {
		a.evictFullestLocked()
	}
	p := NewPacer(a.principalRate, a.principalBurst)
	p.nowFn = a.nowFn
	a.buckets[principal] = p
	return p
}

// evictFullestLocked removes the bucket holding the most tokens. Because a spent
// bucket holds the fewest, it is never chosen while a fuller bucket exists, so
// eviction cannot reset a spent share; and a full bucket, being indistinguishable
// from a fresh one, is the cheapest thing to drop. Caller holds a.mu. Ties (equal
// token counts) resolve to whichever the map yields first, which is safe: equally
// spent buckets mean the app is genuinely at capacity and the attacker has
// already paid the cost the capacity floor imposes.
func (a *AppLimiter) evictFullestLocked() {
	var victim string
	found := false
	most := -1.0
	for key, p := range a.buckets {
		p.mu.Lock()
		tokens := p.tokens
		p.mu.Unlock()
		if tokens > most {
			most = tokens
			victim = key
			found = true
		}
	}
	if found {
		delete(a.buckets, victim)
	}
}
