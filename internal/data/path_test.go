package data

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"
)

func TestAppDataDir_Joins(t *testing.T) {
	if got := AppDataDir("/srv/data", "demo"); got != filepath.Join("/srv/data", "demo") {
		t.Errorf("got %q", got)
	}
}

func TestSanitizeRelPath_Accepts(t *testing.T) {
	for _, p := range []string{"foo.parquet", "a/b/c.duckdb", "uploads/2026/x.csv"} {
		if _, err := SanitizeRelPath(p); err != nil {
			t.Errorf("Sanitize(%q): %v", p, err)
		}
	}
}

func TestSanitizeRelPath_Rejects(t *testing.T) {
	rejects := []string{
		"/abs",
		"../escape",
		"a/../b",
		"",
		"a/",
		".",
		"..",
		".shinyhub-foo",
		".shinyhub-upload-tmp/file",
		strings.Repeat("a", 600),
	}
	for _, p := range rejects {
		if _, err := SanitizeRelPath(p); !errors.Is(err, ErrInvalidPath) {
			t.Errorf("Sanitize(%q) err = %v, want ErrInvalidPath", p, err)
		}
	}
}

func TestSanitizeRelPath_NullByte(t *testing.T) {
	if _, err := SanitizeRelPath("a\x00b"); !errors.Is(err, ErrInvalidPath) {
		t.Errorf("got %v", err)
	}
}
