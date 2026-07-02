package ui

import (
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"sync"
)

//go:embed static
var embedded embed.FS

// assetsETagOnce computes a single ETag over the whole embedded static tree,
// once. Because all assets ship together in one binary, a per-build content
// hash is a correct shared validator: it changes exactly when a release changes
// any asset, so browsers revalidate and refetch once per release and get 304s
// in between.
var (
	assetsETagOnce sync.Once
	assetsETagVal  string
)

func assetsETag() string {
	assetsETagOnce.Do(func() {
		h := sha256.New()
		sub, err := fs.Sub(embedded, "static")
		if err != nil {
			return
		}
		_ = fs.WalkDir(sub, ".", func(path string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return err
			}
			fmt.Fprintf(h, "%s\x00", path)
			f, err := sub.Open(path)
			if err != nil {
				return err
			}
			defer f.Close()
			_, _ = io.Copy(h, f)
			return nil
		})
		assetsETagVal = `"` + hex.EncodeToString(h.Sum(nil))[:16] + `"`
	})
	return assetsETagVal
}

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
//
// Asset URLs are unversioned, so the handler sets a revalidation cache policy:
// a per-build content ETag plus Cache-Control: no-cache, letting browsers cache
// but revalidate (a matching If-None-Match yields a cheap 304), and refetch
// automatically when a new release changes the assets. In dev-static mode the
// files change on disk under a running server, so caching is disabled outright.
func Handler() http.Handler {
	fileServer := http.StripPrefix("/static/", http.FileServer(http.FS(Static())))
	dev := os.Getenv("SHINYHUB_DEV_STATIC") != ""
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if dev {
			w.Header().Set("Cache-Control", "no-store")
			fileServer.ServeHTTP(w, r)
			return
		}
		etag := assetsETag()
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("ETag", etag)
		if r.Header.Get("If-None-Match") == etag {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		fileServer.ServeHTTP(w, r)
	})
}
