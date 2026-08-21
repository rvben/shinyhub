//go:build !linux

package admission

// readMemoryLimit reports no cgroup memory limit on non-Linux hosts. macOS
// development hosts have no cgroup, so the host total is the correct and only
// answer, not a degraded path.
func readMemoryLimit() (uint64, bool) {
	return 0, false
}
