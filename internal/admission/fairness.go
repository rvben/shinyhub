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
	if divisor <= 0 {
		// A non-positive divisor would make principalRate = rate/divisor either
		// +Inf (every principal bucket refills instantly, silently disabling
		// per-principal fairness) or negative (tokens drift below zero, breaking
		// the 0 <= tokens invariant the eviction scan relies on). Reject it the
		// same way an out-of-range capacity is rejected.
		panic("admission: divisor must be >= 1")
	}
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

// principalTokens returns principal's current bucket token count, or -1 when it
// has no bucket yet. Test-only.
func (a *AppLimiter) principalTokens(principal string) float64 {
	a.mu.Lock()
	p, ok := a.buckets[principal]
	a.mu.Unlock()
	if !ok {
		return -1
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.tokens
}

// AdmitResult reports which stage, if any, refused a two-stage admission attempt.
type AdmitResult int

const (
	// Admitted means both the principal and the shared bucket granted a token.
	Admitted AdmitResult = iota
	// PrincipalExhausted means the principal is over its own share; the shared
	// bucket was not touched and no token was spent.
	PrincipalExhausted
	// SharedExhausted means the principal was within its share (one principal
	// token was spent) but the shared app bucket was empty. A caller that then
	// waits for shared capacity must re-check with TrySharedOnly, not Admit, so it
	// does not re-debit the principal bucket on every retry.
	SharedExhausted
)

// Admit runs the two-stage check for principal and reports which stage refused.
// A principal over its own share is refused without debiting the shared bucket
// (PrincipalExhausted). A principal within its share whose app bucket is empty
// has already spent one principal token (SharedExhausted).
func (a *AppLimiter) Admit(principal string) AdmitResult {
	a.mu.Lock()
	p := a.principalPacerLocked(principal)
	a.mu.Unlock()

	if !p.TryTake() {
		return PrincipalExhausted // over its own share; shared bucket untouched
	}
	if !a.shared.TryTake() {
		return SharedExhausted // within share, app at capacity; principal token spent
	}
	return Admitted
}

// TrySharedOnly takes a shared token without touching any principal bucket. It is
// the park-retry primitive: the principal share was already charged by the Admit
// call that returned SharedExhausted, so re-charging the principal on every retry
// would drain a legitimate principal's small bucket and lock it out for as long
// as its slow refill takes, long after shared capacity recovers.
func (a *AppLimiter) TrySharedOnly() bool {
	return a.shared.TryTake()
}

// TryAdmit is the boolean form of Admit: true only when the principal is within
// its own share AND the shared bucket grants a token.
func (a *AppLimiter) TryAdmit(principal string) bool {
	return a.Admit(principal) == Admitted
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
