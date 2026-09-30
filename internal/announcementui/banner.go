// Package announcementui owns optional platform notices shared by console and
// hosted apps. It carries no privileged identity or management capability.
package announcementui

import (
	"crypto/sha256"
	_ "embed"
	"encoding/base64"
	"html"
	"net/http"
)

// ClientPath stays outside the versioned UI asset tree because this shared
// client is served directly by the platform, including in dev-static mode.
const ClientPath = "/announcements.js"

//go:embed assets/banner.js
var Script string

var CSPHash = func() string {
	sum := sha256.Sum256([]byte(Script))
	return "'sha256-" + base64.StdEncoding.EncodeToString(sum[:]) + "'"
}()

func Snippet(slug string) string {
	return `<script id="shinyhub-announcements-loader" data-announcements-url="/app/` + html.EscapeString(slug) + `/.shinyhub/announcements.json">` + Script + `</script>`
}
func ScriptHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	_, _ = w.Write([]byte(Script))
}
