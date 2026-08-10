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

// slugAgg accumulates one tick's replica samples for a single app.
//
// cpuMissing records that some replica contributed no CPU rate: either it could
// not report one yet, which happens on a replica's first tick and so lines up
// with every deploy and restart, or it runs on a tier with no local process to
// sample. Summing only the replicas that did report would quietly understate the
// app, so the whole point is published as unavailable instead.
type slugAgg struct {
	cpu        float64
	cpuMissing bool
	rss        int64
	instances  int
}

// cpuTotal returns the summed CPU rate, or nil when any sampled replica lacked
// one.
func (a *slugAgg) cpuTotal() *float64 {
	if a.cpuMissing {
		return nil
	}
	cpu := a.cpu
	return &cpu
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
			// instances/sessions but has no local CPU/RAM to sample. Its CPU is
			// unknown rather than zero, and staying silent about that would
			// publish a flat 0% line for an app running entirely off-host, or
			// show a bursting app's usage dropping as it scales onto Fargate.
			a.cpuMissing = true
			continue
		}
		if stats, err := c.sampler.Sample(handle); err == nil {
			if stats.CPUPercent == nil {
				a.cpuMissing = true
			} else {
				a.cpu += *stats.CPUPercent
			}
			a.rss += stats.RSSBytes
		}
	}

	activeNow := make(map[string]struct{}, len(byslug))
	for slug, a := range byslug {
		c.store.Append(slug, Sample{
			TS:        now,
			CPU:       a.cpuTotal(),
			RSS:       a.rss,
			Sessions:  sumSessions(c.sessions.ReplicaSessionCounts(slug)),
			Instances: a.instances,
		})
		activeNow[slug] = struct{}{}
	}

	// Record exactly one drop-to-zero edge for apps sampled last tick that have
	// no running replicas now, then stop sampling them until they run again.
	//
	// The edge carries an explicit 0 rather than an absent rate: a stopped app
	// really is using no CPU, which is a measurement and not a gap. Leaving it
	// nil would make the chart skip the point and join the line straight across
	// the shutdown, drawing the app as though it kept running.
	for slug := range c.lastActive {
		if _, ok := activeNow[slug]; !ok {
			c.store.Append(slug, Sample{TS: now, CPU: process.Float(0)})
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
