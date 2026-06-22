package db

import (
	"fmt"
	"time"
)

// RateLimitAllow records an attempt for (bucket, key) and reports whether it is
// within limit for the current fixed window. It is a shared limiter: every
// instance pointed at the same database increments one combined counter, so a
// load-balanced deployment enforces a global cap per key instead of
// limit-per-instance.
//
// The check-and-increment is a single atomic UPSERT: the first attempt in a
// window inserts count=1; later attempts increment only while count < limit. The
// row lock that ON CONFLICT DO UPDATE takes on the counter row serializes
// concurrent attempts on the same key, so even a parallel burst cannot exceed
// the cap (no read-then-write race). RowsAffected is 0 exactly when the
// conditional update was suppressed (count already at limit), i.e. denied.
//
// The window is fixed, not sliding: a key may make up to limit attempts in each
// window, which permits up to 2*limit across a window boundary. That is the
// standard, well-understood property of a fixed-window counter and is fine for
// the abuse-dampening goal here.
//
// The window boundary is computed from the database clock (nowEpochMillis), not
// the calling instance's clock, so clock skew between HA replicas cannot split
// one client's attempts across different window rows.
func (s *Store) RateLimitAllow(bucket, key string, limit int, window time.Duration) (bool, error) {
	defer s.timed("RateLimitAllow")()
	windowMS := window.Milliseconds()
	if windowMS <= 0 {
		windowMS = 1
	}
	// window_start_ms = floor(db_now_ms / windowMS) * windowMS, all integer math.
	query := fmt.Sprintf(
		`INSERT INTO rate_limit_counters (bucket, rl_key, window_start_ms, count)
		 VALUES (?, ?, ((%s) / ?) * ?, 1)
		 ON CONFLICT (bucket, rl_key, window_start_ms)
		 DO UPDATE SET count = rate_limit_counters.count + 1
		 WHERE rate_limit_counters.count < ?`, s.d.nowEpochMillis())
	res, err := s.db.Exec(query, bucket, key, windowMS, windowMS, limit)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, err
	}
	return n > 0, nil
}

// PruneRateLimitCounters deletes counter rows for windows that started before
// the retention cutoff and returns the number removed. Elapsed windows are
// irrelevant to the current count but must be swept so the table cannot grow
// without bound. Retention only needs to exceed the longest limiter window.
//
// The cutoff is computed from the database clock (the same source RateLimitAllow
// buckets with), so a skewed maintenance-owner clock cannot delete a window that
// is still current and reset the shared limiter.
func (s *Store) PruneRateLimitCounters(retention time.Duration) (int64, error) {
	defer s.timed("PruneRateLimitCounters")()
	query := fmt.Sprintf(`DELETE FROM rate_limit_counters WHERE window_start_ms < ((%s) - ?)`, s.d.nowEpochMillis())
	res, err := s.db.Exec(query, retention.Milliseconds())
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}
