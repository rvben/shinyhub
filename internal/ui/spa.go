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

// IsUIPath reports whether path is a client-side-rendered SPA route that
// should be served the index.html shell.
func IsUIPath(path string) bool {
	switch path {
	case "/login", "/users", "/workers", "/audit-log":
		return true
	}
	return appDetailPath.MatchString(path)
}
