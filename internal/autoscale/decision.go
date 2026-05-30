package autoscale

import "math"

// scaleInput carries the resolved per-app figures the scaling decision uses.
// All fields are pre-resolved by the controller (cap and target already folded
// against the runtime defaults) so the decision itself stays a pure function.
type scaleInput struct {
	activeSessions int64   // total active sessions across live replicas
	current        int     // current replica count
	cap            int     // effective per-replica session cap
	target         float64 // effective target fraction of cap, in (0,1]
	min            int     // per-app lower bound
	max            int     // per-app upper bound
	runtimeMax     int     // runtime-wide replica ceiling
	saturated      bool    // pool-saturated rejections observed in the window
}

// desiredReplicas computes the replica count the controller should converge to
// for one app. It targets an average of target*cap active sessions per replica,
// rounding up so the pool never sits above target, then biases up by one when
// the pool is shedding load (pool-saturated rejects). The result is clamped to
// the per-app [min, max] bounds and the runtime ceiling, with a floor of one.
//
// When no effective cap or target is known the saturation ratio is undefined,
// so it holds the current count rather than scaling on a meaningless signal.
//
// reason values: "pool_saturated" when the saturation-bias branch forced the
// +1 increment; "session_load" in all other cases.
func desiredReplicas(in scaleInput) (desired int, reason string) {
	if in.cap <= 0 || in.target <= 0 {
		return in.current, "session_load"
	}

	perReplica := in.target * float64(in.cap)
	desired = int(math.Ceil(float64(in.activeSessions) / perReplica))
	reason = "session_load"

	// A saturated pool is actively rejecting sessions, so the measured active
	// count understates demand; ensure we add at least one replica.
	if in.saturated && desired <= in.current {
		desired = in.current + 1
		reason = "pool_saturated"
	}

	if desired < in.min {
		desired = in.min
	}
	if desired > in.max {
		desired = in.max
	}
	if in.runtimeMax > 0 && desired > in.runtimeMax {
		desired = in.runtimeMax
	}
	if desired < 1 {
		desired = 1
	}
	return desired, reason
}
