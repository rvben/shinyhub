package ui

import (
	"net/http"
	"regexp"
)

// appDetailPath matches /apps/<slug> and /apps/<slug>/<tab>. <slug> must be a
// valid app slug: lowercase letters, digits, dashes, 1-63 chars, starting with
// a letter or digit. <tab> is an optional lowercase identifier; unknown tab
// names are still served — the client router treats unknown tabs as Overview.
var appDetailPath = regexp.MustCompile(`^/apps/[a-z0-9][a-z0-9-]{0,62}(/[a-z-]+)?/?$`)

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
