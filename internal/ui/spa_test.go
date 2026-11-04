package ui

import "testing"

// TestIsUIPath guards the SPA fallback allowlist: client-side routes (including
// the new /tokens page) must be served the index.html shell on a deep link or
// reload, while API and unknown paths must not.
func TestIsUIPath(t *testing.T) {
	uiRoutes := []string{
		"/login", "/home", "/launchpad", "/apps", "/identity", "/users", "/workers", "/audit-log", "/tokens",
		"/apps/my-app", "/apps/my-app/logs", "/projects/team-dashboards",
		"/apps/my-app/settings/general", "/apps/my-app/settings/environment",
		"/apps/my-app/settings/resources", "/apps/my-app/settings/scaling",
		"/apps/my-app/settings/access", "/apps/my-app/settings/privacy/",
		"/apps/my-app/schedules/1", "/apps/my-app/schedules/123/",
	}
	for _, p := range uiRoutes {
		if !IsUIPath(p) {
			t.Errorf("IsUIPath(%q) = false, want true (SPA route must serve the shell)", p)
		}
	}

	notUI := []string{"/api/tokens", "/api/apps", "/static/app.js", "/app/my-app/", "/nope",
		"/apps/my-app/logs/extra", "/apps/my-app/settings/access/extra",
		"/apps/my-app/schedules/0", "/apps/my-app/schedules/-1",
		"/apps/my-app/schedules/nope", "/apps/my-app/schedules/1/extra", "/apps/INVALID/settings/general"}
	for _, p := range notUI {
		if IsUIPath(p) {
			t.Errorf("IsUIPath(%q) = true, want false (must not serve the SPA shell)", p)
		}
	}
}
