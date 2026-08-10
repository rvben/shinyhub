package process

import (
	"context"
	"fmt"
	"math"
	"sync"

	gops "github.com/shirou/gopsutil/v4/process"
)

// Stats holds a point-in-time resource snapshot for one process.
//
// CPUPercent is a rate over the interval since the previous sample of the same
// process, on the scale where 100 means one fully busy core. It is nil when no
// rate can be computed yet, which is the case for the first sample of a process
// and after the sampler's cache has been purged: a rate needs two measurements,
// and reporting the single measurement as 0 would be indistinguishable from an
// app that is genuinely idle.
type Stats struct {
	CPUPercent *float64
	RSSBytes   int64
}

// Sampler reads CPU and memory stats for a running app process.
type Sampler interface {
	Sample(handle RunHandle) (Stats, error)
}

// GopsutilSampler is the production Sampler that reads from the OS via gopsutil.
// It caches process handles by PID because Percent(0) computes its rate against
// the previous reading stored on the *Process, so a fresh handle per call would
// have nothing to subtract from and report every process as idle forever.
type GopsutilSampler struct {
	mu    sync.Mutex
	procs map[int32]*gops.Process
}

// Sample returns CPU rate and RSS for the process identified by handle.PID.
//
// CPUPercent is nil on the first call for a PID. A cached handle is proof that
// an earlier Sample got all the way through Percent, so presence in the cache
// is what distinguishes a primed process from one whose baseline was just
// taken. Both failure paths below evict the handle, which keeps that true: a
// PID that failed mid-sample is re-primed rather than credited with a baseline
// it never established.
func (g *GopsutilSampler) Sample(handle RunHandle) (Stats, error) {
	g.mu.Lock()
	if g.procs == nil {
		g.procs = make(map[int32]*gops.Process)
	}
	pid32 := int32(handle.PID)
	p, primed := g.procs[pid32]
	if !primed {
		var err error
		p, err = gops.NewProcess(pid32)
		if err != nil {
			g.mu.Unlock()
			return Stats{}, fmt.Errorf("process %d not found: %w", handle.PID, err)
		}
		g.procs[pid32] = p
	}
	g.mu.Unlock()

	cpu, err := p.Percent(0)
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
	stats := Stats{RSSBytes: int64(mem.RSS)}
	if primed {
		stats.CPUPercent = &cpu
	}
	return stats, nil
}

// Purge evicts cached process handles for PIDs not present in alive. Purging a
// live PID is safe but not free: its next sample reports CPU as unavailable
// while a new baseline is taken.
//
// The GopsutilSampler caches a *gops.Process per PID so CPU% can be computed as
// a delta across calls, and it only drops an entry when a Sample for that PID
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

// Float returns a pointer to v, for building a Stats or a Runtime.Stats result
// whose CPU rate is known. A runtime that cannot yet compute a rate returns nil
// instead.
func Float(v float64) *float64 { return &v }
