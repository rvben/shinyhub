package proxy

import (
	"net/http"
	"time"
)

// RejectReason is the closed vocabulary stamped onto platform-emitted data-plane
// rejections. It is the single source of truth shared by the X-Shinyhub-Reject
// response header, the in-memory rolling rollup (rejectCounter), and the
// Prometheus admission-rejects counter.
type RejectReason string

const (
	// ReasonUnknownSlug: no app with this slug is registered on this server (404).
	ReasonUnknownSlug RejectReason = "unknown-slug"
	// ReasonPoolSaturated: all configured replicas are live and at their
	// per-replica session cap (503). Remedy: raise --max-sessions-per-replica
	// and/or --replicas.
	ReasonPoolSaturated RejectReason = "pool-saturated"
	// ReasonPoolDegraded: fewer replicas are registered than configured and the
	// survivors are at cap (503). Remedy: check replica health before adding
	// capacity.
	ReasonPoolDegraded RejectReason = "pool-degraded"
	// ReasonAppNotReady: a known (or not-confidently-unknown) app has no replica
	// that has completed a WebSocket handshake yet (503, readiness probe).
	ReasonAppNotReady RejectReason = "app-not-ready"
)

// rejectSentinel is the metrics key substituted for any slug that is not a
// registered app, so attacker-controlled slugs (random 404 / fail-open
// readiness probes) cannot create unbounded ring-counter keys or Prometheus
// series.
const rejectSentinel = "__unknown__"

// RejectRecorder is an optional sink for admission-reject events, satisfied by
// the metrics registry. Defined here (not imported) so the proxy keeps zero
// Prometheus dependencies and stays unit-testable.
type RejectRecorder interface {
	RecordReject(slug, reason string)
}

// SetRejectRecorder wires (or clears, with nil) the admission-reject sink.
// Instance-level and atomic, matching SetAccessLogger/SetClientIPResolver.
// Safe to call concurrently with ServeHTTP.
func (p *Proxy) SetRejectRecorder(rec RejectRecorder) {
	if rec == nil {
		p.rejectRecorder.Store(nil)
		return
	}
	p.rejectRecorder.Store(&rec)
}

// RejectsByReason returns the per-reason rejection counts recorded for slug over
// roughly the last d. Reasons with no rejections in the window are omitted; a
// slug with none returns nil.
func (p *Proxy) RejectsByReason(slug string, d time.Duration) map[RejectReason]uint64 {
	return p.rejects.window(slug, d)
}

// ForgetRejects drops all rejection history for slug. Call only when the app is
// logically gone (post-delete), never on Deregister, which fires on every
// redeploy/restart/stop.
func (p *Proxy) ForgetRejects(slug string) {
	p.rejects.forget(slug)
}

// recordReject sets the X-Shinyhub-Reject header, stashes the reason for the
// access log when w is the request statusRecorder, records the rolling count,
// and forwards to the metrics sink if wired. MUST NOT be called while holding
// p.mu (it takes the reject-counter mutex and may call the injected recorder).
//
// registered is the cardinality guard: when true the real slug is the metrics
// key (a pool exists, so it is in the fleet); when false the key collapses to
// the sentinel so attacker-controlled slugs cannot explode cardinality. The
// header and any body/access-log entry still carry the real slug.
func (p *Proxy) recordReject(w http.ResponseWriter, slug string, reason RejectReason, registered bool) {
	w.Header().Set("X-Shinyhub-Reject", string(reason))
	if sr, ok := w.(*statusRecorder); ok {
		sr.rejectReason = reason
	}
	key := rejectSentinel
	if registered {
		key = slug
	}
	p.rejects.record(key, reason)
	if rp := p.rejectRecorder.Load(); rp != nil {
		(*rp).RecordReject(key, string(reason))
	}
}
