package process

import (
	"context"
	"fmt"
	"math"
	"sync"
	"syscall"
	"time"

	gops "github.com/shirou/gopsutil/v4/process"
)

// Stats holds a point-in-time resource snapshot for one app replica.
//
// CPUPercent is a rate over the interval since the previous sample of the same
// replica, on the scale where 100 means one fully busy core. It is nil when no
// rate can be computed yet, which is the case for the first sample of a replica
// and after the sampler's cache has been purged: a rate needs two measurements,
// and reporting the single measurement as 0 would be indistinguishable from an
// app that is genuinely idle.
//
// All figures cover the replica's whole process group, not just the PID the
// runtime recorded. See GopsutilSampler. PSS, USS, and swap PSS are optional:
// Linux native processes can expose them through smaps_rollup, while remote
// runtimes and other operating systems leave them nil. AttributionPartial is
// true when at least one process-group member could not be attributed.
type Stats struct {
	CPUPercent         *float64
	RSSBytes           int64
	PSSBytes           *int64
	USSBytes           *int64
	SwapPSSBytes       *int64
	AttributionPartial bool
}

// Sampler reads CPU and memory stats for a running app process.
type Sampler interface {
	Sample(handle RunHandle) (Stats, error)
}

// groupScanTTL bounds how stale the process-group membership map may be. One
// sampling tick fans out across every replica in quick succession, so a TTL
// longer than that burst but shorter than any polling interval means each tick
// enumerates the host's processes exactly once. A child forked inside the window
// is counted from the next tick.
const groupScanTTL = time.Second

// GopsutilSampler is the production Sampler that reads from the OS via gopsutil.
//
// It measures a replica's entire process group rather than the single PID the
// runtime recorded, because that PID is often a launcher rather than the app. A
// Python app runs as `uv run ... shiny run app.py`, and uv forks the interpreter
// instead of exec'ing it, so the recorded PID does no work and holds a few
// megabytes no matter what the app is doing. NativeRuntime.Start already places
// each replica in its own process group, which makes the group exactly the set
// of processes belonging to that replica, and also picks up workers the app
// forks for itself.
//
// It caches a gopsutil handle per member because Percent(0) computes its rate
// against the previous reading stored on the *Process, so a fresh handle per
// call would have nothing to subtract from and would report every process as
// idle forever.
type GopsutilSampler struct {
	mu     sync.Mutex
	groups map[int32]*procGroup

	scan      map[int32][]int32
	scannedAt time.Time

	// Test seams. Both default to the real implementations on first use, so the
	// zero value is a working sampler.
	nowFn  func() time.Time
	scanFn func() (map[int32][]int32, error)
}

// procGroup holds the cached gopsutil handles for one replica, keyed by member
// PID. Presence of a member in this map means an earlier Sample got all the way
// through Percent for it, which is what tells a primed process apart from one
// whose baseline is only now being taken.
type procGroup struct {
	members map[int32]*gops.Process
}

// scanProcessGroups maps each process group id to the PIDs it contains.
//
// It reads the process list once and asks the kernel for each PID's group,
// rather than using gopsutil's Children: on Linux that walks all of /proc
// reading every stat file and returns only direct children, so covering a whole
// tree would cost one full scan per replica per level.
func scanProcessGroups() (map[int32][]int32, error) {
	pids, err := gops.Pids()
	if err != nil {
		return nil, fmt.Errorf("list pids: %w", err)
	}
	groups := make(map[int32][]int32, len(pids))
	for _, pid := range pids {
		pgid, err := syscall.Getpgid(int(pid))
		if err != nil {
			// Exited between listing and lookup. Normal on a busy host.
			continue
		}
		groups[int32(pgid)] = append(groups[int32(pgid)], pid)
	}
	return groups, nil
}

// members returns the PIDs to sample for a replica rooted at pid, always
// including the root itself.
//
// A non-empty group under the root's own PID means the root leads a process
// group, which is how NativeRuntime starts every replica. When there is no such
// group the root is not a group leader and its PGID belongs to something else
// entirely, so the root alone is the only defensible answer: aggregating over
// somebody else's group would bill this replica for unrelated processes.
func (g *GopsutilSampler) members(root int32) []int32 {
	found := g.scan[root]
	out := make([]int32, 0, len(found)+1)
	out = append(out, root)
	for _, pid := range found {
		if pid != root {
			out = append(out, pid)
		}
	}
	return out
}

// refreshScan re-reads process-group membership if the cached map has aged out.
//
// A failed scan keeps the previous map rather than falling back to the root
// alone. Losing the ability to enumerate processes must not quietly turn a busy
// app into an idle one; stale membership is a far smaller error than dropping
// every child. With no previous map there is nothing to stand on and the error
// is returned.
func (g *GopsutilSampler) refreshScan() error {
	now := g.nowFn()
	if g.scan != nil && now.Sub(g.scannedAt) < groupScanTTL {
		return nil
	}
	scan, err := g.scanFn()
	if err != nil {
		if g.scan != nil {
			return nil
		}
		return err
	}
	g.scan = scan
	g.scannedAt = now
	return nil
}

// Sample returns the CPU rate and RSS of the process group led by handle.PID.
//
// CPU is the sum of the rates of the members that already had a baseline, and
// is nil until the root has one. Gating on the root rather than on every member
// keeps the rate reportable for an app that forks workers continuously, which
// would otherwise never have a fully primed group; a member joining the group
// contributes nothing for one tick and is counted from the next.
//
// A failure on the root is returned as an error, since the process the runtime
// recorded is gone. A failure on any other member is skipped: members come from
// a snapshot that may be up to groupScanTTL old, so one exiting mid-sample is
// ordinary rather than a reason to report nothing for the replica.
//
// The whole body holds the lock. Beyond guarding the maps, this serialises
// Percent on any one handle, which is not safe to call concurrently: it reads
// and then overwrites the previous CPU times stored on the *Process, so two
// samples racing on the same replica would corrupt each other's baseline.
func (g *GopsutilSampler) Sample(handle RunHandle) (Stats, error) {
	g.mu.Lock()
	defer g.mu.Unlock()

	if g.nowFn == nil {
		g.nowFn = time.Now
	}
	if g.scanFn == nil {
		g.scanFn = scanProcessGroups
	}
	if g.groups == nil {
		g.groups = make(map[int32]*procGroup)
	}
	if err := g.refreshScan(); err != nil {
		return Stats{}, err
	}

	root := int32(handle.PID)
	grp, ok := g.groups[root]
	if !ok {
		grp = &procGroup{members: make(map[int32]*gops.Process)}
		g.groups[root] = grp
	}

	live := g.members(root)
	seen := make(map[int32]struct{}, len(live))

	var (
		cpu                float64
		rss                uint64
		pss                uint64
		uss                uint64
		swapPSS            uint64
		attributedMembers  int
		attributionPartial bool
		rootPrimed         bool
	)
	for _, pid := range live {
		seen[pid] = struct{}{}

		p, primed := grp.members[pid]
		if !primed {
			var err error
			p, err = gops.NewProcess(pid)
			if err != nil {
				if pid == root {
					return Stats{}, fmt.Errorf("process %d not found: %w", handle.PID, err)
				}
				continue
			}
			grp.members[pid] = p
		}

		pct, err := p.Percent(0)
		if err != nil {
			delete(grp.members, pid)
			if pid == root {
				return Stats{}, fmt.Errorf("cpu percent: %w", err)
			}
			continue
		}
		mem, err := p.MemoryInfo()
		if err != nil {
			delete(grp.members, pid)
			if pid == root {
				return Stats{}, fmt.Errorf("memory info: %w", err)
			}
			continue
		}

		rss += mem.RSS
		if attribution, err := readMemoryAttribution(pid); err == nil {
			pss += attribution.PSS
			uss += attribution.USS
			swapPSS += attribution.SwapPSS
			attributedMembers++
		} else {
			// smaps_rollup is supplementary observability. Permissions, kernel
			// support, or a process exiting mid-sample must not make the existing
			// CPU/RSS sample fail.
			attributionPartial = true
		}
		if primed {
			cpu += pct
			if pid == root {
				rootPrimed = true
			}
		}
	}

	// Drop handles for members that have left the group, so a replica that churns
	// short-lived workers does not accumulate one handle per worker for its whole
	// lifetime.
	for pid := range grp.members {
		if _, ok := seen[pid]; !ok {
			delete(grp.members, pid)
		}
	}

	if rss > math.MaxInt64 {
		return Stats{}, fmt.Errorf("rss %d overflows int64", rss)
	}
	stats := Stats{RSSBytes: int64(rss)}
	if attributedMembers > 0 {
		if pss > math.MaxInt64 || uss > math.MaxInt64 || swapPSS > math.MaxInt64 {
			return Stats{}, fmt.Errorf("memory attribution overflows int64")
		}
		stats.PSSBytes = Int64(int64(pss))
		stats.USSBytes = Int64(int64(uss))
		stats.SwapPSSBytes = Int64(int64(swapPSS))
		stats.AttributionPartial = attributionPartial
	}
	if rootPrimed {
		stats.CPUPercent = &cpu
	}
	return stats, nil
}

// Purge evicts cached state for replicas whose root PID is not present in alive.
// Purging a live replica is safe but not free: its next sample reports CPU as
// unavailable while a new baseline is taken.
//
// The sampler caches a gopsutil handle per process so CPU% can be computed as a
// delta across calls, and it only drops one when a sample for that process
// fails. A long-running caller that samples only currently-running replicas (the
// metrics-history collector) never re-samples an exited one, so without periodic
// pruning the cache grows unbounded as PIDs churn. Callers pass the set of live
// root PIDs each cycle; everything else is dropped.
func (g *GopsutilSampler) Purge(alive map[int32]struct{}) {
	g.mu.Lock()
	defer g.mu.Unlock()
	for pid := range g.groups {
		if _, ok := alive[pid]; !ok {
			delete(g.groups, pid)
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

// Int64 returns a pointer to v. It keeps optional byte counters readable at
// API and test call sites without assigning through temporary variables.
func Int64(v int64) *int64 { return &v }
