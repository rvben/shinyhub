package process

import (
	"testing"

	gops "github.com/shirou/gopsutil/v4/process"
)

// Purge must evict cached gopsutil handles for PIDs no longer alive while
// retaining handles for PIDs still present. Without this, the background metrics
// collector (which only ever samples currently-running PIDs) would leak a
// *gops.Process per exited PID forever.
func TestGopsutilSamplerPurgeEvictsDeadPIDs(t *testing.T) {
	g := &GopsutilSampler{
		procs: map[int32]*gops.Process{
			1: {Pid: 1},
			2: {Pid: 2},
			3: {Pid: 3},
		},
	}

	g.Purge(map[int32]struct{}{1: {}, 3: {}})

	if _, ok := g.procs[2]; ok {
		t.Error("pid 2 (not in alive set) should have been purged")
	}
	if _, ok := g.procs[1]; !ok {
		t.Error("pid 1 (alive) should have been retained")
	}
	if _, ok := g.procs[3]; !ok {
		t.Error("pid 3 (alive) should have been retained")
	}
	if len(g.procs) != 2 {
		t.Errorf("want 2 cached handles after purge, got %d", len(g.procs))
	}
}

// Purge on an empty alive set clears the whole cache, and Purge on a nil map is
// a safe no-op-equivalent (everything is dead).
func TestGopsutilSamplerPurgeEmptyAliveClearsCache(t *testing.T) {
	g := &GopsutilSampler{procs: map[int32]*gops.Process{1: {Pid: 1}, 2: {Pid: 2}}}

	g.Purge(map[int32]struct{}{})

	if len(g.procs) != 0 {
		t.Errorf("want empty cache, got %d entries", len(g.procs))
	}
}

// Purge before any Sample call (nil internal map) must not panic.
func TestGopsutilSamplerPurgeNilMapNoPanic(t *testing.T) {
	g := &GopsutilSampler{}
	g.Purge(map[int32]struct{}{1: {}})
}
