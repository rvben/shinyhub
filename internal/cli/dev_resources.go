package cli

import (
	"context"
	"fmt"
	"math"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/charmbracelet/x/ansi"
	"github.com/rvben/shinyhub/internal/localrun"
	"github.com/rvben/shinyhub/internal/process"
	"github.com/shirou/gopsutil/v4/cpu"
	"github.com/shirou/gopsutil/v4/disk"
	"github.com/shirou/gopsutil/v4/mem"
)

const devResourceInterval = 2 * time.Second

type devProcessKey struct {
	app     string
	attempt int64
}
type devProcessReading struct {
	pid   int
	at    time.Time
	stats process.Stats
	err   error
}
type devResourceMsg struct {
	at        time.Time
	hostCPU   *float64
	memory    *mem.VirtualMemoryStat
	disk      *disk.UsageStat
	diskPath  string
	processes map[devProcessKey]devProcessReading
}

// Only the TUI starts this observer. Plain output and NDJSON do no OS polling.
// Sampling runs outside the UI loop and never queues overlapping polls.
type devResourceMonitor struct {
	mu          sync.Mutex
	roots       map[devProcessKey]int
	sampler     process.GopsutilSampler
	previousCPU *cpu.TimesStat
	diskPath    string
}

func (r *devResourceMonitor) observe(e localrun.Event) {
	if e.Type != "process" {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.roots == nil {
		r.roots = make(map[devProcessKey]int)
	}
	key := devProcessKey{e.App, e.Attempt}
	if e.Phase == "started" {
		r.roots[key] = e.PID
	}
	if e.Phase == "exited" && r.roots[key] == e.PID {
		delete(r.roots, key)
	}
}
func devHostCPU(previous, current cpu.TimesStat) *float64 {
	total := func(c cpu.TimesStat) float64 {
		return c.User + c.Nice + c.System + c.Idle + c.Iowait + c.Irq + c.Softirq + c.Steal
	}
	delta := total(current) - total(previous)
	idle := current.Idle + current.Iowait - previous.Idle - previous.Iowait
	if delta <= 0 || idle < 0 || idle > delta {
		return nil
	}
	value := 100 * (delta - idle) / delta
	if math.IsNaN(value) || math.IsInf(value, 0) {
		return nil
	}
	return &value
}
func (r *devResourceMonitor) sample(ctx context.Context) devResourceMsg {
	out := devResourceMsg{at: time.Now(), diskPath: r.diskPath, processes: make(map[devProcessKey]devProcessReading)}
	probeCtx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	if times, err := cpu.TimesWithContext(probeCtx, false); err == nil && len(times) > 0 {
		if r.previousCPU != nil {
			out.hostCPU = devHostCPU(*r.previousCPU, times[0])
		}
		r.previousCPU = &times[0]
	} else {
		r.previousCPU = nil
	}
	out.memory, _ = mem.VirtualMemoryWithContext(probeCtx)
	out.disk, _ = disk.UsageWithContext(probeCtx, r.diskPath)
	r.mu.Lock()
	roots := make(map[devProcessKey]int, len(r.roots))
	for key, pid := range r.roots {
		roots[key] = pid
	}
	r.mu.Unlock()
	alive := make(map[int32]struct{}, len(roots))
	for key, pid := range roots {
		if ctx.Err() != nil {
			break
		}
		alive[int32(pid)] = struct{}{}
		stats, err := r.sampler.SampleBasic(process.RunHandle{PID: pid})
		out.processes[key] = devProcessReading{pid: pid, at: time.Now(), stats: stats, err: err}
	}
	r.sampler.Purge(alive)
	return out
}
func (r *devResourceMonitor) run(ctx context.Context, updates chan devResourceMsg) {
	tick := time.NewTicker(devResourceInterval)
	defer tick.Stop()
	for {
		sample := r.sample(ctx)
		if ctx.Err() != nil {
			return
		}
		// Retain the newest snapshot without ever blocking logs or app execution.
		select {
		case updates <- sample:
		default:
			select {
			case <-updates:
			default:
			}
			select {
			case updates <- sample:
			default:
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
	}
}
func devResourceBytes(bytes uint64) string {
	units := []string{"B", "KiB", "MiB", "GiB", "TiB", "PiB", "EiB"}
	value, index := float64(bytes), 0
	for value >= 1024 && index < len(units)-1 {
		value /= 1024
		index++
	}
	if index == 0 {
		return fmt.Sprintf("%dB", bytes)
	}
	return fmt.Sprintf("%.1f%s", value, units[index])
}
func devCPUText(value *float64) string {
	if value == nil {
		return "—"
	}
	return fmt.Sprintf("%.1f%%", *value)
}
func devSampleAge(at time.Time, now time.Time) string {
	if at.IsZero() {
		return "waiting for sample"
	}
	age := max(time.Duration(0), now.Sub(at))
	if age > 3*devResourceInterval {
		return fmt.Sprintf("STALE · %ds old", int(age.Seconds()))
	}
	return fmt.Sprintf("%ds ago", int(age.Seconds()))
}
func (m *devModel) hostResourceLines(width int) []string {
	r := m.resources
	memory, diskFree := "—", "—"
	if r.memory != nil {
		memory = devResourceBytes(r.memory.Available)
	}
	if r.disk != nil {
		diskFree = devResourceBytes(r.disk.Free)
	}
	cpuText := m.style.dim("Host CPU ") + devStrong(m.style, devCPUText(r.hostCPU))
	memoryText := m.style.dim("RAM avail ") + devStrong(m.style, memory)
	diskText := m.style.dim("Disk free ") + devStrong(m.style, diskFree)
	age := m.style.dim(devSampleAge(r.at, m.clock))
	if width < 80 {
		return []string{cpuText + " · " + memoryText, diskText + " · " + age}
	}
	return []string{cpuText + "   ·   " + memoryText + "   ·   " + diskText + "   ·   " + age}
}
func (m *devModel) processResourceText(app devAppView, attempt int64) string {
	pid := app.processes[attempt]
	if pid == 0 {
		return "not running"
	}
	reading, ok := m.resources.processes[devProcessKey{app.slug, attempt}]
	if !ok || reading.pid != pid {
		return "CPU — · RSS — · sampling"
	}
	if reading.err != nil {
		return "CPU — · RSS — · unavailable"
	}
	text := "CPU " + devCPUText(reading.stats.CPUPercent) + " · RSS " + devResourceBytes(uint64(max(int64(0), reading.stats.RSSBytes)))
	if m.clock.Sub(reading.at) > 3*devResourceInterval {
		text += " · STALE"
	}
	return text
}
func (m *devModel) resourceDetailLines(width int) []string {
	s, r := m.style, m.resources
	section := func(name string) string { return devStrong(s, name) }
	metric := func(label, value, detail string) string {
		if width < 28 {
			return s.dim(label+" ") + devStrong(s, value)
		}
		labelWidth, valueWidth := 10, 11
		if width < 55 {
			labelWidth, valueWidth = 9, max(11, width-13)
			detail = ""
		}
		row := "  " + s.dim(devFit(label, labelWidth)) + devStrong(s, devRight(value, valueWidth))
		if detail != "" {
			row += "  " + s.dim(detail)
		}
		return row
	}
	age := devSampleAge(r.at, m.clock)
	lines := []string{section("Local host") + s.dim("  ·  "+age), ""}
	lines = append(lines, metric("CPU", devCPUText(r.hostCPU), "all cores"))
	available, totalRAM, free, totalDisk := "—", "", "—", ""
	if r.memory != nil {
		available = devResourceBytes(r.memory.Available)
		totalRAM = devResourceBytes(r.memory.Total)
	}
	if r.disk != nil {
		free = devResourceBytes(r.disk.Free)
		totalDisk = devResourceBytes(r.disk.Total)
	}
	ramLabel, diskLabel, ramDetail, diskDetail := "RAM", "Disk", "", ""
	if totalRAM != "" {
		ramDetail = "available / " + totalRAM
	}
	if totalDisk != "" {
		diskDetail = "free / " + totalDisk
	}
	if width < 55 {
		ramLabel, diskLabel = "RAM avail", "Disk free"
		if totalRAM != "" {
			available += " / " + totalRAM
		}
		if totalDisk != "" {
			free += " / " + totalDisk
		}
	}
	lines = append(lines, metric(ramLabel, available, ramDetail), metric(diskLabel, free, diskDetail), "", section("Applications"), "")
	roleWidth := min(24, max(11, width-25))
	if width >= 38 {
		lines = append(lines, s.dim("  "+devFit("Process group", roleWidth)+" "+devRight("CPU", 8)+" "+devRight("RSS", 10)))
	}
	for i, app := range m.apps {
		if m.selected >= 0 && i != m.selected {
			continue
		}
		lines = append(lines, devStrong(s, devSafeText(app.slug)))
		processRow := func(role string, attempt int64) {
			cpuText, rss, note := "—", "—", "sampling"
			reading, ok := r.processes[devProcessKey{app.slug, attempt}]
			pid := app.processes[attempt]
			if pid == 0 {
				note = "not running"
			} else if ok && reading.pid == pid {
				if reading.err != nil {
					note = "unavailable"
				} else {
					cpuText = devCPUText(reading.stats.CPUPercent)
					rss = devResourceBytes(uint64(max(int64(0), reading.stats.RSSBytes)))
					note = ""
					if m.clock.Sub(reading.at) > 3*devResourceInterval {
						note = "STALE"
					}
				}
			}
			label := fmt.Sprintf("%s #%d", role, attempt)
			if width >= 38 {
				label = devFit(label, roleWidth)
			}
			switch role {
			case "Serving":
				label = s.green(label)
			case "Startup":
				label = s.yellow(label)
			default:
				label = s.dim(label)
			}
			if width < 38 {
				lines = append(lines, label, s.dim("CPU ")+devStrong(s, cpuText)+s.dim(" · RSS ")+devStrong(s, rss))
			} else {
				lines = append(lines, "  "+label+" "+devStrong(s, devRight(cpuText, 8))+" "+devStrong(s, devRight(rss, 10)))
			}
			if note != "" {
				lines = append(lines, s.dim("  "+note))
			}
		}
		if app.servingAttempt > 0 && !app.stopped {
			processRow("Serving", app.servingAttempt)
		}
		attempts := make([]int64, 0, len(app.processes))
		for attempt := range app.processes {
			if attempt != app.servingAttempt {
				attempts = append(attempts, attempt)
			}
		}
		sort.Slice(attempts, func(i, j int) bool { return attempts[i] < attempts[j] })
		for _, attempt := range attempts {
			role := "Startup"
			if attempt < app.servingAttempt {
				role = "Retiring"
			}
			processRow(role, attempt)
		}
		if len(app.processes) == 0 {
			lines = append(lines, s.dim("  No running app process"))
		}
	}
	lines = append(lines, "", s.dim("Disk filesystem"), s.dim(devSafeText(r.diskPath)), "", s.dim("Host CPU: 100% = all cores · app CPU: 100% = one core."), s.dim("RSS includes shared pages; it is not additive RAM."), s.dim("Dependency build processes are not attributed."), s.dim("— unavailable or warming up · sampled every 2s."))
	return lines
}

func (m *devModel) resourceDisplayLines(width int) []string {
	var rows []string
	for _, line := range m.resourceDetailLines(width) {
		rows = append(rows, strings.Split(ansi.Hardwrap(line, width, true), "\n")...)
	}
	return rows
}
