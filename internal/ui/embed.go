package ui

import (
	"embed"
	"io/fs"
	"net/http"
)

//go:embed static
var static embed.FS

// Handler returns an HTTP handler that serves the embedded static files
// rooted at /static/. Register it as mux.Handle("/static/", ui.Handler()).
func Handler() http.Handler {
	sub, _ := fs.Sub(static, "static")
	return http.StripPrefix("/static/", http.FileServer(http.FS(sub)))
}

// Static returns the embedded static filesystem for direct serving.
func Static() fs.FS {
	sub, _ := fs.Sub(static, "static")
	return sub
}
