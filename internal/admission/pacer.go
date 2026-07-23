// Package admission provides render-aware admission control primitives: a
// token-bucket pacer, effective-core detection, per-principal fairness, and a
// host CPU watermark. It is a leaf library with no dependency on the proxy or
// any server package, so it is unit-tested in full under an injected clock.
package admission

import (
	"math"
	"sync"
	"time"
)

// Rate returns the sustained admissions per second for an app: the share of
// cores left after headroom, divided by the per-render cost. A render_seconds
// of zero (or negative) means pacing is disabled for the app and the rate is
// zero, which a caller reads as "do not pace".
func Rate(cores, headroom, renderSeconds float64) float64 {
	if renderSeconds <= 0 {
		return 0
	}
	return (cores * headroom) / renderSeconds
}

// Burst returns the instantaneous admission allowance, one per core, never
// below one so a lone arrival is never delayed even on a sub-core quota.
func Burst(cores float64) float64 {
	b := math.Round(cores)
	if b < 1 {
		return 1
	}
	return b
}

// Pacer is a single token bucket. Tokens refill continuously at rate per second
// up to burst. TryTake is non-blocking arithmetic over an injected clock, so it
// is safe to call on a hot path and deterministic in tests.
type Pacer struct {
	mu     sync.Mutex
	tokens float64
	rate   float64
	burst  float64
	last   time.Time
	nowFn  func() time.Time
}

// NewPacer builds a pacer that starts full (burst tokens available). A rate of
// zero never refills, so after the initial burst is spent the pacer refuses
// forever; that is the correct shape for a disabled or fully-constrained app.
// last is left zero and initialized lazily on the first TryTake call, so a
// caller that swaps nowFn after construction (as tests do) still gets an
// elapsed baseline from the clock actually in use, not from whatever clock
// was active at construction time.
func NewPacer(rate, burst float64) *Pacer {
	return &Pacer{
		tokens: burst,
		rate:   rate,
		burst:  burst,
		nowFn:  time.Now,
	}
}

// TryTake refills by the time elapsed since the last call (capped at burst),
// then consumes one token if at least one is available. It returns whether a
// token was consumed. A refused take consumes nothing.
func (p *Pacer) TryTake() bool {
	p.mu.Lock()
	defer p.mu.Unlock()

	now := p.nowFn()
	if p.last.IsZero() {
		p.last = now
	} else if elapsed := now.Sub(p.last).Seconds(); elapsed > 0 {
		p.tokens += elapsed * p.rate
		if p.tokens > p.burst {
			p.tokens = p.burst
		}
		p.last = now
	}
	if p.tokens >= 1 {
		p.tokens--
		return true
	}
	return false
}
