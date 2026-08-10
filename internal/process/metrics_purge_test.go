package process

import (
	"testing"

	gops "github.com/shirou/gopsutil/v4/process"
)

// group builds a cached replica whose members are the given PIDs, standing in
// for the state a real Sample would have left behind.
func group(pids ...int32) *procGroup {
	members := make(map[int32]*gops.Process, len(pids))
	for _, pid := range pids {
		members[pid] = &gops.Process{Pid: pid}
	}
	return &procGroup{members: members}
}

// Purge must evict cached gopsutil handles for replicas no longer alive while
// retaining those still present. Without this, the background metrics collector
// (which only ever samples currently-running replicas) would leak a handle per
// exited process forever.
func TestGopsutilSamplerPurgeEvictsDeadPIDs(t *testing.T) {
	g := &GopsutilSampler{
		groups: map[int32]*procGroup{
			1: group(1),
			2: group(2),
			3: group(3),
		},
	}

	g.Purge(map[int32]struct{}{1: {}, 3: {}})

	if _, ok := g.groups[2]; ok {
		t.Error("pid 2 (not in alive set) should have been purged")
	}
	if _, ok := g.groups[1]; !ok {
		t.Error("pid 1 (alive) should have been retained")
	}
	if _, ok := g.groups[3]; !ok {
		t.Error("pid 3 (alive) should have been retained")
	}
	if len(g.groups) != 2 {
		t.Errorf("want 2 cached replicas after purge, got %d", len(g.groups))
	}
}

// A replica's whole group goes with it. The alive set holds root PIDs only, so
// keying eviction on members would drop every child on the first purge and
// destroy the baseline the children's rates are measured against.
func TestGopsutilSamplerPurgeKeepsAliveGroupMembers(t *testing.T) {
	g := &GopsutilSampler{
		groups: map[int32]*procGroup{
			10: group(10, 11, 12),
			20: group(20, 21),
		},
	}

	g.Purge(map[int32]struct{}{10: {}})

	kept, ok := g.groups[10]
	if !ok {
		t.Fatal("replica 10 (alive) should have been retained")
	}
	if len(kept.members) != 3 {
		t.Errorf("want all 3 members of the live replica retained, got %d; the alive "+
			"set names roots, and its children are not separately listed there",
			len(kept.members))
	}
	if _, ok := g.groups[20]; ok {
		t.Error("replica 20 (not in alive set) should have been purged")
	}
}

// Purge on an empty alive set clears the whole cache, and Purge on a nil map is
// a safe no-op-equivalent (everything is dead).
func TestGopsutilSamplerPurgeEmptyAliveClearsCache(t *testing.T) {
	g := &GopsutilSampler{groups: map[int32]*procGroup{1: group(1), 2: group(2)}}

	g.Purge(map[int32]struct{}{})

	if len(g.groups) != 0 {
		t.Errorf("want empty cache, got %d entries", len(g.groups))
	}
}

// Purge before any Sample call (nil internal map) must not panic.
func TestGopsutilSamplerPurgeNilMapNoPanic(t *testing.T) {
	g := &GopsutilSampler{}
	g.Purge(map[int32]struct{}{1: {}})
}
