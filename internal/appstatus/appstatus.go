// Package appstatus is the single vocabulary for the observed lifecycle state
// of an app: the value of `status` in GET /api/apps/{slug}. The server derives
// that value from process reality (see decorateAppObservation in internal/api)
// and every client-side decision that reads it (deploy readiness gates, `apps
// open`, colouring) goes through the classifiers here. When the server grows a
// new observed status it is added to Observed, which forces the classifiers to
// take a position on it, so the CLI never again learns about a status by
// timing out on it.
package appstatus

// Observed status values. DesiredStatus keeps the stored intent
// (running/stopped/hibernated/deleting); Status is what the processes are
// actually doing.
const (
	// Running: at least one replica or elastic worker is serving.
	Running = "running"
	// Idle: a healthy elastic pool with no live worker. Elastic pools
	// (grouped / per_session) spawn workers on demand, so an idle app is fully
	// ready to serve; the first session boots a worker.
	Idle = "idle"
	// Starting: replicas or workers are booting and none serves yet.
	Starting = "starting"
	// Degraded: serving with lost or crashed replicas, or desired running with
	// no replica at all.
	Degraded = "degraded"
	// Crashed: the app's replicas exited unexpectedly and none serves.
	Crashed = "crashed"
	// Hibernated: parked warm after an idle period; the next request wakes it.
	Hibernated = "hibernated"
	// Suspended: parked by the runtime (frozen replicas); resumes on demand.
	Suspended = "suspended"
	// Stopped: intentionally down.
	Stopped = "stopped"
	// Deleting: being removed.
	Deleting = "deleting"
	// Waking: leaving hibernation (client-side transient shown by the UI).
	Waking = "waking"
	// Deploying: a deployment is in flight (client-side transient).
	Deploying = "deploying"
)

// Observed lists every status the server can report. Class must classify each
// one; TestClass_CoversEveryObservedStatus enforces it.
var Observed = []string{
	Running, Idle, Starting, Degraded, Crashed, Hibernated, Suspended,
	Stopped, Deleting, Waking, Deploying,
}

// Kind groups statuses by what a caller can do with the app.
type Kind int

const (
	// KindUnknown is a status this package does not recognise. Callers treat it
	// conservatively (keep waiting, do not open, render uncoloured).
	KindUnknown Kind = iota
	// KindServing: the app answers requests now. Running, and idle for elastic
	// pools that boot a worker on the first request.
	KindServing
	// KindTransitional: on its way to another state; keep waiting.
	KindTransitional
	// KindParked: intentionally asleep and wakes on demand (hibernated, suspended).
	KindParked
	// KindFailed: down because something broke (crashed).
	KindFailed
	// KindDown: intentionally not serving (stopped, deleting).
	KindDown
	// KindImpaired: serving but not fully healthy (degraded).
	KindImpaired
)

// Class reports what status means for a caller. Matching is exact and
// lower-case: the server emits lower-case, and callers that accept user input
// lower-case it first.
func Class(status string) Kind {
	switch status {
	case Running, Idle:
		return KindServing
	case Starting, Waking, Deploying:
		return KindTransitional
	case Hibernated, Suspended:
		return KindParked
	case Crashed:
		return KindFailed
	case Stopped, Deleting:
		return KindDown
	case Degraded:
		return KindImpaired
	}
	return KindUnknown
}

// Serving reports whether an app in status answers requests without further
// operator action: running, or idle (a healthy elastic pool that spawns a
// worker on the first request). This is the readiness predicate for
// `deploy --wait`, `fleet apply` and the other health gates.
func Serving(status string) bool {
	return Class(status) == KindServing
}
