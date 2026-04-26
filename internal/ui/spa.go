package ui

import (
	"net/http"
	"regexp"

	slugpkg "github.com/rvben/shinyhub/internal/slug"
)

// appDetailPath matches /apps/<slug> and /apps/<slug>/<tab>. <slug> follows
// the canonical slug rule (see internal/slug). <tab> is an optional lowercase
// identifier; unknown tab names are still served — the client router treats
// unknown tabs as Overview.
var appDetailPath = regexp.MustCompile(`^/apps/` + slugpkg.Pattern + `(/[a-z-]+)?/?$`)

// SPAHandler returns an http.Handler that serves the embedded index.html for
// client-side-rendered UI paths (/apps/<slug>..., /users, /audit-log) and 404s
// everything else. It does not handle the bare "/" route — the caller keeps
// that handler because the embedded-FS behavior and SHINYHUB_DEV_STATIC are
// already in place there.
func SPAHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !isUIPath(r.URL.Path) {
			http.NotFound(w, r)
			return
		}
		http.ServeFileFS(w, r, Static(), "index.html")
	})
}

func isUIPath(path string) bool {
	switch path {
	case "/login", "/users", "/audit-log":
		return true
	}
	return appDetailPath.MatchString(path)
}
