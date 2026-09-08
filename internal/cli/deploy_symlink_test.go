package cli

import (
	"archive/zip"
	"bytes"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// symlink creates newname -> oldname, skipping the test where the platform
// cannot make one.
func symlink(t *testing.T, oldname, newname string) {
	t.Helper()
	if err := os.Symlink(oldname, newname); err != nil {
		if runtime.GOOS == "windows" {
			t.Skipf("symlinks unavailable: %v", err)
		}
		t.Fatalf("symlink %s -> %s: %v", newname, oldname, err)
	}
}

// F2: `renv::init()` + `renv::install()` fills renv/library with symlinks into
// the developer's machine-local renv cache. filepath.Walk lstats, so each of
// those links looked like a file, os.Open followed it to the real directory,
// and io.Copy failed with "is a directory" - killing the deploy before upload,
// on the layout docs/recipes/r-shiny.md calls safe to ship.
func TestZipDir_RenvLibraryWithCacheSymlinksDoesNotFail(t *testing.T) {
	cache := t.TempDir()
	writeFile(t, cache, "R6/DESCRIPTION", "Package: R6\n")
	writeFile(t, cache, "shiny/DESCRIPTION", "Package: shiny\n")

	dir := t.TempDir()
	writeFile(t, dir, "app.R", "shinyApp(ui, server)\n")
	writeFile(t, dir, "renv.lock", `{"R":{"Version":"4.4.1"},"Packages":{}}`)
	writeFile(t, dir, "renv/activate.R", "# renv\n")

	libDir := filepath.Join(dir, "renv", "library", "macos", "R-4.4", "aarch64-apple-darwin20")
	if err := os.MkdirAll(libDir, 0o755); err != nil {
		t.Fatal(err)
	}
	symlink(t, filepath.Join(cache, "R6"), filepath.Join(libDir, "R6"))
	symlink(t, filepath.Join(cache, "shiny"), filepath.Join(libDir, "shiny"))
	// Not every restored package is a cache link: one built from source is
	// copied in whole, so the library holds real bytes as well as links.
	writeFile(t, libDir, "golem/DESCRIPTION", "Package: golem\n")

	buf, _, err := zipDir(dir)
	if err != nil {
		t.Fatalf("bundling a restored renv project must not fail: %v", err)
	}
	names := zipNames(t, buf)
	if !contains(names, "app.R") || !contains(names, "renv.lock") || !contains(names, "renv/activate.R") {
		t.Errorf("the app's own source must still ship: %v", names)
	}
	// The restored library is a build artifact of renv.lock - the server
	// rebuilds it, and these links point at paths no other host has.
	if containsPrefix(names, "renv/library") {
		t.Errorf("renv/library must not be uploaded: %v", names)
	}

	// The library must be pruned at its root, not walked and then discarded
	// link by link: every R project restored from a cache would otherwise
	// report a page of skipped symlinks the operator can do nothing about.
	preview, err := buildBundlePreview(dir)
	if err != nil {
		t.Fatalf("buildBundlePreview: %v", err)
	}
	if len(preview.SkippedLinks) != 0 {
		t.Errorf("a pruned library must produce no skipped-link noise, got %v", preview.SkippedLinks)
	}
}

// A symlink to a directory outside renv/library still has no zip
// representation, but skipping it must not abort the bundle - and must not be
// silent, since a symlinked directory of shared code can be deliberate.
func TestBuildBundlePreview_ReportsSkippedDirectorySymlink(t *testing.T) {
	shared := t.TempDir()
	writeFile(t, shared, "helpers.R", "f <- function() 1\n")

	dir := t.TempDir()
	writeFile(t, dir, "app.R", "shinyApp(ui, server)\n")
	symlink(t, shared, filepath.Join(dir, "common"))

	preview, err := buildBundlePreview(dir)
	if err != nil {
		t.Fatalf("a directory symlink must not abort the bundle: %v", err)
	}
	if !contains(preview.Files, "app.R") {
		t.Errorf("app.R missing: %v", preview.Files)
	}
	if !contains(preview.SkippedLinks, "common") {
		t.Errorf("skipped link must be reported, got %v", preview.SkippedLinks)
	}
	if note := summarizeSkippedLinks(preview.SkippedLinks); !strings.Contains(note, "common") {
		t.Errorf("the operator-facing note must name the link, got %q", note)
	}
}

// A dangling symlink has no bytes to archive. It used to abort the walk with
// the underlying stat error; it is now skipped and reported.
func TestBuildBundlePreview_DanglingSymlinkIsSkipped(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "app.R", "shinyApp(ui, server)\n")
	symlink(t, filepath.Join(dir, "gone.csv"), filepath.Join(dir, "link.csv"))

	preview, err := buildBundlePreview(dir)
	if err != nil {
		t.Fatalf("a dangling symlink must not abort the bundle: %v", err)
	}
	if contains(preview.Files, "link.csv") {
		t.Errorf("a dangling link has no content and must not be archived: %v", preview.Files)
	}
	if !contains(preview.SkippedLinks, "link.csv") {
		t.Errorf("dangling link must be reported, got %v", preview.SkippedLinks)
	}
}

// A symlink to a regular file is archived as the file it names, measured by the
// target rather than by the link, so the per-file size rule sees the real size.
func TestZipDir_FileSymlinkShipsTargetContent(t *testing.T) {
	shared := t.TempDir()
	writeFile(t, shared, "config.json", `{"k":"v"}`)

	dir := t.TempDir()
	writeFile(t, dir, "app.R", "shinyApp(ui, server)\n")
	symlink(t, filepath.Join(shared, "config.json"), filepath.Join(dir, "config.json"))

	buf, _, err := zipDir(dir)
	if err != nil {
		t.Fatalf("zipDir: %v", err)
	}
	if !contains(zipNames(t, buf), "config.json") {
		t.Errorf("a symlinked file must ship: %v", zipNames(t, buf))
	}
	// The entry must describe the target, not the link: an entry still carrying
	// the symlink mode bit unpacks on the server as a link to a path that host
	// does not have, which is the same class of breakage as shipping
	// renv/library.
	zr, err := zip.NewReader(bytes.NewReader(buf.Bytes()), int64(buf.Len()))
	if err != nil {
		t.Fatal(err)
	}
	var seen bool
	for _, f := range zr.File {
		if f.Name != "config.json" {
			continue
		}
		seen = true
		if f.Mode()&os.ModeSymlink != 0 {
			t.Errorf("entry mode %v still carries the symlink bit", f.Mode())
		}
		rc, err := f.Open()
		if err != nil {
			t.Fatal(err)
		}
		body, err := io.ReadAll(rc)
		rc.Close()
		if err != nil {
			t.Fatal(err)
		}
		if string(body) != `{"k":"v"}` {
			t.Errorf("entry content = %q, want the target's bytes", body)
		}
	}
	if !seen {
		t.Fatal("config.json entry missing")
	}

	preview, err := buildBundlePreview(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(preview.SkippedLinks) != 0 {
		t.Errorf("a file symlink is archivable and must not be reported skipped: %v", preview.SkippedLinks)
	}
	// The link's own size is the length of its target path; the entry must be
	// measured by the file it points at.
	if preview.UncompressedBytes < int64(len(`{"k":"v"}`)) {
		t.Errorf("uncompressed bytes %d must count the target's size, not the link's",
			preview.UncompressedBytes)
	}
}
