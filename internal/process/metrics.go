package process

import (
	"context"
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

// Sampler reads CPU and memory stats for a running app process.
type Sampler interface {
	Sample(handle RunHandle) (Stats, error)
}

// GopsutilSampler is the production Sampler that reads from the OS via gopsutil.
// It caches process handles by PID so gopsutil can compute CPU deltas across
// successive Sample calls (gopsutil needs two measurements per *Process instance).
type GopsutilSampler struct {
	mu    sync.Mutex
	procs map[int32]*gops.Process
}

// Sample returns CPU% and RSS for the process identified by handle.PID.
// The first call for a PID always returns CPUPercent = 0.0 because gopsutil
// needs two measurements to compute a delta. Subsequent calls return the real value.
func (g *GopsutilSampler) Sample(handle RunHandle) (Stats, error) {
	g.mu.Lock()
	if g.procs == nil {
		g.procs = make(map[int32]*gops.Process)
	}
	pid32 := int32(handle.PID)
	p, ok := g.procs[pid32]
	if !ok {
		var err error
		p, err = gops.NewProcess(pid32)
		if err != nil {
			g.mu.Unlock()
			return Stats{}, fmt.Errorf("process %d not found: %w", handle.PID, err)
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

// Purge evicts cached process handles for PIDs not present in alive. The
// GopsutilSampler caches a *gops.Process per PID so CPU% can be computed as a
// delta across calls, and it only drops an entry when a Sample for that PID
// fails. A long-running caller that samples only currently-running PIDs (the
// metrics-history collector) never re-samples an exited PID, so without periodic
// pruning the cache grows unbounded as PIDs churn. Callers pass the set of live
// PIDs each cycle; everything else is dropped.
func (g *GopsutilSampler) Purge(alive map[int32]struct{}) {
	g.mu.Lock()
	defer g.mu.Unlock()
	for pid := range g.procs {
		if _, ok := alive[pid]; !ok {
			delete(g.procs, pid)
		}
	}
}

// RuntimeSampler implements Sampler by delegating to Runtime.Stats.
// Used when DockerRuntime is active so stats are fetched via the Docker API.
type RuntimeSampler struct {
	Runtime Runtime
}

func (r *RuntimeSampler) Sample(handle RunHandle) (Stats, error) {
	cpu, rss, err := r.Runtime.Stats(context.Background(), handle)
	if err != nil {
		return Stats{}, err
	}
	if rss > math.MaxInt64 {
		return Stats{}, fmt.Errorf("rss %d overflows int64", rss)
	}
	return Stats{CPUPercent: cpu, RSSBytes: int64(rss)}, nil
}
