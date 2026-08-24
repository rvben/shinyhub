package main

import (
	"errors"
	"net/http"

	"github.com/rvben/shinyhub/internal/db"
	"github.com/rvben/shinyhub/internal/favicon"
)

type appFaviconStore interface {
	GetAppBySlug(slug string) (*db.App, error)
	GetAppIcon(slug string) (mime string, data []byte, err error)
}

// appFaviconHandler serves the app identity selected in ShinyHub. The access
// middleware wraps this handler in production, so private app metadata remains
// private. Emoji wins over an uploaded image, matching every in-product avatar;
// an iconless app inherits the effective platform favicon.
func appFaviconHandler(st appFaviconStore, platform http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		slug := r.PathValue("slug")
		app, err := st.GetAppBySlug(slug)
		if err != nil || app == nil {
			if errors.Is(err, db.ErrNotFound) {
				http.NotFound(w, r)
				return
			}
			http.Error(w, "internal server error", http.StatusInternalServerError)
			return
		}

		if app.IconEmoji != "" {
			favicon.Write(w, r, "image/svg+xml", favicon.EmojiSVG(app.IconEmoji))
			return
		}
		if app.IconMime != "" {
			mime, data, iconErr := st.GetAppIcon(slug)
			if iconErr == nil {
				favicon.Write(w, r, mime, data)
				return
			}
			if !errors.Is(iconErr, db.ErrNotFound) {
				http.Error(w, "internal server error", http.StatusInternalServerError)
				return
			}
		}

		platform.ServeHTTP(w, r)
	})
}
