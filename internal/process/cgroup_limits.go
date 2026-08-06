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

// RequiredDelegatedControllers is the cgroup v2 controller set ShinyHub manages
// per app: cpu.max, memory.max (plus warm-wake reclaim) and pids.max. It is the
// literal an operator copies into a systemd unit's Delegate=, so it is also
// what the shipped unit must carry - a controller missing there is not an
// error, it is a limit that silently stops being enforced.
const RequiredDelegatedControllers = "cpu memory pids"

// cgroupDelegation reports which of the optional controllers ShinyHub manages
// are delegated to the service, and therefore which per-app limits actually
// bind. Memory is absent because it is required: the delegated base does not
// come up without it.
type cgroupDelegation struct {
	// CPU is true when cpu.max can be set on an app cgroup.
	CPU bool
	// Pids is true when pids.max (the fork-bomb cap) can be set on an app cgroup.
	Pids bool
}

// planDelegation derives a base cgroup's controller bookkeeping from its two
// controller files: available is cgroup.controllers (what the parent delegated
// to this cgroup) and enabled is cgroup.subtree_control (what this cgroup
// already delegates to its children). It returns the "+controller" writes still
// needed, whether the subtree is already fully prepared, and which optional
// controllers will bind once those writes land.
//
// The two files answer different questions, and only the second one decides
// whether a limit applies: a controller must be in the base's subtree_control
// for its interface file to exist in a child. A unit that delegates pids
// without a "+pids" write therefore produces app cgroups with no pids.max at
// all - the fork-bomb cap is simply absent, which no configuration surface
// reports, because from the unit's point of view the controller is delegated.
func planDelegation(available, enabled string) (enable []string, prepared bool, d cgroupDelegation) {
	d = cgroupDelegation{
		CPU:  fieldsContain(available, "cpu"),
		Pids: fieldsContain(available, "pids"),
	}
	// Memory is the one controller with no availability branch: a base missing
	// it never reaches here (the caller fails first with its own message).
	if !fieldsContain(enabled, "memory") {
		enable = append(enable, "+memory")
	}
	if d.CPU && !fieldsContain(enabled, "cpu") {
		enable = append(enable, "+cpu")
	}
	if d.Pids && !fieldsContain(enabled, "pids") {
		enable = append(enable, "+pids")
	}
	return enable, len(enable) == 0, d
}

// fieldsContain reports whether the space-separated list in s contains field.
func fieldsContain(s, field string) bool {
	for _, f := range strings.Fields(s) {
		if f == field {
			return true
		}
	}
	return false
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
