package process

import (
	"strconv"
	"strings"
)

// parseCgroupOOMCounts sums the kernel OOM-kill counters from the contents of a
// cgroup v2 memory.events file: oom_kill (per-process kills) plus oom_group_kill
// (whole-cgroup kills, present on kernels with memory.oom.group). The bare "oom"
// line (an event counter, not a kill) is deliberately ignored. ok is false when
// the content has no recognizable memory.events lines at all (e.g. an empty read
// or a non-Linux stub), so callers can distinguish "no kills" from "no data".
func parseCgroupOOMCounts(content string) (total uint64, ok bool) {
	for _, line := range strings.Split(content, "\n") {
		field, valStr, found := strings.Cut(strings.TrimSpace(line), " ")
		if !found {
			continue
		}
		switch field {
		case "low", "high", "max", "oom", "oom_kill", "oom_group_kill":
			ok = true // a recognizable memory.events file
		default:
			continue
		}
		if field == "oom_kill" || field == "oom_group_kill" {
			if v, err := strconv.ParseUint(valStr, 10, 64); err == nil {
				total += v
			}
		}
	}
	return total, ok
}

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

// defaultNativePidsMax caps the number of processes/threads a native app cgroup
// may hold, preventing a fork bomb in one app from exhausting the host PID table
// and taking down ShinyHub and every co-located tenant. Generous enough for a
// heavily-threaded data app; a fork bomb spawns orders of magnitude more. The
// Docker runtime applies an equivalent PidsLimit.
const defaultNativePidsMax = 1024

// cgroupPidsMaxValue returns the value to write to a cgroup v2 pids.max file:
// a decimal count, or "max" (unlimited) when limit is zero or negative.
func cgroupPidsMaxValue(limit int) string {
	if limit <= 0 {
		return "max"
	}
	return strconv.Itoa(limit)
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
