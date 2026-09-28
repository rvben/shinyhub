package jobs

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
