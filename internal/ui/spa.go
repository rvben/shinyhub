package ui

import (
	"regexp"

	slugpkg "github.com/rvben/shinyhub/internal/slug"
)

// appDetailPath matches /apps/<slug> and /apps/<slug>/<tab>. <slug> follows
// the canonical slug rule (see internal/slug). <tab> is an optional lowercase
// identifier; unknown tab names are still served - the client router treats
// unknown tabs as Overview.
var appDetailPath = regexp.MustCompile(`^/apps/` + slugpkg.Pattern + `(/[a-z-]+)?/?$`)

// ExactUIRoutes is the single source of truth for the client-side SPA routes
// that are matched by an EXACT path (as opposed to the /apps/<slug> pattern).
// Both IsUIPath (the shell-fallback guard) and the server's mux registrations
// derive from this list so the two cannot drift - a route present in one but not
// the other silently 404s on deep links/reloads. To add an SPA page, add it here.
func ExactUIRoutes() []string {
	return []string{"/login", "/home", "/apps", "/users", "/workers", "/audit-log", "/tokens"}
}

// IsUIPath reports whether path is a client-side-rendered SPA route that
// should be served the index.html shell.
func IsUIPath(path string) bool {
	for _, r := range ExactUIRoutes() {
		if path == r {
			return true
		}
	}
	return appDetailPath.MatchString(path)
}
