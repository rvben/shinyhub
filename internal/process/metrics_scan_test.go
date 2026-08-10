package process

import (
	"errors"
	"os"
	"os/exec"
	"sort"
	"testing"
	"time"
)

func sortedPIDs(in []int32) []int32 {
	out := append([]int32(nil), in...)
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

func samePIDs(got, want []int32) bool {
	g, w := sortedPIDs(got), sortedPIDs(want)
	if len(g) != len(w) {
		return false
	}
	for i := range g {
		if g[i] != w[i] {
			return false
		}
	}
	return true
}

// startSleeper returns the PID of a real, live, cheap process. The scan-level
// tests need PIDs that gopsutil can actually open; what they run does not matter.
func startSleeper(t *testing.T) int32 {
	t.Helper()
	cmd := exec.Command("sleep", "30")
	if err := cmd.Start(); err != nil {
		t.Fatalf("start sleeper: %v", err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
	})
	return int32(cmd.Process.Pid)
}

// A replica is billed for its own process group and never for anyone else's.
// The root PID doubles as the group id only because NativeRuntime starts each
// replica with Setpgid; a PID that is not a group leader has a PGID belonging to
// some unrelated process, and following it would attribute a stranger's CPU to
// this app.
func TestSamplerMembersOnlyAggregatesTheRootsOwnGroup(t *testing.T) {
	g := &GopsutilSampler{scan: map[int32][]int32{
		100: {100, 101, 102},
		200: {200, 201},
	}}

	if got := g.members(100); !samePIDs(got, []int32{100, 101, 102}) {
		t.Errorf("members(100) = %v, want the whole group it leads", got)
	}
	if got := g.members(201); !samePIDs(got, []int32{201}) {
		t.Errorf("members(201) = %v, want only itself: 201 leads no group, so its own "+
			"PGID (200) is another process's group and none of it is this replica's", got)
	}
	if got := g.members(999); !samePIDs(got, []int32{999}) {
		t.Errorf("members(999) = %v, want only itself for a PID absent from the scan", got)
	}
}

// The root is always sampled even if the scan did not list it, so a membership
// snapshot that raced the root's own appearance still measures the process the
// runtime actually recorded.
func TestSamplerMembersAlwaysIncludeTheRootExactlyOnce(t *testing.T) {
	g := &GopsutilSampler{scan: map[int32][]int32{
		300: {301},
		400: {400, 400},
	}}

	if got := g.members(300); !samePIDs(got, []int32{300, 301}) {
		t.Errorf("members(300) = %v, want the root included even though the scan omitted it", got)
	}
	if got := g.members(400); !samePIDs(got, []int32{400}) {
		t.Errorf("members(400) = %v, want the root listed once; sampling it twice would "+
			"double its CPU and RSS in the total", got)
	}
}

// One tick fans out across every replica, and each of those calls must not
// re-enumerate the host's processes. The scan is the only part of a sample whose
// cost scales with the whole machine rather than with the app.
func TestSamplerScansOncePerTTLWindow(t *testing.T) {
	self := int32(os.Getpid())
	now := time.Now()
	scans := 0

	g := &GopsutilSampler{
		nowFn: func() time.Time { return now },
		scanFn: func() (map[int32][]int32, error) {
			scans++
			return map[int32][]int32{self: {self}}, nil
		},
	}

	for i := 0; i < 5; i++ {
		if _, err := g.Sample(RunHandle{PID: int(self)}); err != nil {
			t.Fatalf("sample %d: %v", i, err)
		}
	}
	if scans != 1 {
		t.Errorf("scans = %d after 5 samples in one window, want 1: a per-replica scan "+
			"makes a tick cost replicas x host processes", scans)
	}

	now = now.Add(groupScanTTL)
	if _, err := g.Sample(RunHandle{PID: int(self)}); err != nil {
		t.Fatalf("sample after ttl: %v", err)
	}
	if scans != 2 {
		t.Errorf("scans = %d after the TTL elapsed, want 2: membership must be re-read "+
			"or a replica's new workers are never counted", scans)
	}
}

// Losing the ability to enumerate processes must not silently shrink an app to
// its launcher. Falling back to the root alone would report a busy app as idle
// and there would be nothing in the output to say the number got worse.
func TestSamplerKeepsLastMembershipWhenEnumerationFails(t *testing.T) {
	self := int32(os.Getpid())
	child := startSleeper(t)
	now := time.Now()
	fail := false

	g := &GopsutilSampler{
		nowFn: func() time.Time { return now },
		scanFn: func() (map[int32][]int32, error) {
			if fail {
				return nil, errors.New("cannot read process table")
			}
			return map[int32][]int32{self: {self, child}}, nil
		},
	}

	if _, err := g.Sample(RunHandle{PID: int(self)}); err != nil {
		t.Fatalf("first sample: %v", err)
	}

	fail = true
	now = now.Add(2 * groupScanTTL)
	if _, err := g.Sample(RunHandle{PID: int(self)}); err != nil {
		t.Fatalf("sample after a failed scan: %v, want the previous membership reused", err)
	}
	if got := g.members(self); !samePIDs(got, []int32{self, child}) {
		t.Errorf("members after a failed scan = %v, want the last known group %v",
			got, []int32{self, child})
	}
}

// With no previous membership there is nothing to stand on, so the failure is
// reported rather than dressed up as a replica that happens to be one lonely
// launcher process.
func TestSamplerReportsAScanFailureWithNoPriorMembership(t *testing.T) {
	self := int32(os.Getpid())
	g := &GopsutilSampler{
		nowFn:  time.Now,
		scanFn: func() (map[int32][]int32, error) { return nil, errors.New("cannot read process table") },
	}

	if _, err := g.Sample(RunHandle{PID: int(self)}); err == nil {
		t.Error("sample succeeded with no membership at all, want an error: reporting the " +
			"root's own usage here would claim a measurement the sampler cannot make")
	}
}

// A member that joins after the group was primed must not blank out the rate for
// everyone else. An app that forks a worker per session would otherwise report
// no CPU at all for exactly as long as it kept doing so.
func TestSamplerRateSurvivesANewGroupMember(t *testing.T) {
	self := int32(os.Getpid())
	first := startSleeper(t)
	now := time.Now()
	members := []int32{self, first}

	g := &GopsutilSampler{
		nowFn:  func() time.Time { return now },
		scanFn: func() (map[int32][]int32, error) { return map[int32][]int32{self: members}, nil },
	}

	if _, err := g.Sample(RunHandle{PID: int(self)}); err != nil {
		t.Fatalf("priming sample: %v", err)
	}
	now = now.Add(2 * groupScanTTL)
	primed, err := g.Sample(RunHandle{PID: int(self)})
	if err != nil {
		t.Fatalf("second sample: %v", err)
	}
	if primed.CPUPercent == nil {
		t.Fatal("second sample reported no rate, so this test cannot show a new member changing anything")
	}

	members = append(members, startSleeper(t))
	now = now.Add(2 * groupScanTTL)
	after, err := g.Sample(RunHandle{PID: int(self)})
	if err != nil {
		t.Fatalf("sample after a new member joined: %v", err)
	}
	if after.CPUPercent == nil {
		t.Error("rate went absent because one member was new; the root still has a " +
			"baseline, and an app that forks workers would never report CPU again")
	}
}

// A member that leaves must not keep its handle forever. The collector samples
// only live replicas, so nothing else would ever evict a worker that has exited.
func TestSamplerForgetsMembersThatLeaveTheGroup(t *testing.T) {
	self := int32(os.Getpid())
	leaving := startSleeper(t)
	now := time.Now()
	members := []int32{self, leaving}

	g := &GopsutilSampler{
		nowFn:  func() time.Time { return now },
		scanFn: func() (map[int32][]int32, error) { return map[int32][]int32{self: members}, nil },
	}

	if _, err := g.Sample(RunHandle{PID: int(self)}); err != nil {
		t.Fatalf("first sample: %v", err)
	}
	if got := len(g.groups[self].members); got != 2 {
		t.Fatalf("cached members = %d, want 2 before anything leaves", got)
	}

	members = []int32{self}
	now = now.Add(2 * groupScanTTL)
	if _, err := g.Sample(RunHandle{PID: int(self)}); err != nil {
		t.Fatalf("sample after a member left: %v", err)
	}
	if got := len(g.groups[self].members); got != 1 {
		t.Errorf("cached members = %d after one left, want 1: a replica cycling through "+
			"workers would otherwise hold a handle for every one it ever ran", got)
	}
}
