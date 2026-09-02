// Package appnav owns the app switcher ShinyHub injects into every surface a
// visitor can land on underneath /app/: a running app's own HTML, the wait
// pages, the crashed and stopped pages, and the access-denied pages.
//
// It is its own package because those surfaces are rendered by two packages
// that must not import each other. internal/proxy renders the app, wait and
// down pages; internal/access renders the denied pages and wraps the proxy. The
// switcher has to look and behave identically on all of them, so the snippet,
// the asset and the splice live here and both callers reach for the same ones.
//
// The data the switcher renders never depends on the app being viewed, only on
// who is asking. That is what lets the same bar work on a 401: a visitor
// refused one app can still be offered the apps they do hold.
package appnav

import (
	"bytes"
	"crypto/sha256"
	_ "embed"
	"encoding/base64"
	"html"
)

// Script is the switcher client. See assets/nav.js for what it does and why it
// is safe to run inside an app this server did not write.
//
//go:embed assets/nav.js
var Script string

// CSPHash is the CSP source expression admitting Script as an inline block. It
// is computed from the embedded bytes rather than written by hand, so the hash
// cannot drift from the script that is actually served.
//
// The script body is identical on every surface and for every app - each
// per-page value rides on the tag's data-* attributes, which a hash does not
// cover - so this single source admits the switcher fleet-wide.
var CSPHash = "'sha256-" + base64.StdEncoding.EncodeToString(sha256Of(Script)) + "'"

func sha256Of(s string) []byte {
	sum := sha256.Sum256([]byte(s))
	return sum[:]
}

const (
	// ScriptID names the injected tag. The client falls back to looking itself
	// up by this id when document.currentScript is unavailable.
	ScriptID = "shinyhub-app-nav"

	// DataSuffix is the per-app path the switcher reads its app list from. The
	// leading dot namespaces the segment so it cannot collide with a route an
	// app legitimately serves, matching the ready probe's convention.
	//
	// It is per-app only because the app origin admits nothing outside /app/
	// (see cmd/shinyhub/app_origin.go); the response itself is a function of
	// the caller's identity and ignores the slug entirely.
	DataSuffix    = "/.shinyhub/nav.json"
	VersionSuffix = "/.shinyhub/version.json"
	SwitchSuffix  = "/.shinyhub/version/switch"
)

// DataURL is the switcher's data endpoint for slug.
func DataURL(slug string) string {
	return "/app/" + slug + DataSuffix
}

func VersionURL(slug string) string { return "/app/" + slug + VersionSuffix }
func SwitchURL(slug string) string  { return "/app/" + slug + SwitchSuffix }

// Snippet renders the <script> tag for one surface. Only the attributes vary;
// the body between the tags is always Script verbatim, which is what keeps one
// CSP hash valid everywhere.
//
// homeURL is where "All apps" points. It is the control origin's dashboard,
// which is a different host from the page whenever server.app_origin is set,
// and simply "/" when it is not. Per-app links stay relative on purpose: a
// visitor already on the app origin reaches a sibling app directly with the
// session cookie they hold, and a visitor on the control origin gets the
// launch redirect, so "/app/<slug>/" is correct from both.
func Snippet(slug, homeURL string) string {
	return SnippetWithName(slug, "", homeURL)
}

// SnippetWithName renders the switcher with the app's friendly display name.
// An empty name deliberately falls back to the slug in the client: access-
// denied pages must not disclose metadata for an app the caller cannot see.
func SnippetWithName(slug, name, homeURL string) string {
	return SnippetWithGeneration(slug, name, homeURL, "")
}

// SnippetWithGeneration stamps the exact generation that rendered an app
// page. The client compares this opaque value with the durable active token;
// it never infers ordering and never treats the token as authorization.
func SnippetWithGeneration(slug, name, homeURL, generation string) string {
	if homeURL == "" {
		homeURL = "/"
	}
	return `<script id="` + ScriptID + `"` +
		` data-nav-url="` + html.EscapeString(DataURL(slug)) + `"` +
		` data-current-slug="` + html.EscapeString(slug) + `"` +
		` data-current-name="` + html.EscapeString(name) + `"` +
		` data-home-url="` + html.EscapeString(homeURL) + `"` +
		` data-served-generation="` + html.EscapeString(generation) + `"` +
		` data-version-url="` + html.EscapeString(VersionURL(slug)) + `"` +
		` data-switch-url="` + html.EscapeString(SwitchURL(slug)) + `">` +
		Script +
		`</script>`
}

// SpliceIntoBody inserts snippet immediately before the document's final
// </body>, reporting whether an insertion point was found.
//
// The end of the document is the only correct place: the switcher must never
// run before the app's own bootstrap, and a script placed last cannot.
func SpliceIntoBody(page []byte, snippet string) ([]byte, bool) {
	idx := lastIndexFold(page, []byte("</body>"))
	if idx < 0 {
		return page, false
	}
	out := make([]byte, 0, len(page)+len(snippet))
	out = append(out, page[:idx]...)
	out = append(out, snippet...)
	out = append(out, page[idx:]...)
	return out, true
}

// lastIndexFold is bytes.LastIndex with ASCII case folding, which is all HTML
// tag names need.
func lastIndexFold(haystack, needle []byte) int {
	if len(needle) == 0 || len(haystack) < len(needle) {
		return -1
	}
	for i := len(haystack) - len(needle); i >= 0; i-- {
		if bytes.EqualFold(haystack[i:i+len(needle)], needle) {
			return i
		}
	}
	return -1
}
