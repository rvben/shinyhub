package process

import "strconv"

// cgroupV2CPUPeriod is the cpu.max enforcement window in microseconds. 100ms is
// the cgroup v2 default; a 100% quota over a 100000us period is one full core.
const cgroupV2CPUPeriod = 100000

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

// cgroupCPUMaxValue returns the value to write to a cgroup v2 cpu.max file for a
// quota percent, as "<quota> <period>" microseconds where 100 percent is one
// full core (quota == period). Zero or negative yields "max <period>" (no
// limit). Mirrors the Docker runtime's NanoCPUs mapping (100 -> 1 core).
func cgroupCPUMaxValue(cpuPct int) string {
	period := strconv.Itoa(cgroupV2CPUPeriod)
	if cpuPct <= 0 {
		return "max " + period
	}
	quota := cpuPct * cgroupV2CPUPeriod / 100
	return strconv.Itoa(quota) + " " + period
}
