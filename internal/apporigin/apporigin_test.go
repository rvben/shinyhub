package apporigin_test

import (
	"testing"

	"github.com/rvben/shinyhub/internal/apporigin"
)

// Admits decides what a host running untrusted application JavaScript may reach,
// and the Cloudflare demo Worker reimplements it in TypeScript to answer the
// rejects without waking its container. Both uses turn on the difference between
// an exact match and a prefix match, so the paths that sit just either side of
// that line are what this pins.
func TestAdmitsOnlyProxiedPaths(t *testing.T) {
	for _, tc := range []struct {
		path string
		want bool
		why  string
	}{
		{"/app/streamlit-demo/", true, "an application's own page"},
		{"/app/bookmarking-demo/websocket/", true, "an application's websocket"},
		{"/app/", true, "the bare proxy prefix"},
		{"/healthz", true, "the liveness probe"},
		{"/readyz", true, "the readiness probe"},
		{"/favicon.ico", true, "the platform favicon"},

		{"/", false, "the dashboard"},
		{"/api/auth/session", false, "the session endpoint"},
		{"/api/user/login", false, "the control plane"},
		{"/.env", false, "a secret-hunting probe"},

		// An exact path matched as a prefix, or a prefix matched as a substring,
		// both admit the control plane onto the application host.
		{"/healthz-probe", false, "an exact path used as a prefix"},
		{"/readyz.json", false, "an exact path with a suffix"},
		{"/favicon.ico.map", false, "the favicon with a suffix"},
		{"/app", false, "the prefix without its separator"},
		{"/apps", false, "a sibling of the prefix"},
		{"/application", false, "a word starting with the prefix"},
		{"/static/app/bundle.js", false, "the prefix as an interior substring"},
		{"/x/app/y", false, "the prefix anywhere but the start"},
	} {
		if got := apporigin.Admits(tc.path); got != tc.want {
			t.Errorf("Admits(%q) = %v, want %v (%s)", tc.path, got, tc.want, tc.why)
		}
	}
}

// Admits is the only way the two lists are read in production, so a path that
// appears in one of them and is then rejected means the list and the matcher
// have drifted apart.
func TestAdmitsAcceptsEveryPathItPublishes(t *testing.T) {
	for _, exact := range apporigin.ExactPaths() {
		if !apporigin.Admits(exact) {
			t.Errorf("ExactPaths lists %q but Admits rejects it", exact)
		}
	}
	for _, prefix := range apporigin.Prefixes() {
		if !apporigin.Admits(prefix + "demo/") {
			t.Errorf("Prefixes lists %q but Admits rejects a path under it", prefix)
		}
	}
}
