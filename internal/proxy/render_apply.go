package proxy

import "github.com/rvben/shinyhub/internal/admission"

// SetRenderLimiterFactory installs the factory ApplyRenderPacing uses to build a
// per-app limiter from a render_seconds value, capturing the host sizing (cores,
// headroom, divisor, LRU) once at startup so Detect is never called on a request
// or reconcile path. Call once before serving.
func (p *Proxy) SetRenderLimiterFactory(f func(renderSeconds float64) *admission.AppLimiter) {
	p.appLimitersMu.Lock()
	defer p.appLimitersMu.Unlock()
	p.renderLimiterFactory = f
}

// ApplyRenderPacing installs, updates, or clears the render-admission limiter for
// slug to match renderSeconds. It is idempotent and safe from any goroutine: a
// repeated call with an unchanged value is a no-op that never resets the live
// token bucket. renderSeconds <= 0 clears pacing. With no factory set it is a
// no-op (pacing stays off). The entire compare-build-install runs in one
// appLimitersMu critical section, via setAppLimiterLocked rather than the
// re-locking public SetAppLimiter, so concurrent callers are serialized and the
// last writer's value is the one left installed (no stale limiter can be written
// after a newer one). BuildAppLimiter touches no proxy state and only allocates,
// so holding the lock across it is cheap.
func (p *Proxy) ApplyRenderPacing(slug string, renderSeconds float64) {
	p.appLimitersMu.Lock()
	defer p.appLimitersMu.Unlock()
	if p.renderLimiterFactory == nil {
		return
	}
	prev, had := p.appliedRenderSeconds[slug]
	if had && prev == renderSeconds {
		return // unchanged: keep the live token bucket
	}
	if !had && renderSeconds <= 0 {
		return // already off, staying off
	}
	limiter := p.renderLimiterFactory(renderSeconds) // nil when renderSeconds <= 0
	p.setAppLimiterLocked(slug, limiter)
	if renderSeconds > 0 {
		p.appliedRenderSeconds[slug] = renderSeconds
	} else {
		delete(p.appliedRenderSeconds, slug)
	}
}
