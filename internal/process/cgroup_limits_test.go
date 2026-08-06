package process

import (
	"strings"
	"testing"
)

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

// TestPlanDelegation verifies which controllers a base cgroup still has to
// enable in its subtree, and which per-app limits bind as a result.
//
// The case that matters is a base whose parent delegated pids but whose subtree
// only enables cpu and memory: the app cgroup then has no pids.max file at all,
// so the fork-bomb cap is absent on a host whose unit says Delegate=pids. That
// state must be reported unprepared and must produce a "+pids" write.
func TestPlanDelegation(t *testing.T) {
	cases := []struct {
		name       string
		available  string // cgroup.controllers
		enabled    string // cgroup.subtree_control
		wantEnable []string
		wantReady  bool
		want       cgroupDelegation
	}{
		{
			name:       "fresh base delegating everything enables all three",
			available:  "cpuset cpu io memory pids\n",
			enabled:    "\n",
			wantEnable: []string{"+memory", "+cpu", "+pids"},
			want:       cgroupDelegation{CPU: true, Pids: true},
		},
		{
			name:       "pids delegated but not enabled is not prepared",
			available:  "cpu memory pids\n",
			enabled:    "cpu memory\n",
			wantEnable: []string{"+pids"},
			want:       cgroupDelegation{CPU: true, Pids: true},
		},
		{
			name:      "all three enabled is prepared",
			available: "cpu memory pids\n",
			enabled:   "cpu memory pids\n",
			wantReady: true,
			want:      cgroupDelegation{CPU: true, Pids: true},
		},
		{
			name:      "memory-only delegation is prepared with both optional limits off",
			available: "memory\n",
			enabled:   "memory\n",
			wantReady: true,
			want:      cgroupDelegation{},
		},
		{
			name:       "cpu delegated without pids enables only what exists",
			available:  "cpu memory\n",
			enabled:    "memory\n",
			wantEnable: []string{"+cpu"},
			want:       cgroupDelegation{CPU: true},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			enable, prepared, got := planDelegation(tc.available, tc.enabled)
			if prepared != tc.wantReady {
				t.Errorf("prepared = %v, want %v", prepared, tc.wantReady)
			}
			if got != tc.want {
				t.Errorf("delegation = %+v, want %+v", got, tc.want)
			}
			if len(enable) != len(tc.wantEnable) {
				t.Fatalf("enable = %v, want %v", enable, tc.wantEnable)
			}
			for i := range tc.wantEnable {
				if enable[i] != tc.wantEnable[i] {
					t.Fatalf("enable = %v, want %v", enable, tc.wantEnable)
				}
			}
		})
	}
}

// TestPlanDelegationCoversEveryRequiredController verifies planDelegation
// accounts for each controller the shipped unit's Delegate= line promises. A
// controller added to RequiredDelegatedControllers without a matching clause
// here would be delegated by systemd and then never enabled in the subtree,
// which is exactly how pids.max came to be silently absent.
func TestPlanDelegationCoversEveryRequiredController(t *testing.T) {
	all := RequiredDelegatedControllers // "cpu memory pids"
	enable, prepared, _ := planDelegation(all, "")
	if prepared {
		t.Fatal("a base with nothing enabled must not be reported prepared")
	}
	for _, c := range strings.Fields(all) {
		want := "+" + c
		found := false
		for _, e := range enable {
			if e == want {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("planDelegation does not enable %q for a base that delegates %q; enable = %v", want, all, enable)
		}
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
