package jobs

import "time"

// RunSpanCount reports how many schedule.run spans are still open, reading the
// manager's real per-run map so leak checks observe production state.
func RunSpanCount(m *Manager) int {
	n := 0
	m.runSpans.Range(func(_, _ any) bool {
		n++
		return true
	})
	return n
}

// SetProducerCheckBudget replaces the environment check's build budget for
// the duration of a test.
func SetProducerCheckBudget(t interface{ Cleanup(func()) }, d time.Duration) {
	producerCheckBudget = func(time.Duration) time.Duration { return d }
	t.Cleanup(func() { producerCheckBudget = nil })
}
