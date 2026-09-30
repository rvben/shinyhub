// Package history keeps a short, in-memory time series of per-app resource
// metrics (CPU, memory, sessions, instance count) for the dashboard's "Trends"
// view. It deliberately holds only a few hours of data in bounded per-app ring
// buffers: no database, no persistence across restart, no long-term retention.
package history

import (
	"sync"
	"time"
)

// maxPointsPerApp hard-caps a single app's ring regardless of the configured
// window/interval, so no configuration can drive unbounded memory. At ~40 bytes
// per Sample this bounds a ring to roughly 350KB.
const maxPointsPerApp = 8640

// Sample is one aggregated, point-in-time resource snapshot for an app, summed
// across the replicas running locally on this instance.
type Sample struct {
	TS        int64    // unix seconds
	CPU       *float64 // sum of replica CPU%; nil when no rate was available
	RSS       int64    // sum of replica RSS bytes
	Sessions  int64    // sum of active sessions
	Instances int      // count of running replicas
}

// Series is the columnar form returned to API consumers. Parallel arrays keep
// the JSON compact and map directly onto the sparkline renderer. All slices are
// non-nil so they marshal as [] rather than null.
type Series struct {
	TS        []int64    `json:"ts"`
	CPU       []*float64 `json:"cpu"`
	RSS       []int64    `json:"rss"`
	Sessions  []int64    `json:"sessions"`
	Instances []int      `json:"instances"`
}

// Store holds one bounded ring buffer per app slug. It is safe for one writer
// (the collector) and many concurrent readers (HTTP handlers): the collector
// mutates under Lock; readers copy out the requested window under RLock so JSON
// marshaling never races the writer.
type Store struct {
	mu         sync.RWMutex
	rings      map[string]*ring
	capacity   int
	window     time.Duration
	interval   time.Duration
	replicaCPU map[string]map[int]replicaCPUWindow
}

type replicaCPUPoint struct {
	ts      int64
	percent *float64
}

type replicaCPUWindow struct {
	runID  string
	points []replicaCPUPoint
}

// NewStore builds a Store whose per-app ring holds min(window/interval,
// maxPointsPerApp) points. interval must be positive; capacity is floored at 1.
func NewStore(window, interval time.Duration) *Store {
	capacity := 1
	if interval > 0 {
		capacity = int(window / interval)
	}
	if capacity > maxPointsPerApp {
		capacity = maxPointsPerApp
	}
	if capacity < 1 {
		capacity = 1
	}
	return &Store{
		rings:      make(map[string]*ring),
		replicaCPU: make(map[string]map[int]replicaCPUWindow),
		capacity:   capacity,
		window:     window,
		interval:   interval,
	}
}

// RecordReplicaCPU keeps only three fixed-cadence samples per concrete run.
// A missing rate breaks the streak. The history collector calls this once per
// tick, independently of how many clients request live metrics.
func (s *Store) RecordReplicaCPU(slug string, index int, runID string, ts int64, percent *float64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rows := s.replicaCPU[slug]
	if rows == nil {
		rows = make(map[int]replicaCPUWindow)
		s.replicaCPU[slug] = rows
	}
	w := rows[index]
	if w.runID != runID {
		w = replicaCPUWindow{runID: runID}
	}
	if len(w.points) > 0 && ts <= w.points[len(w.points)-1].ts {
		return
	}
	var recorded *float64
	if percent != nil {
		value := *percent
		recorded = &value
	}
	w.points = append(w.points, replicaCPUPoint{ts: ts, percent: recorded})
	if len(w.points) > 3 {
		w.points = w.points[len(w.points)-3:]
	}
	rows[index] = w
}

// ReplicaCPUSaturated reports three recent consecutive samples at or above
// 90% of the replica's own ceiling. The caller supplies its enforced quota (or
// one core), so quota policy remains in the API rather than the collector.
func (s *Store) ReplicaCPUSaturated(slug string, index int, runID string, now int64, ceilingPercent float64) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	w := s.replicaCPU[slug][index]
	if w.runID != runID || len(w.points) != 3 || ceilingPercent <= 0 {
		return false
	}
	maxAge := int64((2 * s.interval).Seconds())
	if maxAge < 1 {
		maxAge = 1
	}
	maxGap := int64((3 * s.interval / 2).Seconds())
	if maxGap < 1 {
		maxGap = 1
	}
	if now-w.points[2].ts > maxAge ||
		w.points[1].ts-w.points[0].ts > maxGap ||
		w.points[2].ts-w.points[1].ts > maxGap {
		return false
	}
	for _, point := range w.points {
		if point.percent == nil || *point.percent < 0.9*ceilingPercent {
			return false
		}
	}
	return true
}

// EmptySeries returns a Series with non-nil, empty slices so it marshals as []
// arrays rather than null. Used for unknown slugs and when history is disabled.
func EmptySeries() Series {
	return Series{TS: []int64{}, CPU: []*float64{}, RSS: []int64{}, Sessions: []int64{}, Instances: []int{}}
}

// WindowSeconds returns the retention window in whole seconds (for the API).
func (s *Store) WindowSeconds() int64 { return int64(s.window.Seconds()) }

// IntervalSeconds returns the sample interval in whole seconds (for the API).
func (s *Store) IntervalSeconds() int64 { return int64(s.interval.Seconds()) }

// Append records one sample for slug, creating the ring lazily.
func (s *Store) Append(slug string, sm Sample) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r := s.rings[slug]
	if r == nil {
		r = newRing(s.capacity)
		s.rings[slug] = r
	}
	r.push(sm)
}

// Series returns slug's samples within the window ending at now (unix seconds),
// oldest-first, copied out. Unknown slugs return an empty, non-nil series.
func (s *Store) Series(slug string, now int64) Series {
	return s.SeriesWindow(slug, now, s.window)
}

// SeriesWindow returns only the requested tail of a ring. The duration is
// clamped to the configured retention window so a specialized fleet view can
// avoid copying and serializing hours of data it will immediately discard.
func (s *Store) SeriesWindow(slug string, now int64, window time.Duration) Series {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := EmptySeries()
	r := s.rings[slug]
	if r == nil {
		return out
	}
	if window <= 0 || window > s.window {
		window = s.window
	}
	cutoff := now - int64(window.Seconds())
	bn := len(r.buf)
	for i := range r.size {
		sm := r.buf[(r.start+i)%bn] // oldest-first
		if sm.TS < cutoff {
			continue
		}
		out.TS = append(out.TS, sm.TS)
		out.CPU = append(out.CPU, sm.CPU)
		out.RSS = append(out.RSS, sm.RSS)
		out.Sessions = append(out.Sessions, sm.Sessions)
		out.Instances = append(out.Instances, sm.Instances)
	}
	return out
}

// GC drops any ring whose newest sample is older than the window ending at now.
// This is the sole reclaimer for rings orphaned by app deletion, hibernation, or
// a deletion that happened on another instance: a slug that stops being sampled
// ages out within one window.
func (s *Store) GC(now int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	cutoff := now - int64(s.window.Seconds())
	for slug, r := range s.rings {
		newest, ok := r.newest()
		if !ok || newest.TS < cutoff {
			delete(s.rings, slug)
		}
	}
	replicaCutoff := now - int64((2 * s.interval).Seconds())
	for slug, rows := range s.replicaCPU {
		for index, w := range rows {
			if len(w.points) == 0 || w.points[len(w.points)-1].ts < replicaCutoff {
				delete(rows, index)
			}
		}
		if len(rows) == 0 {
			delete(s.replicaCPU, slug)
		}
	}
}

// has reports whether a ring currently exists for slug (test helper).
func (s *Store) has(slug string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	_, ok := s.rings[slug]
	return ok
}

// ring is a fixed-capacity circular buffer of Samples.
type ring struct {
	buf   []Sample
	start int // index of the oldest element
	size  int
}

func newRing(capacity int) *ring {
	return &ring{buf: make([]Sample, capacity)}
}

func (r *ring) push(s Sample) {
	n := len(r.buf)
	if r.size < n {
		r.buf[(r.start+r.size)%n] = s
		r.size++
		return
	}
	r.buf[r.start] = s
	r.start = (r.start + 1) % n
}

func (r *ring) newest() (Sample, bool) {
	if r.size == 0 {
		return Sample{}, false
	}
	n := len(r.buf)
	return r.buf[(r.start+r.size-1)%n], true
}
