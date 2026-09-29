package ui

import (
	"bytes"
	"io/fs"
	"mime"
	"net/http"
	"os"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Versioned static assets.
//
// Every embedded asset is also reachable under /static/v/<version>/<path>,
// where <version> is the per-build content hash of the whole static tree
// (assetsETag). A versioned URL names exactly one build's bytes, so it is
// served with a one-year immutable cache policy: a returning visitor loads the
// dashboard's ~90 modules, stylesheets and fonts from cache with no network
// request at all, where the unversioned /static/ URLs cost one revalidation
// round trip each (a 304 per asset, most of them serialized behind the module
// import graph). A new release changes the version, so the new shell points at
// new URLs and nothing stale is ever reused.
//
// The shell (index.html) is what points at the versioned tree: ShellHTML
// rewrites its /static/ references and adds a modulepreload hint for every
// module in the entry's static import graph. Versioned JS and CSS are served
// with their own /static/ references rewritten the same way, so an absolute
// import ('/static/views/x.js') and a relative one ('./x.js') from a versioned
// module resolve to the same URL and the browser never instantiates a module
// twice. No import map is involved, so this holds on browsers without
// import-map support too.
//
// Under SHINYHUB_DEV_STATIC nothing is rewritten: files change on disk under a
// running server, so there is no stable version to name.

// versionedPrefix is the URL prefix of the versioned asset tree.
const versionedPrefix = "/static/v/"

// immutableCacheControl is the policy for a versioned asset of the running
// build.
const immutableCacheControl = "public, max-age=31536000, immutable"

// staticRefRe matches the lead-in of a root-relative /static/ reference in the
// three syntaxes the static tree uses to address its own assets: ES module
// specifiers (static import/export-from, side-effect import, dynamic import()),
// CSS url(), and HTML href/src attributes. Group 1 is everything before the
// "/static/", which the rewrite keeps.
var staticRefRe = regexp.MustCompile(`(\b(?:from|import)\s*\(?\s*['"]|\burl\(\s*['"]?|\b(?:href|src)\s*=\s*['"])/static/`)

// devStatic reports whether assets are served live from disk.
func devStatic() bool { return os.Getenv("SHINYHUB_DEV_STATIC") != "" }

// AssetVersion returns the version segment of the running build's versioned
// asset URLs, or "" under SHINYHUB_DEV_STATIC, where assets are not versioned.
func AssetVersion() string {
	if devStatic() {
		return ""
	}
	return strings.Trim(assetsETag(), `"`)
}

// rewriteStaticRefs points every /static/ reference in data at the versioned
// tree of version v.
func rewriteStaticRefs(data []byte, v string) []byte {
	return staticRefRe.ReplaceAll(data, []byte("${1}"+versionedPrefix+v+"/"))
}

// isRewrittenAsset reports whether name's references are rewritten when it is
// served from the versioned tree. Only the text formats that address other
// assets are; everything else is served byte-for-byte.
func isRewrittenAsset(name string) bool {
	switch strings.ToLower(path.Ext(name)) {
	case ".js", ".css":
		return true
	}
	return false
}

// moduleSpecRe matches a static module specifier (import/export ... from 'x',
// or a side-effect import 'x'). Dynamic import() is deliberately excluded: a
// lazily loaded module is lazy on purpose and must not be preloaded.
var moduleSpecRe = regexp.MustCompile(`(?m)(?:^|[;}\s])(?:import|export)\s+(?:[^'";]*?\s+from\s*)?['"]([^'"]+)['"]`)

// entryModuleRe matches the shell's module entry scripts.
var entryModuleRe = regexp.MustCompile(`<script\s+type="module"\s+src="(/static/[^"]+)"`)

// moduleGraph returns the root-relative URLs (under /static/) of every module
// reachable through static imports from entries, sorted, excluding entries.
func moduleGraph(fsys fs.FS, entries []string) []string {
	visited := map[string]bool{}
	found := map[string]bool{}
	var walk func(url string)
	walk = func(url string) {
		if visited[url] {
			return
		}
		visited[url] = true
		src, err := fs.ReadFile(fsys, strings.TrimPrefix(url, "/static/"))
		if err != nil {
			return // a specifier naming no file is never preloaded
		}
		found[url] = true
		for _, m := range moduleSpecRe.FindAllSubmatch(src, -1) {
			spec := string(m[1])
			switch {
			case strings.HasPrefix(spec, "/static/"):
				walk(path.Clean(spec))
			case strings.HasPrefix(spec, "./"), strings.HasPrefix(spec, "../"):
				walk(path.Join(path.Dir(url), spec))
			}
		}
	}
	for _, e := range entries {
		walk(e)
	}
	for _, e := range entries {
		delete(found, e)
	}
	out := make([]string, 0, len(found))
	for u := range found {
		out = append(out, u)
	}
	sort.Strings(out)
	return out
}

// modulePreloadRe matches an existing modulepreload link, so the generated
// hints skip modules the shell already preloads.
var modulePreloadRe = regexp.MustCompile(`<link\s+rel="modulepreload"\s+href="(/static/[^"]+)"`)

// addModulePreloads inserts a modulepreload hint, just before </head>, for
// every module in the static import graph of raw's module entries that raw does
// not already preload. Without them the browser discovers the graph one import
// level per round trip; with them it requests every module as the head parses.
func addModulePreloads(raw []byte, fsys fs.FS) []byte {
	var entries []string
	for _, m := range entryModuleRe.FindAllSubmatch(raw, -1) {
		entries = append(entries, string(m[1]))
	}
	have := map[string]bool{}
	for _, m := range modulePreloadRe.FindAllSubmatch(raw, -1) {
		have[string(m[1])] = true
	}
	var b bytes.Buffer
	for _, u := range moduleGraph(fsys, entries) {
		if have[u] {
			continue
		}
		b.WriteString(`  <link rel="modulepreload" href="` + u + "\">\n")
	}
	if b.Len() == 0 {
		return raw
	}
	i := bytes.Index(raw, []byte("</head>"))
	if i < 0 {
		return raw
	}
	out := make([]byte, 0, len(raw)+b.Len())
	out = append(out, raw[:i]...)
	out = append(out, b.Bytes()...)
	return append(out, raw[i:]...)
}

var (
	shellOnce sync.Once
	shellVal  []byte
	shellErr  error
)

// ShellHTML returns the SPA shell as served: the embedded index.html with a
// modulepreload hint for its whole module graph and every /static/ reference
// pointed at the versioned tree. It is computed once per process. Under
// SHINYHUB_DEV_STATIC it is re-read from disk on every call and returned
// unchanged, so live reload keeps working. Inline <script> bodies are not
// touched, so the CSP hashes of StaticShellInlineScriptSources still match.
func ShellHTML() ([]byte, error) {
	if devStatic() {
		return fs.ReadFile(Static(), "index.html")
	}
	shellOnce.Do(func() {
		raw, err := fs.ReadFile(Static(), "index.html")
		if err != nil {
			shellErr = err
			return
		}
		shellVal = rewriteStaticRefs(addModulePreloads(raw, Static()), AssetVersion())
	})
	return shellVal, shellErr
}

// versionedAsset is a memoized versioned asset: its identity bytes (rewritten
// for JS/CSS), the resolved Content-Type, and a lazily built gzip form.
type versionedAsset struct {
	data        []byte
	contentType string
	gzipOnce    sync.Once
	gzip        gzipEntry
}

var (
	versionedMu    sync.Mutex
	versionedCache = map[string]*versionedAsset{}
)

// loadVersionedAsset returns the served form of the embedded asset at name,
// computed once per process. ok is false for a missing file or a directory.
func loadVersionedAsset(name string) (*versionedAsset, bool) {
	versionedMu.Lock()
	a, ok := versionedCache[name]
	versionedMu.Unlock()
	if ok {
		return a, true
	}
	if !fs.ValidPath(name) {
		return nil, false
	}
	fsys := Static()
	if st, err := fs.Stat(fsys, name); err != nil || st.IsDir() {
		return nil, false
	}
	data, err := fs.ReadFile(fsys, name)
	if err != nil {
		return nil, false
	}
	if isRewrittenAsset(name) {
		data = rewriteStaticRefs(data, AssetVersion())
	}
	ct := mime.TypeByExtension(path.Ext(name))
	if ct == "" {
		ct = http.DetectContentType(data)
	}
	a = &versionedAsset{data: data, contentType: ct}
	versionedMu.Lock()
	if prev, ok := versionedCache[name]; ok {
		a = prev
	} else {
		versionedCache[name] = a
	}
	versionedMu.Unlock()
	return a, true
}

// serveVersioned serves a request under /static/v/. The version segment picks
// the cache policy, never the bytes: only the running build's assets exist, so
// a request naming another version (a page loaded before an upgrade that now
// lazily requests a module) gets the current bytes with a revalidation policy,
// never an immutable one that would pin them under the wrong version.
func serveVersioned(w http.ResponseWriter, r *http.Request, fileServer http.Handler) {
	rest := strings.TrimPrefix(r.URL.Path, versionedPrefix)
	version, name, ok := strings.Cut(rest, "/")
	if !ok || version == "" || name == "" {
		http.NotFound(w, r)
		return
	}
	if devStatic() {
		r2 := r.Clone(r.Context())
		r2.URL.Path = "/static/" + name
		w.Header().Set("Cache-Control", "no-store")
		fileServer.ServeHTTP(w, r2)
		return
	}
	a, ok := loadVersionedAsset(name)
	if !ok {
		http.NotFound(w, r)
		return
	}
	current := version == AssetVersion()
	if current {
		w.Header().Set("Cache-Control", immutableCacheControl)
	} else {
		w.Header().Set("Cache-Control", "no-cache")
	}

	if (r.Method == http.MethodGet || r.Method == http.MethodHead) &&
		r.Header.Get("Range") == "" && AcceptsGzip(r) && isCompressibleAsset(name) {
		a.gzipOnce.Do(func() {
			a.gzip = gzipEntry{data: gzipCompress(a.data), etag: gzipETag(contentETag(a.data))}
		})
		addVaryAcceptEncoding(w)
		w.Header().Set("ETag", a.gzip.etag)
		if r.Header.Get("If-None-Match") == a.gzip.etag {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		w.Header().Set("Content-Type", a.contentType)
		w.Header().Set("Content-Encoding", "gzip")
		w.Header().Set("Content-Length", strconv.Itoa(len(a.gzip.data)))
		if r.Method != http.MethodHead {
			_, _ = w.Write(a.gzip.data)
		}
		return
	}
	if isCompressibleAsset(name) {
		addVaryAcceptEncoding(w)
	}
	w.Header().Set("ETag", contentETag(a.data))
	w.Header().Set("Content-Type", a.contentType)
	http.ServeContent(w, r, name, time.Time{}, bytes.NewReader(a.data))
}
