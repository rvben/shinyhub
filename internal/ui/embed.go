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
	"strconv"
	"strings"
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
//
// When the request accepts gzip, is a plain GET/HEAD with no Range header,
// and the asset's extension isn't already-compressed (png, woff2, ...), the
// handler serves a gzip-compressed copy instead, compressed once per asset
// and cached for the life of the process (embedded assets never change). A
// Range request always falls through to the identity path below, so partial
// content is never served against a compressed representation. A request
// that doesn't accept gzip takes the exact pre-compression code path.
func Handler() http.Handler {
	fileServer := http.StripPrefix("/static/", http.FileServer(http.FS(Static())))
	dev := os.Getenv("SHINYHUB_DEV_STATIC") != ""
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if dev {
			w.Header().Set("Cache-Control", "no-store")
			fileServer.ServeHTTP(w, r)
			return
		}

		name := strings.TrimPrefix(r.URL.Path, "/static/")
		gzipEligible := (r.Method == http.MethodGet || r.Method == http.MethodHead) &&
			r.Header.Get("Range") == "" && AcceptsGzip(r) && isCompressibleAsset(name)
		if gzipEligible {
			if e, ok := gzipStaticAsset(name); ok {
				addVaryAcceptEncoding(w)
				w.Header().Set("Cache-Control", "no-cache")
				w.Header().Set("ETag", e.etag)
				if r.Header.Get("If-None-Match") == e.etag {
					w.WriteHeader(http.StatusNotModified)
					return
				}
				w.Header().Set("Content-Type", e.contentType)
				w.Header().Set("Content-Encoding", "gzip")
				w.Header().Set("Content-Length", strconv.Itoa(len(e.data)))
				if r.Method != http.MethodHead {
					_, _ = w.Write(e.data)
				}
				return
			}
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

// InvitationHandler serves the public account-activation page without loading
// the authenticated SPA or putting the invitation secret in an HTTP URL.
func InvitationHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		file, err := Static().Open("accept-invitation.html")
		if err != nil {
			http.Error(w, "Invitation page unavailable", 500)
			return
		}
		defer file.Close()
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self'; font-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'self'")
		if r.Method == http.MethodGet {
			_, _ = io.Copy(w, file)
		}
	})
}
