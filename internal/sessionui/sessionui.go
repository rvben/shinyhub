// Package sessionui shares browser renewal between the dashboard and hosted
// applications without exposing dashboard APIs on the isolated app origin.
package sessionui

import (
	"crypto/sha256"
	_ "embed"
	"encoding/base64"
	"html"
	"strconv"
	"strings"

	"github.com/rvben/shinyhub/internal/ui"
)

const DataSuffix = "/.shinyhub/session.json"

//go:embed assets/session.js
var loader string

// The canonical controller is an ES module in the dashboard and a private
// function in this inline script. Every app receives identical script bytes so
// CSP can admit exactly this script by its hash.
var Script = "(function () {\n" + strings.Replace(ui.BrowserSessionController, "export function createSessionController", "function createSessionController", 1) + loader + "\n})();"

var CSPHash = func() string {
	h := sha256.Sum256([]byte(Script))
	return "'sha256-" + base64.StdEncoding.EncodeToString(h[:]) + "'"
}()

func Snippet(slug, signInURL string, userID int64) string {
	return `<script id="shinyhub-browser-session" data-session-url="` + html.EscapeString("/app/"+slug+DataSuffix) +
		`" data-sign-in-url="` + html.EscapeString(signInURL) + `" data-user-id="` + strconv.FormatInt(userID, 10) + `">` + Script + `</script>`
}
