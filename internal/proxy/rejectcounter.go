package proxy

import (
	"sync"
	"time"
)

// rejectRingBuckets is the number of 1-minute buckets per key. 15 covers a
// 10-minute query window with slack for the lazy stale-key sweep.
const rejectRingBuckets = 15

type rejectBucket struct {
	epoch  int64 // minute epoch (unix/60) this bucket currently represents
	counts map[RejectReason]uint64
}

type rejectRing struct {
	buckets [rejectRingBuckets]rejectBucket
}

// rejectCounter is a bounded, approximate, per-(key,reason) rolling counter.
// It holds a fixed ring of 1-minute buckets per key behind a single mutex,
// independent of the proxy route-table lock so recording never contends with
// routing. The window is quantised to whole minutes: a query for duration d
// sums the most recent ceil-ish d minutes of buckets, never counting buckets
// that have rolled over. Stale keys are swept lazily (no background goroutine).
type rejectCounter struct {
	mu        sync.Mutex
	nowFn     func() time.Time // injectable for tests
	keys      map[string]*rejectRing
	lastSweep int64 // minute epoch of the last sweep; sweeps run at most once/min
}

func newRejectCounter() *rejectCounter {
	return &rejectCounter{nowFn: time.Now, keys: make(map[string]*rejectRing)}
}

// record increments the (key, reason) count in the current minute bucket,
// lazily resetting that bucket if it has rolled over to a new minute.
func (c *rejectCounter) record(key string, reason RejectReason) {
	c.mu.Lock()
	defer c.mu.Unlock()
	epoch := c.nowFn().Unix() / 60
	c.sweepLocked(epoch)
	ring := c.keys[key]
	if ring == nil {
		ring = &rejectRing{}
		c.keys[key] = ring
	}
	b := &ring.buckets[epoch%rejectRingBuckets]
	if b.epoch != epoch {
		b.epoch = epoch
		b.counts = make(map[RejectReason]uint64)
	}
	b.counts[reason]++
}

// window returns the per-reason counts for key over roughly the last d
// (quantised to whole minutes). Reasons with a zero sum are omitted; a key with
// no live buckets returns nil.
func (c *rejectCounter) window(key string, d time.Duration) map[RejectReason]uint64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	epoch := c.nowFn().Unix() / 60
	c.sweepLocked(epoch)
	ring := c.keys[key]
	if ring == nil {
		return nil
	}
	minutes := int64(d / time.Minute)
	if minutes < 1 {
		minutes = 1
	}
	minEpoch := epoch - (minutes - 1)
	out := make(map[RejectReason]uint64)
	for i := range ring.buckets {
		b := &ring.buckets[i]
		if b.epoch >= minEpoch && b.epoch <= epoch {
			for r, n := range b.counts {
				out[r] += n
			}
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// forget drops all history for a key. Call only when the app is logically gone.
func (c *rejectCounter) forget(key string) {
	c.mu.Lock()
	delete(c.keys, key)
	c.mu.Unlock()
}

// sweepLocked removes keys whose entire ring has rolled out of range. It runs at
// most once per minute epoch to keep record/window cheap. Caller holds c.mu.
func (c *rejectCounter) sweepLocked(epoch int64) {
	if epoch == c.lastSweep {
		return
	}
	c.lastSweep = epoch
	oldest := epoch - (rejectRingBuckets - 1)
	for key, ring := range c.keys {
		stale := true
		for i := range ring.buckets {
			if ring.buckets[i].epoch >= oldest {
				stale = false
				break
			}
		}
		if stale {
			delete(c.keys, key)
		}
	}
}
