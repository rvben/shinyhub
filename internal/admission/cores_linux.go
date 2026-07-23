//go:build linux

package admission

import (
	"os"
	"strconv"
	"strings"
)

// readCPUQuota returns the cgroup CPU bandwidth limit for this process in whole
// cores (quota / period), and whether a finite limit was found. It reads
// cgroup v2 (cpu.max: "quota period", or "max period" for unlimited) first, then
// falls back to cgroup v1 (cpu.cfs_quota_us / cpu.cfs_period_us). Any read or
// parse failure returns ok=false so the caller falls back to affinity rather
// than treating a missing limit as zero cores.
func readCPUQuota() (float64, bool) {
	if q, ok := readCgroupV2Quota(); ok {
		return q, true
	}
	return readCgroupV1Quota()
}

func readCgroupV2Quota() (float64, bool) {
	b, err := os.ReadFile("/sys/fs/cgroup/cpu.max")
	if err != nil {
		return 0, false
	}
	fields := strings.Fields(strings.TrimSpace(string(b)))
	if len(fields) != 2 {
		return 0, false
	}
	if fields[0] == "max" {
		return 0, false // no limit
	}
	quota, err := strconv.ParseFloat(fields[0], 64)
	if err != nil {
		return 0, false
	}
	period, err := strconv.ParseFloat(fields[1], 64)
	if err != nil || period <= 0 {
		return 0, false
	}
	return quota / period, true
}

func readCgroupV1Quota() (float64, bool) {
	qb, err := os.ReadFile("/sys/fs/cgroup/cpu/cpu.cfs_quota_us")
	if err != nil {
		return 0, false
	}
	quota, err := strconv.ParseFloat(strings.TrimSpace(string(qb)), 64)
	if err != nil || quota <= 0 { // -1 means unlimited
		return 0, false
	}
	pb, err := os.ReadFile("/sys/fs/cgroup/cpu/cpu.cfs_period_us")
	if err != nil {
		return 0, false
	}
	period, err := strconv.ParseFloat(strings.TrimSpace(string(pb)), 64)
	if err != nil || period <= 0 {
		return 0, false
	}
	return quota / period, true
}
