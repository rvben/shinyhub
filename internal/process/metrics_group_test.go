package process_test

import (
	"os"
	"os/exec"
	"syscall"
	"testing"
	"time"

	gops "github.com/shirou/gopsutil/v4/process"

	"github.com/rvben/shinyhub/internal/process"
)

// TestHelperBusyForever burns one core until it is killed. The process-group
// tests need work that outlasts the whole sampling window, so that a low reading
// can only mean the sampler missed the process and never that the test raced the
// child's own schedule.
func TestHelperBusyForever(t *testing.T) {
	if os.Getenv("SHINYHUB_CPU_HELPER_BUSY") != "1" {
		t.Skip("helper process, spawned by the process-group sampling tests")
	}
	deadline := time.Now().Add(90 * time.Second)
	for time.Now().Before(deadline) {
	}
}

// TestHelperIdleParentBusyChild does no work itself and forks a child that does
// all of it. That is the shape of every native Python app: `uv run --frozen
// --no-sync shiny run app.py` does not exec the interpreter, it spawns it and
// waits, so the PID the runtime records is a launcher that is idle by
// construction.
func TestHelperIdleParentBusyChild(t *testing.T) {
	if os.Getenv("SHINYHUB_CPU_HELPER_PARENT") != "1" {
		t.Skip("helper process, spawned by the process-group sampling tests")
	}
	child := exec.Command(os.Args[0], "-test.run=^TestHelperBusyForever$", "-test.timeout=120s")
	child.Env = append(os.Environ(), "SHINYHUB_CPU_HELPER_BUSY=1")
	if err := child.Start(); err != nil {
		t.Fatalf("start busy child: %v", err)
	}
	defer func() {
		_ = child.Process.Kill()
		_, _ = child.Process.Wait()
	}()
	time.Sleep(90 * time.Second)
}

// startIdleParentBusyChild spawns the helper pair in its own process group,
// exactly as NativeRuntime.Start does, and returns the group leader's PID. That
// Setpgid is what makes the leader's PID equal to the group id, which is what
// lets the sampler treat "this PGID" as "this replica's processes".
func startIdleParentBusyChild(t *testing.T) int {
	t.Helper()

	cmd := exec.Command(os.Args[0], "-test.run=^TestHelperIdleParentBusyChild$", "-test.timeout=120s")
	cmd.Env = append(os.Environ(), "SHINYHUB_CPU_HELPER_PARENT=1")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		t.Fatalf("start parent helper: %v", err)
	}
	pid := cmd.Process.Pid
	t.Cleanup(func() {
		// Signal the group rather than the leader. Killing only the leader orphans
		// the busy child, which would then spin on a core for the rest of the run.
		_ = syscall.Kill(-pid, syscall.SIGKILL)
		_, _ = cmd.Process.Wait()
	})
	waitForGroupMember(t, pid)
	return pid
}

// waitForGroupMember blocks until the leader's group holds more than the leader,
// so no test samples the window before the child has been forked.
func waitForGroupMember(t *testing.T, pgid int) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		if countGroupMembers(t, pgid) > 1 {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("the child of %d never appeared in its process group", pgid)
}

func countGroupMembers(t *testing.T, pgid int) int {
	t.Helper()
	pids, err := gops.Pids()
	if err != nil {
		t.Fatalf("list pids: %v", err)
	}
	n := 0
	for _, p := range pids {
		if g, err := syscall.Getpgid(int(p)); err == nil && g == pgid {
			n++
		}
	}
	return n
}

// rootOnlyCPU measures the group leader by itself over d, which is what the
// sampler used to report for the whole app. Every group test uses it as a
// negative control: without it, a passing group assertion could just as well be
// measuring a root that happened to be busy.
func rootOnlyCPU(t *testing.T, pid int, d time.Duration) float64 {
	t.Helper()
	root, err := gops.NewProcess(int32(pid))
	if err != nil {
		t.Fatalf("open root process %d: %v", pid, err)
	}
	if _, err := root.Percent(0); err != nil {
		t.Fatalf("prime root: %v", err)
	}
	time.Sleep(d)
	pct, err := root.Percent(0)
	if err != nil {
		t.Fatalf("root percent: %v", err)
	}
	return pct
}

// TestGopsutilSamplerCountsTheWholeProcessGroup is the regression test for what a
// native app actually looks like on the host. The runtime records the launcher's
// PID, the launcher forks the real interpreter, and sampling the launcher alone
// reports an app burning a full core as doing nothing, with metrics_available
// true so nothing downstream can tell the number is meaningless.
func TestGopsutilSamplerCountsTheWholeProcessGroup(t *testing.T) {
	pgid := startIdleParentBusyChild(t)

	var s process.GopsutilSampler
	handle := process.RunHandle{PID: pgid}

	if _, err := s.Sample(handle); err != nil {
		t.Fatalf("priming sample: %v", err)
	}
	time.Sleep(2 * time.Second)

	got, err := s.Sample(handle)
	if err != nil {
		t.Fatalf("sample: %v", err)
	}
	if got.CPUPercent == nil {
		t.Fatal("sample reported no rate, but the previous sample primed this group")
	}

	rootOnly := rootOnlyCPU(t, pgid, 1*time.Second)
	if rootOnly > 20 {
		t.Fatalf("negative control failed: the launcher itself read %.1f%%, so this "+
			"test cannot show that the group total came from its child", rootOnly)
	}

	if *got.CPUPercent < 50 {
		t.Errorf("group cpu = %.1f%%, want at least 50%%: this group's child spins on a "+
			"core for the whole window while its root reads %.1f%%, so a sampler that "+
			"stops at the root reports a busy app as idle", *got.CPUPercent, rootOnly)
	}
}

// TestGopsutilSamplerSumsGroupRSS covers the other half of the same defect. A
// launcher's own footprint is both tiny and constant, so reading it as the app's
// memory puts every Python app at the same few-megabyte floor and makes the
// memory-limit warnings on the dashboard unreachable.
func TestGopsutilSamplerSumsGroupRSS(t *testing.T) {
	pgid := startIdleParentBusyChild(t)

	var s process.GopsutilSampler
	group, err := s.Sample(process.RunHandle{PID: pgid})
	if err != nil {
		t.Fatalf("sample: %v", err)
	}

	root, err := gops.NewProcess(int32(pgid))
	if err != nil {
		t.Fatalf("open root process: %v", err)
	}
	mem, err := root.MemoryInfo()
	if err != nil {
		t.Fatalf("root memory: %v", err)
	}
	rootOnly := int64(mem.RSS)

	if rootOnly <= 0 {
		t.Fatalf("negative control failed: the root reported %d bytes, so there is no "+
			"baseline to show the group total exceeding", rootOnly)
	}
	if group.RSSBytes <= rootOnly {
		t.Errorf("group rss = %d, want more than the root's own %d: the group holds a "+
			"second process whose memory belongs to this app", group.RSSBytes, rootOnly)
	}
}
