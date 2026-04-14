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
	sub, err := fs.Sub(static, "static")
	if err != nil {
		panic("ui: embedded static directory missing: " + err.Error())
	}
	return http.StripPrefix("/static/", http.FileServer(http.FS(sub)))
}

// Static returns the embedded static filesystem for direct serving.
func Static() fs.FS {
	sub, err := fs.Sub(static, "static")
	if err != nil {
		panic("ui: embedded static directory missing: " + err.Error())
	}
	return sub
}
