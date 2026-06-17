package history

import (
	"testing"
	"time"

	"github.com/rvben/shinyhub/internal/process"
)

// --- fakes -------------------------------------------------------------------

type rk struct {
	slug  string
	index int
}

type fakeProcs struct {
	infos   []*process.ProcessInfo
	handles map[rk]process.RunHandle
}

func (f *fakeProcs) All() []*process.ProcessInfo { return f.infos }
func (f *fakeProcs) HandleReplica(slug string, index int) (process.RunHandle, bool) {
	h, ok := f.handles[rk{slug, index}]
	return h, ok
}

type fakeSessions struct{ counts map[string][]int64 }

func (f *fakeSessions) ReplicaSessionCounts(slug string) []int64 { return f.counts[slug] }

type fakeSampler struct {
	byPID     map[int]process.Stats
	errByPID  map[int]error
	sampled   []process.RunHandle
	lastAlive map[int32]struct{}
	purgeN    int
}

func (f *fakeSampler) Sample(h process.RunHandle) (process.Stats, error) {
	f.sampled = append(f.sampled, h)
	if err := f.errByPID[h.PID]; err != nil {
		return process.Stats{}, err
	}
	return f.byPID[h.PID], nil
}

func (f *fakeSampler) Purge(alive map[int32]struct{}) {
	f.purgeN++
	f.lastAlive = alive
}

func running(slug string, index, pid int) *process.ProcessInfo {
	return &process.ProcessInfo{Slug: slug, Index: index, PID: pid, Status: process.StatusRunning}
}

func newTestCollector(p *fakeProcs, sess *fakeSessions, smp *fakeSampler, st *Store) *Collector {
	return NewCollector(p, sess, smp, st, 15*time.Second)
}

func (f *fakeSampler) sampledPID(pid int) bool {
	for _, h := range f.sampled {
		if h.PID == pid {
			return true
		}
	}
	return false
}

// --- tests -------------------------------------------------------------------

func TestCollectAggregatesAcrossRunningReplicas(t *testing.T) {
	p := &fakeProcs{
		infos: []*process.ProcessInfo{running("demo", 0, 10), running("demo", 1, 11)},
		handles: map[rk]process.RunHandle{
			{"demo", 0}: {PID: 10},
			{"demo", 1}: {PID: 11},
		},
	}
	sess := &fakeSessions{counts: map[string][]int64{"demo": {2, 3}}}
	smp := &fakeSampler{byPID: map[int]process.Stats{
		10: {CPUPercent: 5, RSSBytes: 100},
		11: {CPUPercent: 7, RSSBytes: 200},
	}}
	st := NewStore(time.Hour, 15*time.Second)
	c := newTestCollector(p, sess, smp, st)

	c.collectOnce(1000)

	got := st.Series("demo", 1000)
	if len(got.TS) != 1 {
		t.Fatalf("want 1 snapshot, got %d", len(got.TS))
	}
	if got.CPU[0] != 12 {
		t.Errorf("cpu = %v, want 12 (5+7)", got.CPU[0])
	}
	if got.RSS[0] != 300 {
		t.Errorf("rss = %v, want 300 (100+200)", got.RSS[0])
	}
	if got.Sessions[0] != 5 {
		t.Errorf("sessions = %v, want 5 (2+3)", got.Sessions[0])
	}
	if got.Instances[0] != 2 {
		t.Errorf("instances = %v, want 2", got.Instances[0])
	}
}

func TestCollectSkipsNonRunningReplicas(t *testing.T) {
	stopped := &process.ProcessInfo{Slug: "demo", Index: 1, PID: 11, Status: process.StatusStopped}
	p := &fakeProcs{
		infos:   []*process.ProcessInfo{running("demo", 0, 10), stopped},
		handles: map[rk]process.RunHandle{{"demo", 0}: {PID: 10}, {"demo", 1}: {PID: 11}},
	}
	sess := &fakeSessions{counts: map[string][]int64{"demo": {4}}}
	smp := &fakeSampler{byPID: map[int]process.Stats{10: {CPUPercent: 3, RSSBytes: 50}}}
	st := NewStore(time.Hour, 15*time.Second)
	c := newTestCollector(p, sess, smp, st)

	c.collectOnce(1000)

	got := st.Series("demo", 1000)
	if got.Instances[0] != 1 {
		t.Errorf("instances = %v, want 1 (stopped replica excluded)", got.Instances[0])
	}
	if smp.sampledPID(11) {
		t.Error("stopped replica (pid 11) must not be sampled")
	}
}

func TestCollectPID0NotSampled(t *testing.T) {
	p := &fakeProcs{
		// Fargate/remote replica: running but PID 0, empty ContainerID.
		infos:   []*process.ProcessInfo{running("demo", 0, 0)},
		handles: map[rk]process.RunHandle{{"demo", 0}: {PID: 0, ContainerID: ""}},
	}
	sess := &fakeSessions{counts: map[string][]int64{"demo": {9}}}
	smp := &fakeSampler{byPID: map[int]process.Stats{}}
	st := NewStore(time.Hour, 15*time.Second)
	c := newTestCollector(p, sess, smp, st)

	c.collectOnce(1000)

	if smp.sampledPID(0) || len(smp.sampled) != 0 {
		t.Errorf("PID-0 replica must not be sampled, sampled=%+v", smp.sampled)
	}
	got := st.Series("demo", 1000)
	if got.Instances[0] != 1 || got.Sessions[0] != 9 {
		t.Errorf("instances/sessions = %d/%d, want 1/9 (counted even without cpu/rss)", got.Instances[0], got.Sessions[0])
	}
	if got.CPU[0] != 0 || got.RSS[0] != 0 {
		t.Errorf("cpu/rss = %v/%v, want 0/0 for PID-0 replica", got.CPU[0], got.RSS[0])
	}
}

func TestCollectSessionMinusOneTreatedAsZero(t *testing.T) {
	p := &fakeProcs{
		infos:   []*process.ProcessInfo{running("demo", 0, 10)},
		handles: map[rk]process.RunHandle{{"demo", 0}: {PID: 10}},
	}
	sess := &fakeSessions{counts: map[string][]int64{"demo": {2, -1}}} // -1 = empty slot sentinel
	smp := &fakeSampler{byPID: map[int]process.Stats{10: {CPUPercent: 1, RSSBytes: 1}}}
	st := NewStore(time.Hour, 15*time.Second)
	c := newTestCollector(p, sess, smp, st)

	c.collectOnce(1000)

	got := st.Series("demo", 1000)
	if got.Sessions[0] != 2 {
		t.Errorf("sessions = %v, want 2 (the -1 sentinel must be treated as 0, not subtracted)", got.Sessions[0])
	}
}

func TestCollectSampleErrorSkipsCPURSSButCountsInstance(t *testing.T) {
	p := &fakeProcs{
		infos:   []*process.ProcessInfo{running("demo", 0, 10), running("demo", 1, 11)},
		handles: map[rk]process.RunHandle{{"demo", 0}: {PID: 10}, {"demo", 1}: {PID: 11}},
	}
	sess := &fakeSessions{counts: map[string][]int64{"demo": {1, 1}}}
	smp := &fakeSampler{
		byPID:    map[int]process.Stats{10: {CPUPercent: 5, RSSBytes: 100}},
		errByPID: map[int]error{11: errSample},
	}
	st := NewStore(time.Hour, 15*time.Second)
	c := newTestCollector(p, sess, smp, st)

	c.collectOnce(1000)

	got := st.Series("demo", 1000)
	if got.Instances[0] != 2 {
		t.Errorf("instances = %v, want 2 (errored replica still counts)", got.Instances[0])
	}
	if got.CPU[0] != 5 || got.RSS[0] != 100 {
		t.Errorf("cpu/rss = %v/%v, want 5/100 (errored replica contributes nothing)", got.CPU[0], got.RSS[0])
	}
}

func TestCollectDropToZeroEdgeOnceThenPauses(t *testing.T) {
	infos := []*process.ProcessInfo{running("demo", 0, 10)}
	p := &fakeProcs{infos: infos, handles: map[rk]process.RunHandle{{"demo", 0}: {PID: 10}}}
	sess := &fakeSessions{counts: map[string][]int64{"demo": {1}}}
	smp := &fakeSampler{byPID: map[int]process.Stats{10: {CPUPercent: 5, RSSBytes: 100}}}
	st := NewStore(time.Hour, 15*time.Second)
	c := newTestCollector(p, sess, smp, st)

	c.collectOnce(1000) // running -> real sample

	// demo stops running.
	p.infos = nil
	c.collectOnce(1015) // not running -> one zero edge sample
	c.collectOnce(1030) // still not running -> NO new sample (paused)

	got := st.Series("demo", 1030)
	if len(got.TS) != 2 {
		t.Fatalf("want 2 samples (real + one drop-to-zero), got %d: %+v", len(got.TS), got)
	}
	if got.Instances[0] != 1 || got.Instances[1] != 0 {
		t.Errorf("instances = %v, want [1 0]", got.Instances)
	}
	if got.CPU[1] != 0 || got.RSS[1] != 0 || got.Sessions[1] != 0 {
		t.Errorf("drop sample = cpu %v rss %v sess %v, want all 0", got.CPU[1], got.RSS[1], got.Sessions[1])
	}
}

func TestCollectPurgesWithAlivePIDSet(t *testing.T) {
	p := &fakeProcs{
		infos:   []*process.ProcessInfo{running("demo", 0, 10), running("demo", 1, 11)},
		handles: map[rk]process.RunHandle{{"demo", 0}: {PID: 10}, {"demo", 1}: {PID: 11}},
	}
	sess := &fakeSessions{counts: map[string][]int64{"demo": {0, 0}}}
	smp := &fakeSampler{byPID: map[int]process.Stats{10: {}, 11: {}}}
	st := NewStore(time.Hour, 15*time.Second)
	c := newTestCollector(p, sess, smp, st)

	c.collectOnce(1000)

	if smp.purgeN != 1 {
		t.Fatalf("Purge called %d times, want 1 per tick", smp.purgeN)
	}
	if _, ok := smp.lastAlive[10]; !ok {
		t.Error("alive set should contain pid 10")
	}
	if _, ok := smp.lastAlive[11]; !ok {
		t.Error("alive set should contain pid 11")
	}
	if len(smp.lastAlive) != 2 {
		t.Errorf("alive set size = %d, want 2", len(smp.lastAlive))
	}
}

func TestCollectAppendsOneSnapshotPerActiveSlug(t *testing.T) {
	p := &fakeProcs{
		infos:   []*process.ProcessInfo{running("a", 0, 10), running("b", 0, 20)},
		handles: map[rk]process.RunHandle{{"a", 0}: {PID: 10}, {"b", 0}: {PID: 20}},
	}
	sess := &fakeSessions{counts: map[string][]int64{"a": {0}, "b": {0}}}
	smp := &fakeSampler{byPID: map[int]process.Stats{10: {}, 20: {}}}
	st := NewStore(time.Hour, 15*time.Second)
	c := newTestCollector(p, sess, smp, st)

	c.collectOnce(1000)

	if n := len(st.Series("a", 1000).TS); n != 1 {
		t.Errorf("slug a got %d snapshots, want 1", n)
	}
	if n := len(st.Series("b", 1000).TS); n != 1 {
		t.Errorf("slug b got %d snapshots, want 1", n)
	}
}

func TestCollectGCRunsEachTick(t *testing.T) {
	st := NewStore(time.Hour, 15*time.Second)
	// Pre-seed a stale ring for an app that is no longer running anywhere.
	st.Append("gone", sample(100, 1, 1, 1, 1)) // very old
	p := &fakeProcs{infos: nil, handles: map[rk]process.RunHandle{}}
	sess := &fakeSessions{counts: map[string][]int64{}}
	smp := &fakeSampler{byPID: map[int]process.Stats{}}
	c := newTestCollector(p, sess, smp, st)

	c.collectOnce(1_000_000) // far beyond the window from ts=100

	if st.has("gone") {
		t.Error("collector tick should GC the stale ring for a long-gone app")
	}
}

var errSample = sampleError("boom")

type sampleError string

func (e sampleError) Error() string { return string(e) }
