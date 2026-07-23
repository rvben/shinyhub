package admission

import "runtime"

// Detect resolves the CPU capacity the pacer sizes against, and the source of
// that number so an operator can see which one is in effect. override comes
// from server.render_capacity_cores; a positive value is authoritative.
//
// Detect measures the cores available to the ShinyHub process. Under
// runtime.mode: docker the app workers are separately capped containers, so
// this number is not their render capacity; that case is an explicit non-goal
// with a startup warning wired by the caller.
func Detect(override float64) (float64, string) {
	// GOMAXPROCS is container-aware as of Go 1.25 (this repo builds on a newer
	// toolchain), so it already folds in the cgroup CPU-bandwidth limit. It is
	// an integer that rounds a fractional quota up, which over-states cores, so
	// it is used only as one input to a minimum, never as the answer.
	gomaxprocs := runtime.GOMAXPROCS(0)
	quota, quotaOK := readCPUQuota()
	return resolve(override, gomaxprocs, quota, quotaOK)
}

// resolve is the pure resolution logic, separated from platform reads so it is
// testable on any host. Order: a positive override wins; otherwise take the
// minimum of the affinity-derived core count and a cgroup quota when one exists,
// reporting cgroup-quota only when the quota is the binding term.
func resolve(override float64, gomaxprocs int, quota float64, quotaOK bool) (float64, string) {
	if override > 0 {
		return override, "config"
	}
	affinity := float64(gomaxprocs)
	if quotaOK && quota > 0 && quota < affinity {
		return quota, "cgroup-quota"
	}
	return affinity, "affinity"
}
