//go:build !linux

package admission

// readCPUQuota reports no cgroup quota on non-Linux hosts. macOS development
// hosts have no cgroup, so affinity is the correct and only answer, not a
// degraded path.
func readCPUQuota() (float64, bool) {
	return 0, false
}
