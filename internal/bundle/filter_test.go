package bundle

import (
	"strings"
	"testing"
)

func TestInspect_Accepts(t *testing.T) {
	r := DefaultRules()
	accepts := []string{"app.R", "requirements.txt", "src/helpers.py", "ui/css/site.css"}
	for _, p := range accepts {
		if got := r.Inspect(p, 1024); got != FilterAccept {
			t.Errorf("Inspect(%q) = %v, want FilterAccept", p, got)
		}
	}
}

func TestInspect_SoftSkipsCacheDirs(t *testing.T) {
	r := DefaultRules()
	skips := []string{
		".git/config",
		".venv/lib/site-packages/foo.py",
		"__pycache__/bar.pyc",
		"node_modules/x/index.js",
		".renv/library/abc",
		".Rproj.user/y",
	}
	for _, p := range skips {
		if got := r.Inspect(p, 100); got != FilterSkipCacheDir {
			t.Errorf("Inspect(%q) = %v, want FilterSkipCacheDir", p, got)
		}
	}
}

func TestInspect_RejectsDataDir(t *testing.T) {
	r := DefaultRules()
	for _, p := range []string{"data/foo.csv", "data/sub/x.parquet", "data"} {
		if got := r.Inspect(p, 1); got != FilterRejectDataDir {
			t.Errorf("Inspect(%q) = %v, want FilterRejectDataDir", p, got)
		}
	}
}

func TestInspect_RejectsDatasetDir(t *testing.T) {
	r := DefaultRules()
	for _, p := range []string{"datasets/bar.csv", ".shinyhub-data/baz"} {
		if got := r.Inspect(p, 1); got != FilterRejectDatasetDir {
			t.Errorf("Inspect(%q) = %v, want FilterRejectDatasetDir", p, got)
		}
	}
}

func TestInspect_RejectsDataExtensions(t *testing.T) {
	r := DefaultRules()
	for _, p := range []string{"seed.parquet", "deep/sub/cache.duckdb", "x.sqlite3", "y.RDS"} {
		if got := r.Inspect(p, 100); got != FilterRejectExtension {
			t.Errorf("Inspect(%q) = %v, want FilterRejectExtension", p, got)
		}
	}
}

func TestInspect_RejectsOversizedFile(t *testing.T) {
	r := DefaultRules()
	big := r.MaxFileBytes + 1
	if got := r.Inspect("notes.md", big); got != FilterRejectFileSize {
		t.Errorf("got %v, want FilterRejectFileSize", got)
	}
}

func TestDefaultRules_Stable(t *testing.T) {
	r := DefaultRules()
	if r.MaxFileBytes != 10*1024*1024 {
		t.Errorf("MaxFileBytes = %d, want 10MiB", r.MaxFileBytes)
	}
	want := []string{".git", ".venv", "__pycache__", "node_modules", ".renv", ".Rproj.user"}
	if strings.Join(r.CacheDirs, ",") != strings.Join(want, ",") {
		t.Errorf("CacheDirs = %v, want %v", r.CacheDirs, want)
	}
}
