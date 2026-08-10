package process_test

import (
	"os"
	"os/exec"
	"testing"
	"time"

	"github.com/rvben/shinyhub/internal/process"
)

// TestHelperBusyThenIdle is not a test of its own. It is the child process the
// sampling tests drive: it burns one core for a fixed window and then goes
// idle. That shape is the only one that tells a rate apart from a lifetime
// average, because the two agree on a process that has done exactly one thing
// since it started. It exits immediately unless a parent asks for it by name.
func TestHelperBusyThenIdle(t *testing.T) {
	if os.Getenv("SHINYHUB_CPU_HELPER") != "1" {
		t.Skip("helper process, spawned by the CPU sampling tests")
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
	}
	time.Sleep(30 * time.Second)
}

// startBusyThenIdleHelper spawns the helper and returns its PID. The helper is
// busy for its first two seconds and idle afterwards; callers time their
// samples against the returned start instant.
func startBusyThenIdleHelper(t *testing.T) (pid int, start time.Time) {
	t.Helper()

	cmd := exec.Command(os.Args[0], "-test.run=^TestHelperBusyThenIdle$", "-test.timeout=60s")
	cmd.Env = append(os.Environ(), "SHINYHUB_CPU_HELPER=1")
	if err := cmd.Start(); err != nil {
		t.Fatalf("start helper: %v", err)
	}
	start = time.Now()
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
	})
	return cmd.Process.Pid, start
}

// sleepUntil blocks until d has elapsed since start, so each sample lands at a
// known point in the helper's busy/idle timeline regardless of how long the
// preceding sample took.
func sleepUntil(start time.Time, d time.Duration) {
	if rest := time.Until(start.Add(d)); rest > 0 {
		time.Sleep(rest)
	}
}

// TestGopsutilSamplerReportsARateNotALifetimeAverage is the regression test for
// the metric itself. A process that stops working must stop reporting CPU: an
// operator watching this number is asking "what is it doing now", and a
// lifetime average answers "what has it done since it started", which for a
// long-lived app process converges to a small number that never moves.
//
// The middle sample is a positive control. It fails loudly if the host is too
// loaded to give the helper a core, rather than letting the real assertion pass
// for the wrong reason.
func TestGopsutilSamplerReportsARateNotALifetimeAverage(t *testing.T) {
	pid, start := startBusyThenIdleHelper(t)

	var s process.GopsutilSampler
	handle := process.RunHandle{PID: pid}

	sleepUntil(start, 1*time.Second)
	if _, err := s.Sample(handle); err != nil {
		t.Fatalf("priming sample: %v", err)
	}

	sleepUntil(start, 2*time.Second)
	busy, err := s.Sample(handle)
	if err != nil {
		t.Fatalf("busy sample: %v", err)
	}
	if busy.CPUPercent == nil {
		t.Fatalf("busy sample reported no rate, but the previous sample primed this PID")
	}
	if *busy.CPUPercent < 50 {
		t.Fatalf("positive control failed: a process spinning on a core for the "+
			"whole sample window read %.1f%%, so this host is too loaded for the "+
			"rest of this test to mean anything", *busy.CPUPercent)
	}

	sleepUntil(start, 4*time.Second)
	idle, err := s.Sample(handle)
	if err != nil {
		t.Fatalf("idle sample: %v", err)
	}
	if idle.CPUPercent == nil {
		t.Fatalf("idle sample reported no rate, but the previous sample primed this PID")
	}
	if *idle.CPUPercent > 20 {
		t.Errorf("a process idle for the whole sample window read %.1f%%, want under 20%%; "+
			"a lifetime average never decays, so it still reports the burst that ended "+
			"two seconds ago", *idle.CPUPercent)
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
