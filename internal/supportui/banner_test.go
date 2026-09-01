package supportui

import (
	"strings"
	"testing"
	"time"
)

func TestInactivePageUsesTerminalLanguage(t *testing.T) {
	page := InactivePage("sales", "admin", "alice", time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC))
	for _, want := range []string{"Support session ended", "Clear session and return", "The support identity was", "Its original deadline was"} {
		if !strings.Contains(page, want) {
			t.Fatalf("inactive page missing %q: %s", want, page)
		}
	}
	for _, contradictory := range []string{"Support session paused", "End support session", "active support identity", "expires automatically"} {
		if strings.Contains(page, contradictory) {
			t.Fatalf("inactive page contains active-state copy %q: %s", contradictory, page)
		}
	}
}
