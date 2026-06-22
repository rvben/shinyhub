package process

import "testing"

// TestCgroupMemoryMaxValue verifies the value written to a cgroup v2 memory.max
// file: a byte count for a positive MB limit, "max" (unlimited) otherwise.
func TestCgroupMemoryMaxValue(t *testing.T) {
	cases := []struct {
		memMB int
		want  string
	}{
		{0, "max"},
		{-1, "max"},
		{1, "1048576"},           // 1 MiB
		{512, "536870912"},       // 512 MiB
		{2048, "2147483648"},     // 2 GiB
		{262144, "274877906944"}, // 256 GiB: no int overflow
	}
	for _, c := range cases {
		if got := cgroupMemoryMaxValue(c.memMB); got != c.want {
			t.Errorf("cgroupMemoryMaxValue(%d) = %q, want %q", c.memMB, got, c.want)
		}
	}
}

// TestCgroupCPUMaxValue verifies the value written to a cgroup v2 cpu.max file:
// "<quota> <period>" microseconds where 100% == one full core (quota == period),
// and "max <period>" for no limit. Mirrors the Docker runtime's NanoCPUs mapping.
func TestCgroupCPUMaxValue(t *testing.T) {
	cases := []struct {
		cpuPct int
		want   string
	}{
		{0, "max 100000"},
		{-5, "max 100000"},
		{100, "100000 100000"}, // one full core
		{50, "50000 100000"},   // half a core
		{25, "25000 100000"},   // quarter core
		{1, "1000 100000"},     // 1%
	}
	for _, c := range cases {
		if got := cgroupCPUMaxValue(c.cpuPct); got != c.want {
			t.Errorf("cgroupCPUMaxValue(%d) = %q, want %q", c.cpuPct, got, c.want)
		}
	}
}
