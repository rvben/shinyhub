package appnav

import (
	"encoding/hex"
	"io"
	"net/http"
	"strconv"
)

const assetPrefix = "/app/.shinyhub/nav/"

// AssetRoute keeps the asset reachable from the dedicated app origin.
const AssetRoute = assetPrefix + "{asset}"

var assetPath = assetPrefix + hex.EncodeToString(sha256Of(Script)) + ".js"

// ServeAsset serves the embedded script at its content-addressed URL.
func ServeAsset(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != assetPath {
		w.Header().Set("Cache-Control", "no-store")
		http.NotFound(w, r)
		return
	}
	h := w.Header()
	h.Set("Content-Type", "text/javascript; charset=utf-8")
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Cross-Origin-Resource-Policy", "same-origin")
	// Forward authentication can attach a session cookie to this response.
	h.Set("Cache-Control", "private, max-age=31536000, immutable")
	h.Set("Content-Length", strconv.Itoa(len(Script)))
	if r.Method != http.MethodHead {
		_, _ = io.WriteString(w, Script)
	}
}
