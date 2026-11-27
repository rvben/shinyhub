package process_test

import (
	"bufio"
	"os"
	"os/exec"
	"syscall"
	"testing"
	"time"

	gops "github.com/shirou/gopsutil/v4/process"

	"github.com/rvben/shinyhub/internal/process"
)

// helperBusyCPU is how much CPU time the busy-then-idle helper burns before it
// goes idle. It is an amount of work rather than a stretch of wall time, so the
// helper does the same work on a loaded host as on an idle one; only the time
// it takes changes.
const helperBusyCPU = time.Second

// helperLifetime is how long a helper process stays alive unless the test kills
// it first, which every test does in its cleanup. It only bounds the leak if
// the test binary itself is killed. It must outlast any stall the tests can
// otherwise survive: a helper that exits mid-test frees its PID for reuse, and
// the kernel's CPU time for that PID then belongs to some other process.
const helperLifetime = 10 * time.Minute

// helperTimeout is the -test.timeout flag a helper runs under, a little past
// its lifetime so the helper ends by finishing rather than by panicking.
var helperTimeout = "-test.timeout=" + (helperLifetime + time.Minute).String()

// TestHelperBusyThenIdle is not a test of its own. It is the child process the
// sampling tests drive: it burns helperBusyCPU of CPU time, says so on stdout
// and then goes idle. That shape is the only one that tells a rate apart from a
// lifetime average, because the two agree on a process that has done exactly
// one thing since it started. It exits immediately unless a parent asks for it
// by name.
func TestHelperBusyThenIdle(t *testing.T) {
	if os.Getenv("SHINYHUB_CPU_HELPER") != "1" {
		t.Skip("helper process, spawned by the CPU sampling tests")
	}
	for ownCPU(t) < helperBusyCPU {
		for i := 0; i < 1_000_000; i++ {
		}
	}
	os.Stdout.WriteString("idle\n")
	time.Sleep(helperLifetime)
}

func ownCPU(t *testing.T) time.Duration {
	var ru syscall.Rusage
	if err := syscall.Getrusage(syscall.RUSAGE_SELF, &ru); err != nil {
		t.Fatalf("getrusage: %v", err)
	}
	return time.Duration(ru.Utime.Nano() + ru.Stime.Nano())
}

// startBusyThenIdleHelper spawns the helper and returns its PID and a channel
// that is closed once the helper has done its work and gone idle.
func startBusyThenIdleHelper(t *testing.T) (pid int, idle <-chan struct{}) {
	t.Helper()

	cmd := exec.Command(os.Args[0], "-test.run=^TestHelperBusyThenIdle$", helperTimeout)
	cmd.Env = append(os.Environ(), "SHINYHUB_CPU_HELPER=1")
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatalf("helper stdout: %v", err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatalf("start helper: %v", err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
	})
	done := make(chan struct{})
	go func() {
		scanner := bufio.NewScanner(stdout)
		for scanner.Scan() {
			if scanner.Text() == "idle" {
				close(done)
				break
			}
		}
		for scanner.Scan() {
		}
	}()
	return cmd.Process.Pid, done
}

// cpuTime is the CPU time the given processes have used so far, read straight
// from the kernel. It is the oracle the sampler's rates are checked against:
// the change over a window divided by the window is the rate the sampler should
// report, whatever share of a core the host gave the process.
func cpuTime(t *testing.T, pids ...int) time.Duration {
	t.Helper()
	var total time.Duration
	for _, pid := range pids {
		p, err := gops.NewProcess(int32(pid))
		if err != nil {
			t.Fatalf("open process %d: %v", pid, err)
		}
		times, err := p.Times()
		if err != nil {
			t.Fatalf("cpu times of %d: %v", pid, err)
		}
		total += time.Duration((times.User + times.System) * float64(time.Second))
	}
	return total
}

func percentOf(cpu, wall time.Duration) float64 {
	return 100 * cpu.Seconds() / wall.Seconds()
}

// TestGopsutilSamplerReportsARateNotALifetimeAverage is the regression test for
// the metric itself. A process that stops working must stop reporting CPU: an
// operator watching this number is asking "what is it doing now", and a
// lifetime average answers "what has it done since it started", which for a
// long-lived app process converges to a small number that never moves.
//
// The busy sample is a positive control: it must match the rate the kernel's
// own CPU time gives for the same window, which holds at any host load. The
// idle sample is then checked against the helper's lifetime average, which a
// rate falls far below once the work has stopped and an average does not.
func TestGopsutilSamplerReportsARateNotALifetimeAverage(t *testing.T) {
	pid, idle := startBusyThenIdleHelper(t)

	var s process.GopsutilSampler
	handle := process.RunHandle{PID: pid}

	if _, err := s.Sample(handle); err != nil {
		t.Fatalf("priming sample: %v", err)
	}
	primedAt, primedCPU := time.Now(), cpuTime(t, pid)

	select {
	case <-idle:
	case <-time.After(60 * time.Second):
		t.Fatalf("the helper did not burn %s of CPU within a minute", helperBusyCPU)
	}
	busy, err := s.Sample(handle)
	if err != nil {
		t.Fatalf("busy sample: %v", err)
	}
	busyCPU := cpuTime(t, pid) - primedCPU
	if busyCPU < helperBusyCPU/2 {
		t.Fatalf("the helper used only %s of CPU after the priming sample, so the busy "+
			"window cannot tell a working sampler from one that always reads 0", busyCPU)
	}
	busyWant := percentOf(busyCPU, time.Since(primedAt))
	if busy.CPUPercent == nil {
		t.Fatalf("busy sample reported no rate, but the previous sample primed this PID")
	}
	if got := *busy.CPUPercent; got < 0.6*busyWant || got > 1.4*busyWant+2 {
		t.Fatalf("positive control failed: the busy window read %.1f%%, but the "+
			"kernel's CPU time for the same window gives %.1f%%", got, busyWant)
	}

	time.Sleep(time.Second)
	idleSample, err := s.Sample(handle)
	if err != nil {
		t.Fatalf("idle sample: %v", err)
	}
	if idleSample.CPUPercent == nil {
		t.Fatalf("idle sample reported no rate, but the previous sample primed this PID")
	}
	p, err := gops.NewProcess(int32(pid))
	if err != nil {
		t.Fatalf("open helper: %v", err)
	}
	created, err := p.CreateTime()
	if err != nil {
		t.Fatalf("helper create time: %v", err)
	}
	lifetime := percentOf(cpuTime(t, pid), time.Since(time.UnixMilli(created)))
	if got := *idleSample.CPUPercent; got > lifetime/4 {
		t.Errorf("a process idle for the whole sample window read %.1f%%, against a "+
			"lifetime average of %.1f%%: a lifetime average never decays, so it "+
			"still reports the work that ended a second ago", got, lifetime)
	}
}

// TestGopsutilSamplerFirstSampleHasNoRate covers the cost of measuring a rate:
// the first sample of a process has nothing to measure against. Reporting that
// as 0 would be a lie told at a specific and unlucky moment, since every deploy,
// restart and scale-up produces a fresh PID whose first poll would read as an
// app doing nothing.
//
// RSS is asserted alongside it because it needs no baseline. The absent rate has
// to be the narrow claim "no rate yet", not a sample that failed wholesale.
func TestGopsutilSamplerFirstSampleHasNoRate(t *testing.T) {
	pid, _ := startBusyThenIdleHelper(t)

	var s process.GopsutilSampler
	handle := process.RunHandle{PID: pid}

	first, err := s.Sample(handle)
	if err != nil {
		t.Fatalf("first sample: %v", err)
	}
	if first.CPUPercent != nil {
		t.Errorf("first sample cpu = %.1f, want nil: there is no previous reading to "+
			"compute a rate against", *first.CPUPercent)
	}
	if first.RSSBytes <= 0 {
		t.Errorf("first sample rss = %d, want a real measurement: memory needs no baseline",
			first.RSSBytes)
	}

	second, err := s.Sample(handle)
	if err != nil {
		t.Fatalf("second sample: %v", err)
	}
	if second.CPUPercent == nil {
		t.Error("second sample cpu = nil, want a rate: the first sample established the baseline")
	}
}

// TestGopsutilSamplerPurgeDropsTheBaseline pins the documented cost of Purge.
// The sampler purges PIDs it no longer tracks, and a purged-then-resampled PID
// is in the same position as a brand new one, so it must say so rather than
// resume with a rate measured against a reading from before the gap.
func TestGopsutilSamplerPurgeDropsTheBaseline(t *testing.T) {
	pid, _ := startBusyThenIdleHelper(t)

	var s process.GopsutilSampler
	handle := process.RunHandle{PID: pid}

	if _, err := s.Sample(handle); err != nil {
		t.Fatalf("priming sample: %v", err)
	}
	primed, err := s.Sample(handle)
	if err != nil {
		t.Fatalf("second sample: %v", err)
	}
	if primed.CPUPercent == nil {
		t.Fatal("second sample reported no rate, so this test cannot show Purge changing anything")
	}

	s.Purge(nil)

	after, err := s.Sample(handle)
	if err != nil {
		t.Fatalf("post-purge sample: %v", err)
	}
	if after.CPUPercent != nil {
		t.Errorf("post-purge cpu = %.1f, want nil: purging discarded the baseline",
			*after.CPUPercent)
	}
}
