package ui

import "testing"

// TestIsUIPath guards the SPA fallback allowlist: client-side routes (including
// the new /tokens page) must be served the index.html shell on a deep link or
// reload, while API and unknown paths must not.
func TestIsUIPath(t *testing.T) {
	uiRoutes := []string{
		"/login", "/home", "/apps", "/users", "/workers", "/audit-log", "/tokens",
		"/apps/my-app", "/apps/my-app/logs",
	}
	for _, p := range uiRoutes {
		if !IsUIPath(p) {
			t.Errorf("IsUIPath(%q) = false, want true (SPA route must serve the shell)", p)
		}
	}

	notUI := []string{"/api/tokens", "/api/apps", "/static/app.js", "/app/my-app/", "/nope"}
	for _, p := range notUI {
		if IsUIPath(p) {
			t.Errorf("IsUIPath(%q) = true, want false (must not serve the SPA shell)", p)
		}
	}
}
