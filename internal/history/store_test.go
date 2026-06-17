package history

import (
	"sync"
	"testing"
	"time"
)

func sample(ts int64, cpu float64, rss, sessions int64, instances int) Sample {
	return Sample{TS: ts, CPU: cpu, RSS: rss, Sessions: sessions, Instances: instances}
}

func TestStoreAppendAndSeriesColumnar(t *testing.T) {
	s := NewStore(12*time.Hour, 15*time.Second)
	now := int64(1_000_000)
	s.Append("demo", sample(now-30, 10, 100, 1, 1))
	s.Append("demo", sample(now-15, 20, 200, 2, 2))
	s.Append("demo", sample(now, 30, 300, 3, 2))

	got := s.Series("demo", now)
	if want := []int64{now - 30, now - 15, now}; !eqI64(got.TS, want) {
		t.Errorf("ts = %v, want %v", got.TS, want)
	}
	if want := []float64{10, 20, 30}; !eqF64(got.CPU, want) {
		t.Errorf("cpu = %v, want %v", got.CPU, want)
	}
	if want := []int64{100, 200, 300}; !eqI64(got.RSS, want) {
		t.Errorf("rss = %v, want %v", got.RSS, want)
	}
	if want := []int64{1, 2, 3}; !eqI64(got.Sessions, want) {
		t.Errorf("sessions = %v, want %v", got.Sessions, want)
	}
	if want := []int{1, 2, 2}; !eqInt(got.Instances, want) {
		t.Errorf("instances = %v, want %v", got.Instances, want)
	}
}

func TestStoreRingEvictsOldestAtCapacity(t *testing.T) {
	// window/interval = 60/20 = capacity 3.
	s := NewStore(60*time.Second, 20*time.Second)
	now := int64(1_000_000)
	for i := range 5 {
		s.Append("demo", sample(now-int64(4-i), float64(i), 0, 0, 1))
	}
	got := s.Series("demo", now)
	// Only the last 3 of the 5 appends survive (oldest two evicted).
	if want := []float64{2, 3, 4}; !eqF64(got.CPU, want) {
		t.Errorf("cpu after eviction = %v, want %v", got.CPU, want)
	}
}

func TestStoreCapacityClampedAtMax(t *testing.T) {
	// 7 days / 1s would be 604800 points; must clamp to maxPointsPerApp.
	s := NewStore(7*24*time.Hour, time.Second)
	if s.capacity != maxPointsPerApp {
		t.Errorf("capacity = %d, want clamp to %d", s.capacity, maxPointsPerApp)
	}
}

func TestStoreCapacityAtLeastOne(t *testing.T) {
	// Pathological window < interval must still yield a usable ring.
	s := NewStore(time.Second, time.Hour)
	if s.capacity < 1 {
		t.Errorf("capacity = %d, want >= 1", s.capacity)
	}
}

func TestStoreSeriesExcludesSamplesOlderThanWindow(t *testing.T) {
	s := NewStore(time.Hour, 15*time.Second)
	now := int64(1_000_000)
	s.Append("demo", sample(now-7200, 1, 0, 0, 1)) // 2h old, outside 1h window
	s.Append("demo", sample(now-1800, 2, 0, 0, 1)) // 30m old, inside
	s.Append("demo", sample(now, 3, 0, 0, 1))

	got := s.Series("demo", now)
	if want := []float64{2, 3}; !eqF64(got.CPU, want) {
		t.Errorf("cpu = %v, want %v (older-than-window sample must be dropped)", got.CPU, want)
	}
}

func TestStoreSeriesUnknownSlugReturnsEmptyNonNil(t *testing.T) {
	s := NewStore(time.Hour, 15*time.Second)
	got := s.Series("nope", 1_000_000)
	if got.TS == nil || got.CPU == nil || got.RSS == nil || got.Sessions == nil || got.Instances == nil {
		t.Fatalf("series slices must be non-nil (marshal as []), got %+v", got)
	}
	if len(got.TS) != 0 {
		t.Errorf("want empty series, got %d points", len(got.TS))
	}
}

func TestStoreGCDropsStaleRingKeepsFresh(t *testing.T) {
	s := NewStore(time.Hour, 15*time.Second)
	now := int64(1_000_000)
	s.Append("stale", sample(now-7200, 1, 0, 0, 1)) // newest is 2h old
	s.Append("fresh", sample(now-60, 1, 0, 0, 1))   // newest is 1m old

	s.GC(now)

	if s.has("stale") {
		t.Error("stale ring (newest older than window) should have been GC'd")
	}
	if !s.has("fresh") {
		t.Error("fresh ring should have been retained")
	}
}

func TestStorePerSlugIsolation(t *testing.T) {
	s := NewStore(time.Hour, 15*time.Second)
	now := int64(1_000_000)
	s.Append("a", sample(now, 1, 0, 0, 1))
	s.Append("b", sample(now, 2, 0, 0, 1))
	if got := s.Series("a", now); !eqF64(got.CPU, []float64{1}) {
		t.Errorf("a cpu = %v, want [1]", got.CPU)
	}
	if got := s.Series("b", now); !eqF64(got.CPU, []float64{2}) {
		t.Errorf("b cpu = %v, want [2]", got.CPU)
	}
}

func TestStoreWindowAndIntervalSeconds(t *testing.T) {
	s := NewStore(12*time.Hour, 15*time.Second)
	if s.WindowSeconds() != 43200 {
		t.Errorf("window_seconds = %d, want 43200", s.WindowSeconds())
	}
	if s.IntervalSeconds() != 15 {
		t.Errorf("interval_seconds = %d, want 15", s.IntervalSeconds())
	}
}

// TestStoreConcurrentAppendAndReadIsRaceFree exercises the writer/reader split
// the design relies on: one writer (collector) and many readers (HTTP handlers).
// Run under -race to catch a regression in the RWMutex discipline.
func TestStoreConcurrentAppendAndReadIsRaceFree(t *testing.T) {
	s := NewStore(time.Hour, 15*time.Second)
	var wg sync.WaitGroup
	now := int64(2_000_000)

	wg.Add(1)
	go func() { // single writer
		defer wg.Done()
		for i := range 500 {
			s.Append("demo", sample(now+int64(i), float64(i), int64(i), int64(i), 1))
			if i%50 == 0 {
				s.GC(now + int64(i))
			}
		}
	}()

	for r := range 4 { // concurrent readers
		wg.Add(1)
		go func(seed int) {
			defer wg.Done()
			for i := range 500 {
				_ = s.Series("demo", now+int64(seed+i))
			}
		}(r)
	}
	wg.Wait()
}

func eqI64(a, b []int64) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func eqF64(a, b []float64) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func eqInt(a, b []int) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
