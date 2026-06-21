package process

import "strconv"

// cgroupMemoryMaxValue returns the value to write to a cgroup v2 memory.max file
// for a per-app limit in mebibytes: a byte count, or "max" (unlimited) when the
// limit is zero or negative. Mirrors the Docker runtime's MemoryBytes mapping
// (MemoryLimitMB * 1024 * 1024).
func cgroupMemoryMaxValue(memMB int) string {
	if memMB <= 0 {
		return "max"
	}
	return strconv.FormatInt(int64(memMB)*1024*1024, 10)
}
