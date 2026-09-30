package lifecycle

import (
	"strings"
	"time"
)

// UpdateSessionLifetime extends or disables already-armed worker deadlines.
// Reductions apply to new workers: saving a shorter limit never evicts an
// existing session. The caller holds the app operation lock, like Spawn.
func (s *ElasticSpawner) UpdateSessionLifetime(slug string, seconds int) {
	s.lifetimeMu.Lock()
	defer s.lifetimeMu.Unlock()
	limit := time.Duration(seconds) * time.Second
	s.lifetimeTimers.Range(func(key, value any) bool {
		if !strings.HasPrefix(key.(string), slug+"/") {
			return true
		}
		old := value.(*elasticLifetime)
		if seconds <= 0 {
			old.timer.Stop()
			s.lifetimeTimers.Delete(key)
			return true
		}
		if limit <= old.limit {
			return true
		}
		old.timer.Stop()
		replacement := &elasticLifetime{epoch: old.epoch, started: old.started, limit: limit, slot: old.slot}
		// Slot identity comes from the same key cancellation uses. Replacing the
		// map entry also fences an old expiry already queued behind an operation.
		replacement.timer = time.AfterFunc(time.Until(old.started.Add(limit)), func() {
			s.expireLifetime(slug, old.slot, replacement)
		})
		s.lifetimeTimers.Store(key, replacement)
		return true
	})
}
