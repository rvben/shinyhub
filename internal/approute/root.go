// Package approute provides shared routing behavior for mounted apps.
package approute

import "net/http"

// RedirectRoot canonicalizes an exact /app/<slug> request and reports whether
// it handled the response. Callers must first confirm that the app exists.
// The host-relative Location preserves the escaped path and raw query, and
// 308 preserves the method and body when the client follows the redirect.
func RedirectRoot(w http.ResponseWriter, r *http.Request, slug string) bool {
	if slug == "" || r.URL.Path != "/app/"+slug {
		return false
	}
	location := r.URL.EscapedPath() + "/"
	if r.URL.RawQuery != "" || r.URL.ForceQuery {
		location += "?" + r.URL.RawQuery
	}
	http.Redirect(w, r, location, http.StatusPermanentRedirect)
	return true
}
