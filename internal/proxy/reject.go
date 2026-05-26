package proxy

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
