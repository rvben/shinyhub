package proxy

import "github.com/rvben/shinyhub/internal/admission"

// BuildAppLimiter constructs the per-app AppLimiter from an app's render cost and
// the host sizing knobs, or returns nil when render_seconds is not positive,
// which means pacing is disabled for the app and no limiter should exist. The
// shared bucket is sized R = (cores * headroom) / render_seconds with burst =
// round(cores); per-principal buckets get R/divisor with principalBurst.
//
// Exported (capital B) because cmd/shinyhub/main.go, in package main, calls it
// at startup; an unexported helper is not reachable across packages.
func BuildAppLimiter(renderSeconds, cores, headroom, principalBurst float64, divisor, lruCapacity int) *admission.AppLimiter {
	if renderSeconds <= 0 {
		return nil
	}
	rate := admission.Rate(cores, headroom, renderSeconds)
	burst := admission.Burst(cores)
	return admission.NewAppLimiter(rate, burst, divisor, principalBurst, lruCapacity)
}
