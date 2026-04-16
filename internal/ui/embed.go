package ui

import (
	"embed"
	"io/fs"
	"net/http"
	"os"
)

//go:embed static
var embedded embed.FS

// Static returns the filesystem serving UI assets. If SHINYHUB_DEV_STATIC is
// set to a directory path, assets are served from disk so edits appear on
// page refresh without rebuilding the binary. Otherwise the compiled-in
// embed.FS is used.
func Static() fs.FS {
	if dir := os.Getenv("SHINYHUB_DEV_STATIC"); dir != "" {
		return os.DirFS(dir)
	}
	sub, err := fs.Sub(embedded, "static")
	if err != nil {
		panic("ui: embedded static directory missing: " + err.Error())
	}
	return sub
}

// Handler returns an HTTP handler that serves the Static() FS rooted at
// /static/. Register it as mux.Handle("/static/", ui.Handler()).
func Handler() http.Handler {
	return http.StripPrefix("/static/", http.FileServer(http.FS(Static())))
}
