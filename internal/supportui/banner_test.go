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

func TestGuardOnlyPageStates(t *testing.T) {
	deadline := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	live := &GuardedSession{AppSlug: "sales", Actor: "admin", Subject: "alice", ExpiresAt: deadline, Active: true}

	elsewhere := GuardOnlyPage("other", live)
	for _, want := range []string{"Support session paused", `href="/app/sales/"`, `action="/app/sales/.shinyhub/support-session/stop"`,
		"End support session", "active support identity is <strong>alice</strong>", "<strong>admin</strong> remains", "2026-09-01T12:00:00Z"} {
		if !strings.Contains(elsewhere, want) {
			t.Fatalf("page on another app missing %q: %s", want, elsewhere)
		}
	}

	bound := GuardOnlyPage("sales", live)
	if !strings.Contains(bound, "app-scoped cookie is missing") || strings.Contains(bound, "<form") || strings.Contains(bound, `href="/app/sales/"`) {
		t.Fatalf("page on the bound app must explain the missing cookie and offer no dead controls: %s", bound)
	}

	ended := GuardOnlyPage("other", &GuardedSession{AppSlug: "sales", Actor: "admin", Subject: "alice", ExpiresAt: deadline, Active: false})
	for _, want := range []string{"Support session ended", "original deadline", "2026-09-01T12:00:00Z", "<strong>alice</strong>", "<strong>admin</strong> was"} {
		if !strings.Contains(ended, want) {
			t.Fatalf("ended page missing %q: %s", want, ended)
		}
	}
	for _, stale := range []string{"<form", "Return to the support session", "remains the administrator"} {
		if strings.Contains(ended, stale) {
			t.Fatalf("ended page carries live-state control %q: %s", stale, ended)
		}
	}

	unknown := GuardOnlyPage("other", nil)
	if !strings.Contains(unknown, "Support session guard active") || strings.Contains(unknown, "<form") || strings.Contains(unknown, "<strong>") {
		t.Fatalf("unknown-session page must be generic and control-free: %s", unknown)
	}
}

func TestGuardOnlyPageEscapesUntrustedValues(t *testing.T) {
	hostile := &GuardedSession{AppSlug: `x"><script>`, Actor: "<b>admin</b>", Subject: "alice&co", ExpiresAt: time.Now(), Active: true}
	page := GuardOnlyPage("other", hostile)
	for _, raw := range []string{`x"><script>`, "<b>admin</b>", "alice&co<"} {
		if strings.Contains(page, raw) {
			t.Fatalf("unescaped value %q in page: %s", raw, page)
		}
	}
	if !strings.Contains(page, `href="/app/x&#34;&gt;&lt;script&gt;/"`) {
		t.Fatalf("bound slug not escaped in link: %s", page)
	}
}
