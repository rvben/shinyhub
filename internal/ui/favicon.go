package ui

import (
	"io/fs"
	"net/http"
	"path"
	"strings"

	"github.com/rvben/shinyhub/internal/config"
	"github.com/rvben/shinyhub/internal/favicon"
)

// FaviconHandler serves the effective platform favicon from one stable URL.
// That URL is used by ShinyHub's standalone pages and as the browser's implicit
// /favicon.ico fallback for operator landing pages.
//
// When no favicon override is configured, every branding combination falls
// back to the stock ShinyHub icon. A custom title or logo changes the page
// identity without leaving browser tabs blank.
func FaviconHandler(b config.BrandingConfig) http.Handler {
	resolved := b.ResolvedAssets()
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if b.Favicon != "" {
			low := strings.ToLower(b.Favicon)
			if strings.HasPrefix(low, "http://") || strings.HasPrefix(low, "https://") {
				w.Header().Set("Cache-Control", "no-cache")
				http.Redirect(w, r, b.Favicon, http.StatusTemporaryRedirect)
				return
			}
			if abs, ok := resolved[path.Base(b.Favicon)]; ok {
				w.Header().Set("Cache-Control", "no-cache")
				http.ServeFile(w, r, abs)
				return
			}
			http.NotFound(w, r)
			return
		}

		data, err := fs.ReadFile(Static(), "brand/favicon.ico")
		if err != nil {
			http.NotFound(w, r)
			return
		}
		favicon.Write(w, r, "image/x-icon", data)
	})
}
