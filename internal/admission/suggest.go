package admission

import "math"

// suggestCadenceSeconds is the interaction cadence the cap suggestion assumes: a
// session triggers a fresh render about this often. It matches the aggressive
// cadence the verification rig used, so the number is reproducible and the API
// can state the assumption. It is a heuristic, not a guarantee.
const suggestCadenceSeconds = 2.0

// SuggestSessionCap returns a conservative max_sessions_per_replica for an app
// paced at renderSeconds on a host with the given effective cores and headroom
// fraction, plus the cadence it assumed. It sizes for sustained interaction: N
// sessions each rendering renderSeconds every cadence seconds demand
// N*renderSeconds/cadence cores, which stays within cores*headroom at
// N = Rate(cores,headroom,renderSeconds) * cadence, floored to >= 1. A
// renderSeconds <= 0 (pacing off) yields no suggestion. renderSeconds is assumed
// finite; callers validate and reject NaN/Inf before persistence.
func SuggestSessionCap(cores, headroom, renderSeconds float64) (cap int, cadenceSeconds float64) {
	if renderSeconds <= 0 {
		return 0, suggestCadenceSeconds
	}
	n := int(math.Floor(Rate(cores, headroom, renderSeconds) * suggestCadenceSeconds))
	if n < 1 {
		n = 1
	}
	return n, suggestCadenceSeconds
}
