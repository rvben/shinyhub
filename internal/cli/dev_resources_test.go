package cli

import (
	"context"
	"errors"
	"os/exec"
	"strings"
	"syscall"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/rvben/shinyhub/internal/localrun"
	"github.com/rvben/shinyhub/internal/process"
	"github.com/shirou/gopsutil/v4/cpu"
	"github.com/shirou/gopsutil/v4/disk"
	"github.com/shirou/gopsutil/v4/mem"
	gops "github.com/shirou/gopsutil/v4/process"
)

func TestDevHostCPUUsesIntervalAndRejectsReset(t *testing.T) {
	prev := cpu.TimesStat{User: 10, System: 10, Idle: 80, Guest: 100}
	next := cpu.TimesStat{User: 12, System: 13, Idle: 85, Guest: 200}
	if got := devHostCPU(prev, next); got == nil || *got != 50 {
		t.Fatalf("cpu = %v, want 50%% independent of guest counters", got)
	}
	if devHostCPU(next, prev) != nil || devHostCPU(prev, prev) != nil {
		t.Fatal("reset or empty interval produced a valid CPU reading")
	}
}
func TestDevResourcesKeepServingSeparateAndDiscardExitedReadings(t *testing.T) {
	m := testDevModel()
	m.clock = time.Now()
	for _, e := range []localrun.Event{
		{Type: "process", App: "sales", Phase: "started", Attempt: 1, PID: 100},
		{Type: "phase", App: "sales", Phase: "ready", Attempt: 1, Generation: 1},
		{Type: "process", App: "sales", Phase: "started", Attempt: 2, PID: 200},
		{Type: "phase", App: "sales", Phase: "starting", Attempt: 2},
	} {
		m.apply(e)
	}
	m.resources = devResourceMsg{at: m.clock, processes: map[devProcessKey]devProcessReading{
		{"sales", 1}: {pid: 100, at: m.clock, stats: process.Stats{CPUPercent: process.Float(12), RSSBytes: 1024}},
		{"sales", 2}: {pid: 200, at: m.clock, stats: process.Stats{CPUPercent: process.Float(80), RSSBytes: 2048}},
	}}
	text := strings.Join(m.resourceDetailLines(100), "\n")
	for role, wantCPU := range map[string]string{"Serving #1": "12.0%", "Startup #2": "80.0%"} {
		found := false
		for _, line := range strings.Split(text, "\n") {
			if strings.Contains(line, role) && strings.Contains(line, wantCPU) {
				found = true
			}
		}
		if !found {
			t.Fatalf("missing %s CPU %s: %s", role, wantCPU, text)
		}
	}

	if len(m.sessionLogs.logs) != 2 || m.apps[0].phase != "starting" {
		t.Fatal("process events polluted logs or phase")
	}
	m.apply(localrun.Event{Type: "process", App: "sales", Phase: "exited", Attempt: 2, PID: 200})
	if strings.Contains(strings.Join(m.resourceDetailLines(100), "\n"), "Startup #2") {
		t.Fatal("exited candidate still reported as live")
	}
	// Even an already in-flight snapshot may not revive an exited process.
	if got := m.processResourceText(m.apps[0], 2); got != "not running" {
		t.Fatalf("exited metrics = %s", got)
	}
	m.apply(localrun.Event{Type: "process", App: "sales", Phase: "started", Attempt: 2, PID: 300})
	if got := m.processResourceText(m.apps[0], 2); !strings.Contains(got, "sampling") {
		t.Fatalf("reused attempt showed stale PID: %s", got)
	}
}
func TestDevResourceUnavailableAndStaleReadings(t *testing.T) {
	m := testDevModel()
	m.clock = time.Now()
	m.apply(localrun.Event{Type: "process", App: "sales", Phase: "started", Attempt: 1, PID: 100})
	m.resources = devResourceMsg{at: m.clock.Add(-10 * time.Second), processes: map[devProcessKey]devProcessReading{
		{"sales", 1}: {pid: 100, at: m.clock.Add(-10 * time.Second), stats: process.Stats{RSSBytes: 1024}},
	}}
	if got := m.processResourceText(m.apps[0], 1); !strings.Contains(got, "CPU —") || !strings.Contains(got, "STALE") {
		t.Fatalf("unprimed stale stats: %s", got)
	}
	if got := strings.Join(m.hostResourceLines(100), " "); !strings.Contains(got, "STALE") || !strings.Contains(got, "RAM avail —") {
		t.Fatalf("host unavailable: %s", got)
	}
	reading := m.resources.processes[devProcessKey{"sales", 1}]
	reading.err = errors.New("permission denied")
	m.resources.processes[devProcessKey{"sales", 1}] = reading
	if !strings.Contains(m.processResourceText(m.apps[0], 1), "unavailable") {
		t.Fatal("failed reading appears measured")
	}
}
func TestDevResourceLayoutsAndTogglePreserveLogs(t *testing.T) {
	m := testDevModel()
	m.clock = time.Now()
	m.resources = devResourceMsg{at: m.clock, hostCPU: process.Float(50), memory: &mem.VirtualMemoryStat{Available: 4 << 30}, disk: &disk.UsageStat{Free: 20 << 30}, diskPath: "/workspace"}
	m.apply(localrun.Event{Type: "phase", App: "sales", Phase: "ready", Attempt: 1, Generation: 1})
	m.apply(localrun.Event{Type: "log", App: "sales", Source: "app", Attempt: 1, Message: "runtime needle"})
	m.apply(localrun.Event{Type: "phase", App: "sales", Phase: "failed", Attempt: 2, Message: "candidate failed"})
	m.filter = "needle"
	m.following = false
	m.offset = 3
	for _, details := range []bool{false, true} {
		m.showResources = details
		for _, scope := range []int{0, devAllApps, devShinyHub} {
			m.selected = scope
			for _, size := range [][2]int{{42, 14}, {80, 20}, {100, 24}, {160, 40}} {
				m.width, m.height = size[0], size[1]
				view := m.View().Content
				if len(strings.Split(view, "\n")) > m.height {
					t.Fatalf("height exceeds %d: %s", m.height, view)
				}
				for _, line := range strings.Split(view, "\n") {
					if ansi.StringWidth(line) > m.width {
						t.Fatalf("width exceeds %d: %s", m.width, line)
					}
				}
				if !strings.Contains(view, "q stop") {
					t.Fatalf("quit hidden at %v: %s", size, view)
				}
			}
		}
	}
	m.Update(tea.KeyPressMsg{Code: 'm', Text: "m"})
	if m.showResources || m.filter != "needle" || m.following || m.offset != 3 {
		t.Fatal("returning to logs lost filter or scroll")
	}
}
func TestDevResourceMonitorSamplesRealChildGroupAndStops(t *testing.T) {
	cmd := exec.Command("sh", "-c", "sleep 30 & wait")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL); _ = cmd.Wait() }()
	root, err := gops.NewProcess(int32(cmd.Process.Pid))
	if err != nil {
		t.Fatal(err)
	}
	var children []*gops.Process
	deadline := time.Now().Add(time.Second)
	for len(children) == 0 && time.Now().Before(deadline) {
		children, _ = root.Children()
		if len(children) == 0 {
			time.Sleep(10 * time.Millisecond)
		}
	}
	if len(children) == 0 {
		t.Fatal("test app did not spawn its child")
	}
	rootMemory, err := root.MemoryInfo()
	if err != nil {
		t.Fatal(err)
	}
	childMemory, err := children[0].MemoryInfo()
	if err != nil {
		t.Fatal(err)
	}
	r := &devResourceMonitor{diskPath: t.TempDir()}
	start := localrun.Event{Type: "process", App: "demo", Attempt: 1, PID: cmd.Process.Pid, Phase: "started"}
	r.observe(start)
	sample := r.sample(context.Background())
	reading, ok := sample.processes[devProcessKey{"demo", 1}]
	if !ok || reading.err != nil || reading.stats.RSSBytes <= 0 || reading.stats.CPUPercent != nil {
		t.Fatalf("first real sample: %+v", reading)
	}
	if uint64(reading.stats.RSSBytes) < rootMemory.RSS+childMemory.RSS/2 {
		t.Fatalf("child omitted: group=%d root=%d child=%d", reading.stats.RSSBytes, rootMemory.RSS, childMemory.RSS)
	}
	if reading.stats.PSSBytes != nil || reading.stats.USSBytes != nil {
		t.Fatal("lightweight monitor collected unused attribution")
	}
	if sample.memory == nil || sample.disk == nil {
		t.Fatal("host memory or disk unavailable on test host")
	}
	exit := start
	exit.Phase = "exited"
	r.observe(exit)
	if len(r.sample(context.Background()).processes) != 0 {
		t.Fatal("monitor retained exited app")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	done := make(chan struct{})
	go func() { r.run(ctx, make(chan devResourceMsg, 1)); close(done) }()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("monitor did not stop after cancellation")
	}
}

func TestDevResourceBytesSupportsLargeHostDisks(t *testing.T) {
	if got := devResourceBytes(4 << 40); got != "4.0TiB" {
		t.Fatalf("large disk: %s", got)
	}
	if got := devResourceBytes(^uint64(0)); got != "16.0EiB" {
		t.Fatalf("maximum size: %s", got)
	}
}
