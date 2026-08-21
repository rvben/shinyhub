package admission

import gopsmem "github.com/shirou/gopsutil/v4/mem"

// DetectMemory resolves the memory capacity of the box ShinyHub runs on, in
// MiB, along with the source of that number and whether any source answered.
// override comes from server.host_capacity_memory_mb; a positive value is
// authoritative.
//
// The third return value is the point of this signature: a host with no
// readable memory figure is reported as unknown, never as a host with zero
// memory. A zero would render as a box with no capacity, which is a plausible
// value for something that is simply not known.
//
// This measures the memory visible to the ShinyHub process. Under
// runtime.mode: docker the app workers are separately capped containers, so
// this describes the shared box they run on rather than any one worker's
// limit; those apps carry enforced per-app limits and are measured against
// those instead.
func DetectMemory(overrideMB int) (int, string, bool) {
	limit, limitOK := readMemoryLimit()
	total, totalOK := readHostMemoryTotal()
	return resolveMemory(overrideMB, limit, limitOK, total, totalOK)
}

// readHostMemoryTotal reports the total memory the OS says this host has, in
// bytes. On Linux that is MemTotal, which lxcfs rewrites per-container, so a
// container usually sees its own allowance here; on macOS it is the sysctl
// figure. An error, or a nonsensical zero, reports no answer.
func readHostMemoryTotal() (uint64, bool) {
	vm, err := gopsmem.VirtualMemory()
	if err != nil || vm == nil || vm.Total == 0 {
		return 0, false
	}
	return vm.Total, true
}

// resolveMemory is the pure resolution logic, separated from platform reads so
// it is testable on any host. Order: a positive override wins; otherwise take
// the smaller of the cgroup limit and the host total, reporting cgroup-limit
// only when the cgroup is the binding term. With neither source it reports
// unknown.
func resolveMemory(overrideMB int, limit uint64, limitOK bool, total uint64, totalOK bool) (int, string, bool) {
	if overrideMB > 0 {
		return overrideMB, "config", true
	}
	if limitOK && limit > 0 && (!totalOK || limit < total) {
		return bytesToMB(limit), "cgroup-limit", true
	}
	if totalOK && total > 0 {
		return bytesToMB(total), "host-total", true
	}
	return 0, "", false
}

// bytesToMB converts to whole MiB, with a floor of 1 so a capacity that was
// genuinely read never reports as 0 and gets mistaken for "unknown".
func bytesToMB(b uint64) int {
	mb := int(b / (1024 * 1024))
	if mb < 1 {
		return 1
	}
	return mb
}
