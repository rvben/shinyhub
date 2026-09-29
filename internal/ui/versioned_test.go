package ui_test

import (
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/rvben/shinyhub/internal/ui"
)

// serve issues one request against ui.Handler and returns the recorder.
func serve(t *testing.T, target string, header map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, target, nil)
	for k, v := range header {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	ui.Handler().ServeHTTP(rec, req)
	return rec
}

// versioned returns the versioned URL of the static asset at name.
func versioned(t *testing.T, name string) string {
	t.Helper()
	v := ui.AssetVersion()
	if v == "" {
		t.Fatal("AssetVersion is empty outside dev-static mode")
	}
	return "/static/v/" + v + "/" + name
}

// unversionedModuleRefRe matches a module specifier, dynamic import, CSS url()
// or href/src attribute that still addresses the unversioned /static/ tree.
// The fetch() of a JSON data file is not a module reference and is not
// matched: a stray unversioned fetch costs a revalidation, never a second
// module instance.
var unversionedModuleRefRe = regexp.MustCompile(`(?:\b(?:from|import)\s*\(?\s*['"]|\burl\(\s*['"]?|\b(?:href|src)\s*=\s*['"])/static/(?:[^v]|v[^/])`)

func TestShellHTML_PointsEveryAssetAtTheVersionedTree(t *testing.T) {
	shell, err := ui.ShellHTML()
	if err != nil {
		t.Fatalf("ShellHTML: %v", err)
	}
	if m := unversionedModuleRefRe.Find(shell); m != nil {
		t.Errorf("shell still references the unversioned tree: %q", m)
	}
	for _, want := range []string{
		`<script type="module" src="` + versioned(t, "app.js") + `"`,
		`href="` + versioned(t, "style.css") + `"`,
		`href="` + versioned(t, "fonts/fonts.css") + `"`,
	} {
		if !strings.Contains(string(shell), want) {
			t.Errorf("shell missing %q", want)
		}
	}
	// Positive control for the regex above: the raw shell does carry
	// unversioned references, so a pattern that could never match is caught.
	raw, err := fs.ReadFile(ui.Static(), "index.html")
	if err != nil {
		t.Fatal(err)
	}
	if unversionedModuleRefRe.Find(raw) == nil {
		t.Fatal("control: the raw shell must match unversionedModuleRefRe, or the check above is vacuous")
	}
}

// staticImportRe finds the static import specifiers of a served module. It is
// written independently of the production graph walker so the closure check
// below does not grade the walker against itself.
var staticImportRe = regexp.MustCompile(`(?m)^\s*(?:import|export)\b[^;'"]*?(?:\bfrom\s*)?['"]([^'"]+\.js)['"]`)

// TestShellHTML_PreloadsTheWholeStaticImportGraph checks the preload set is
// closed under static imports: every module a preloaded (or entry) module
// imports statically is itself preloaded, so the browser never discovers a
// module one round trip late.
func TestShellHTML_PreloadsTheWholeStaticImportGraph(t *testing.T) {
	shell, err := ui.ShellHTML()
	if err != nil {
		t.Fatalf("ShellHTML: %v", err)
	}
	preloadRe := regexp.MustCompile(`<link rel="modulepreload" href="([^"]+)"`)
	preloaded := map[string]bool{}
	for _, m := range preloadRe.FindAllSubmatch(shell, -1) {
		preloaded[string(m[1])] = true
	}
	if len(preloaded) < 20 {
		t.Fatalf("only %d modulepreload hints; the dashboard graph has far more modules", len(preloaded))
	}
	entry := versioned(t, "app.js")
	queue := []string{entry}
	seen := map[string]bool{entry: true}
	for len(queue) > 0 {
		u := queue[0]
		queue = queue[1:]
		rec := serve(t, u, nil)
		if rec.Code != http.StatusOK {
			t.Fatalf("GET %s = %d", u, rec.Code)
		}
		for _, m := range staticImportRe.FindAllStringSubmatch(rec.Body.String(), -1) {
			spec := m[1]
			if !strings.HasPrefix(spec, "/") {
				spec = path.Join(path.Dir(u), spec)
			}
			if !preloaded[spec] && spec != entry {
				t.Errorf("%s imports %s, which is not preloaded", u, spec)
			}
			if !seen[spec] {
				seen[spec] = true
				queue = append(queue, spec)
			}
		}
	}
	if len(seen) < 20 {
		t.Fatalf("walked only %d modules from the entry; the import regex is not matching", len(seen))
	}
}

// TestShellHTML_LeavesInlineScriptsUntouched guards the CSP: the dashboard's
// script-src hashes are computed from the raw index.html inline scripts, so
// the served shell must carry them byte for byte.
func TestShellHTML_LeavesInlineScriptsUntouched(t *testing.T) {
	shell, err := ui.ShellHTML()
	if err != nil {
		t.Fatalf("ShellHTML: %v", err)
	}
	raw, err := fs.ReadFile(ui.Static(), "index.html")
	if err != nil {
		t.Fatal(err)
	}
	got := inlineScriptRe.FindAllSubmatch(shell, -1)
	want := inlineScriptRe.FindAllSubmatch(raw, -1)
	if len(want) == 0 || len(got) != len(want) {
		t.Fatalf("inline scripts: shell has %d, index.html has %d", len(got), len(want))
	}
	for i := range want {
		if string(got[i][1]) != string(want[i][1]) {
			t.Errorf("inline script %d changed by the shell rewrite", i)
		}
	}
}

// TestVersionedAssets_ReferenceOnlyTheVersionedTree serves every JS and CSS file
// from the versioned tree and requires that none still imports the unversioned
// tree. One unversioned import would load a second copy of every module below
// it, with its own module state, next to the versioned copy.
func TestVersionedAssets_ReferenceOnlyTheVersionedTree(t *testing.T) {
	checked := 0
	err := fs.WalkDir(ui.Static(), ".", func(name string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		ext := path.Ext(name)
		if ext != ".js" && ext != ".css" {
			return nil
		}
		rec := serve(t, versioned(t, name), nil)
		if rec.Code != http.StatusOK {
			t.Errorf("GET %s = %d", name, rec.Code)
			return nil
		}
		if m := unversionedModuleRefRe.Find(rec.Body.Bytes()); m != nil {
			t.Errorf("%s still references the unversioned tree: %q", name, m)
		}
		checked++
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if checked < 50 {
		t.Fatalf("checked only %d assets", checked)
	}
}

func TestVersionedAssets_CachePolicy(t *testing.T) {
	cur := serve(t, versioned(t, "router.js"), nil)
	if cur.Code != http.StatusOK {
		t.Fatalf("current version: status %d", cur.Code)
	}
	if got := cur.Header().Get("Cache-Control"); got != "public, max-age=31536000, immutable" {
		t.Errorf("current version Cache-Control = %q, want immutable", got)
	}
	if ct := cur.Header().Get("Content-Type"); !strings.Contains(ct, "javascript") {
		t.Errorf("Content-Type = %q", ct)
	}

	// A page loaded before an upgrade may lazily request a module under the
	// old version. It gets the current bytes, but never an immutable policy
	// that would pin them under a version they do not belong to.
	stale := serve(t, "/static/v/0000000000000000/router.js", nil)
	if stale.Code != http.StatusOK {
		t.Fatalf("stale version: status %d", stale.Code)
	}
	if got := stale.Header().Get("Cache-Control"); got != "no-cache" {
		t.Errorf("stale version Cache-Control = %q, want no-cache", got)
	}
	if stale.Body.String() != cur.Body.String() {
		t.Error("a stale version must serve the current bytes")
	}

	// Unversioned URLs keep their revalidation policy.
	if got := serve(t, "/static/router.js", nil).Header().Get("Cache-Control"); got != "no-cache" {
		t.Errorf("unversioned Cache-Control = %q, want no-cache", got)
	}
}

func TestVersionedAssets_GzipAndRevalidation(t *testing.T) {
	u := versioned(t, "app.js")
	plain := serve(t, u, nil)
	gz := serve(t, u, map[string]string{"Accept-Encoding": "gzip"})
	if gz.Header().Get("Content-Encoding") != "gzip" {
		t.Fatalf("Content-Encoding = %q, want gzip", gz.Header().Get("Content-Encoding"))
	}
	if !strings.Contains(gz.Header().Get("Vary"), "Accept-Encoding") {
		t.Error("gzip response must Vary on Accept-Encoding")
	}
	if string(gunzip(t, gz.Body.Bytes())) != plain.Body.String() {
		t.Error("gzip body does not decompress to the identity body")
	}
	if gz.Header().Get("ETag") == plain.Header().Get("ETag") {
		t.Error("gzip and identity representations must not share an ETag")
	}
	nm := serve(t, u, map[string]string{"Accept-Encoding": "gzip", "If-None-Match": gz.Header().Get("ETag")})
	if nm.Code != http.StatusNotModified {
		t.Errorf("matching If-None-Match: status %d, want 304", nm.Code)
	}
	if nm := serve(t, u, map[string]string{"If-None-Match": plain.Header().Get("ETag")}); nm.Code != http.StatusNotModified {
		t.Errorf("identity If-None-Match: status %d, want 304", nm.Code)
	}
}

func TestVersionedAssets_NotFound(t *testing.T) {
	v := ui.AssetVersion()
	for _, target := range []string{
		"/static/v/",
		"/static/v/" + v,
		"/static/v/" + v + "/",
		"/static/v/" + v + "/views",
		"/static/v/" + v + "/missing.js",
		"/static/v/" + v + "/../index.html",
		"/static/v/" + v + "/views/../../index.html",
	} {
		if rec := serve(t, target, nil); rec.Code != http.StatusNotFound {
			t.Errorf("GET %s = %d, want 404", target, rec.Code)
		}
	}
}

// In dev-static mode files change under a running server, so nothing is
// versioned or rewritten, and a versioned URL (from a shell loaded before the
// switch) still resolves, uncached.
func TestVersionedAssets_DevStatic(t *testing.T) {
	dir := t.TempDir()
	const src = "import { x } from '/static/lib.js';\n"
	if err := os.WriteFile(filepath.Join(dir, "a.js"), []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "index.html"), []byte(`<script type="module" src="/static/a.js"></script></head>`), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SHINYHUB_DEV_STATIC", dir)

	if v := ui.AssetVersion(); v != "" {
		t.Errorf("AssetVersion = %q in dev-static mode, want empty", v)
	}
	shell, err := ui.ShellHTML()
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(shell), "/static/v/") || strings.Contains(string(shell), "modulepreload") {
		t.Errorf("dev-static shell was rewritten: %s", shell)
	}
	rec := serve(t, "/static/v/anything/a.js", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d", rec.Code)
	}
	if got := rec.Header().Get("Cache-Control"); got != "no-store" {
		t.Errorf("Cache-Control = %q, want no-store", got)
	}
	body, _ := io.ReadAll(rec.Body)
	if string(body) != src {
		t.Errorf("dev-static body was rewritten: %q", body)
	}
}
