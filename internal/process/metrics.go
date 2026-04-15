package process

import (
	"fmt"
	"math"
	"sync"

	gops "github.com/shirou/gopsutil/v4/process"
)

// Stats holds a point-in-time resource snapshot for one process.
type Stats struct {
	CPUPercent float64
	RSSBytes   int64
}

// Sampler reads CPU and memory stats for a given PID.
// The interface exists so tests can inject a fake without a live process.
type Sampler interface {
	Sample(pid int) (Stats, error)
}

// GopsutilSampler is the production Sampler that reads from the OS via gopsutil.
// It caches process handles by PID so gopsutil can compute CPU deltas across
// successive Sample calls (gopsutil needs two measurements per *Process instance).
type GopsutilSampler struct {
	mu    sync.Mutex
	procs map[int32]*gops.Process
}

// Sample returns CPU% and RSS for the process with the given PID.
// The first call for a PID always returns CPUPercent = 0.0 because gopsutil
// needs two measurements to compute a delta. Subsequent calls return the real value.
func (g *GopsutilSampler) Sample(pid int) (Stats, error) {
	g.mu.Lock()
	if g.procs == nil {
		g.procs = make(map[int32]*gops.Process)
	}
	pid32 := int32(pid)
	p, ok := g.procs[pid32]
	if !ok {
		var err error
		p, err = gops.NewProcess(pid32)
		if err != nil {
			g.mu.Unlock()
			return Stats{}, fmt.Errorf("process %d not found: %w", pid, err)
		}
		g.procs[pid32] = p
	}
	g.mu.Unlock()

	cpu, err := p.CPUPercent()
	if err != nil {
		g.mu.Lock()
		delete(g.procs, pid32)
		g.mu.Unlock()
		return Stats{}, fmt.Errorf("cpu percent: %w", err)
	}
	mem, err := p.MemoryInfo()
	if err != nil {
		g.mu.Lock()
		delete(g.procs, pid32)
		g.mu.Unlock()
		return Stats{}, fmt.Errorf("memory info: %w", err)
	}
	if mem.RSS > math.MaxInt64 {
		return Stats{}, fmt.Errorf("rss %d overflows int64", mem.RSS)
	}
	return Stats{
		CPUPercent: cpu,
		RSSBytes:   int64(mem.RSS),
	}, nil
}
