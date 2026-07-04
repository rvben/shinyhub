package process

import "testing"

// TestParseCgroupOOMCounts verifies the cgroup v2 memory.events parser sums the
// kernel OOM-kill counters (oom_kill + oom_group_kill) and ignores unrelated
// event lines. ok=false only when the content is unparseable/missing.
func TestParseCgroupOOMCounts(t *testing.T) {
	const memoryEvents = `low 0
high 0
max 12
oom 4
oom_kill 3
oom_group_kill 1
`
	got, ok := parseCgroupOOMCounts(memoryEvents)
	if !ok {
		t.Fatal("expected ok=true for well-formed memory.events")
	}
	if got != 4 { // 3 oom_kill + 1 oom_group_kill
		t.Errorf("parseCgroupOOMCounts sum = %d, want 4 (oom_kill 3 + oom_group_kill 1)", got)
	}

	// No OOM lines yet (a freshly created cgroup) parses to 0, ok=true.
	if got, ok := parseCgroupOOMCounts("low 0\nhigh 0\nmax 0\n"); !ok || got != 0 {
		t.Errorf("no-oom content: got (%d,%v), want (0,true)", got, ok)
	}

	// "oom" alone (the event count, not a kill) must NOT be counted.
	if got, _ := parseCgroupOOMCounts("oom 7\n"); got != 0 {
		t.Errorf("bare 'oom' line counted as a kill: got %d, want 0", got)
	}

	// Empty content is not a valid memory.events file.
	if _, ok := parseCgroupOOMCounts(""); ok {
		t.Error("empty content should report ok=false")
	}
}

// TestCgroupPidsMaxValue verifies the pids.max value mapping: a positive limit
// becomes its decimal, and zero/negative means unlimited ("max").
func TestCgroupPidsMaxValue(t *testing.T) {
	if got := cgroupPidsMaxValue(0); got != "max" {
		t.Errorf("cgroupPidsMaxValue(0) = %q, want max", got)
	}
	if got := cgroupPidsMaxValue(-3); got != "max" {
		t.Errorf("cgroupPidsMaxValue(-3) = %q, want max", got)
	}
	if got := cgroupPidsMaxValue(1024); got != "1024" {
		t.Errorf("cgroupPidsMaxValue(1024) = %q, want 1024", got)
	}
	if defaultNativePidsMax <= 0 {
		t.Errorf("defaultNativePidsMax must be a positive fork-bomb ceiling, got %d", defaultNativePidsMax)
	}
}

// TestJobCgroupNameNeverCollidesWithReplica verifies a one-shot job's cgroup name
// is disjoint from every replica's app-<slug>-<index>, so a capped job can never
// be placed into (and write its PID into) a live replica's cgroup.
func TestJobCgroupNameNeverCollidesWithReplica(t *testing.T) {
	const slug = "dash"
	job := jobCgroupName(slug, 0) // runID 0 is the adversarial case (vs replica index 0)
	for idx := 0; idx < 8; idx++ {
		if job == appCgroupName(slug, idx) {
			t.Fatalf("jobCgroupName(%q,0)=%q collides with appCgroupName(%q,%d)", slug, job, slug, idx)
		}
	}
	if got, want := jobCgroupName(slug, 7), "job-dash-7"; got != want {
		t.Errorf("jobCgroupName = %q, want %q", got, want)
	}
}

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
