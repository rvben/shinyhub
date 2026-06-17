package history

import (
	"context"
	"time"

	"github.com/rvben/shinyhub/internal/process"
)

// ProcessSource is the subset of *process.Manager the collector reads: the local
// running-process snapshot plus per-replica run handles for sampling.
type ProcessSource interface {
	All() []*process.ProcessInfo
	HandleReplica(slug string, index int) (process.RunHandle, bool)
}

// SessionSource is the subset of *proxy.Proxy the collector reads: per-replica
// live session counts. Empty slots are reported as -1.
type SessionSource interface {
	ReplicaSessionCounts(slug string) []int64
}

// purger is the optional capability (implemented by process.GopsutilSampler) to
// evict cached handles for exited PIDs. RuntimeSampler does not implement it.
type purger interface {
	Purge(alive map[int32]struct{})
}

// Collector samples local app resource usage on a fixed cadence and writes one
// aggregated snapshot per running app into the Store. It is intentionally
// always-on (every instance records the replicas it runs locally) and owns ring
// lifecycle entirely through the Store's time-based GC.
type Collector struct {
	procs    ProcessSource
	sessions SessionSource
	sampler  process.Sampler
	store    *Store
	interval time.Duration
	now      func() int64

	// lastActive is the set of slugs that produced a real sample on the previous
	// tick. It drives the single drop-to-zero edge recorded when an app stops.
	lastActive map[string]struct{}
}

// NewCollector wires a Collector. The sampler should be dedicated to the
// collector (its own CPU-delta baseline at the fixed interval); pass a
// *process.GopsutilSampler in native mode or a *process.RuntimeSampler in
// container mode, mirroring the API server's sampler selection.
func NewCollector(procs ProcessSource, sessions SessionSource, sampler process.Sampler, store *Store, interval time.Duration) *Collector {
	return &Collector{
		procs:      procs,
		sessions:   sessions,
		sampler:    sampler,
		store:      store,
		interval:   interval,
		now:        func() int64 { return time.Now().Unix() },
		lastActive: map[string]struct{}{},
	}
}

// Run samples on each tick until ctx is cancelled. Started unconditionally at
// startup and cancelled on shutdown so it stops cleanly on SIGTERM and on a
// tableflip zero-downtime re-exec.
func (c *Collector) Run(ctx context.Context) {
	ticker := time.NewTicker(c.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			c.collectOnce(c.now())
		}
	}
}

type slugAgg struct {
	cpu       float64
	rss       int64
	instances int
}

// collectOnce records one snapshot per locally-running app at time now (unix
// seconds), emits a single drop-to-zero edge for apps that just stopped, purges
// sampler handles for exited PIDs, and GCs stale rings.
func (c *Collector) collectOnce(now int64) {
	byslug := map[string]*slugAgg{}
	alive := map[int32]struct{}{}

	for _, info := range c.procs.All() {
		if info.Status != process.StatusRunning {
			continue
		}
		a := byslug[info.Slug]
		if a == nil {
			a = &slugAgg{}
			byslug[info.Slug] = a
		}
		a.instances++
		if info.PID != 0 {
			alive[int32(info.PID)] = struct{}{}
		}
		handle, ok := c.procs.HandleReplica(info.Slug, info.Index)
		if !ok || (handle.PID == 0 && handle.ContainerID == "") {
			// No PID and no container: a Fargate/remote replica. It counts toward
			// instances/sessions but has no local CPU/RAM to sample.
			continue
		}
		if stats, err := c.sampler.Sample(handle); err == nil {
			a.cpu += stats.CPUPercent
			a.rss += stats.RSSBytes
		}
	}

	activeNow := make(map[string]struct{}, len(byslug))
	for slug, a := range byslug {
		c.store.Append(slug, Sample{
			TS:        now,
			CPU:       a.cpu,
			RSS:       a.rss,
			Sessions:  sumSessions(c.sessions.ReplicaSessionCounts(slug)),
			Instances: a.instances,
		})
		activeNow[slug] = struct{}{}
	}

	// Record exactly one drop-to-zero edge for apps sampled last tick that have
	// no running replicas now, then stop sampling them until they run again.
	for slug := range c.lastActive {
		if _, ok := activeNow[slug]; !ok {
			c.store.Append(slug, Sample{TS: now})
		}
	}
	c.lastActive = activeNow

	if p, ok := c.sampler.(purger); ok {
		p.Purge(alive)
	}
	c.store.GC(now)
}

// sumSessions totals the per-replica session counts, treating the -1 empty-slot
// sentinel (and any negative) as 0 so an empty slot never under-counts.
func sumSessions(counts []int64) int64 {
	var total int64
	for _, n := range counts {
		if n > 0 {
			total += n
		}
	}
	return total
}
