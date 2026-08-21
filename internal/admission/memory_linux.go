//go:build linux

package admission

import (
	"os"
	"strconv"
	"strings"
)

// unlimitedMemoryFloor is the point above which a cgroup memory limit is read
// as "no limit" rather than as a real number. Both cgroup versions spell
// unlimited as a near-max integer rather than a sentinel word (v1 uses
// PAGE_COUNTER_MAX, whose exact value varies with word size and page size), so
// a magnitude test is the portable check. 1 PiB is far above any host ShinyHub
// runs on and far below every unlimited spelling.
const unlimitedMemoryFloor = uint64(1) << 50

// readMemoryLimit returns the cgroup memory limit for this process in bytes,
// and whether a finite limit was found. It reads cgroup v2 (memory.max, which
// is the literal "max" when unlimited) first, then falls back to cgroup v1
// (memory.limit_in_bytes). Any read or parse failure returns ok=false so the
// caller falls back to the host total rather than treating a missing limit as
// zero memory.
func readMemoryLimit() (uint64, bool) {
	if v, ok := readCgroupV2Memory(); ok {
		return v, true
	}
	return readCgroupV1Memory()
}

func readCgroupV2Memory() (uint64, bool) {
	b, err := os.ReadFile("/sys/fs/cgroup/memory.max")
	if err != nil {
		return 0, false
	}
	field := strings.TrimSpace(string(b))
	if field == "max" {
		return 0, false // no limit
	}
	return parseMemoryLimit(field)
}

func readCgroupV1Memory() (uint64, bool) {
	b, err := os.ReadFile("/sys/fs/cgroup/memory/memory.limit_in_bytes")
	if err != nil {
		return 0, false
	}
	return parseMemoryLimit(strings.TrimSpace(string(b)))
}

func parseMemoryLimit(field string) (uint64, bool) {
	v, err := strconv.ParseUint(field, 10, 64)
	if err != nil || v == 0 || v >= unlimitedMemoryFloor {
		return 0, false
	}
	return v, true
}
