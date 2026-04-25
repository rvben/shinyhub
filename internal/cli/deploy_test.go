package cli

import (
	"archive/zip"
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)


func TestValidSlugRE(t *testing.T) {
	valid := []string{"myapp", "my-app", "app123", "a", "a0"}
	for _, s := range valid {
		if !validSlugRE.MatchString(s) {
			t.Errorf("expected %q to be valid, but validSlugRE rejected it", s)
		}
	}
	invalid := []string{"MyApp", "UPPER", "my_app", "-leading", "trailing-", "my app", ""}
	for _, s := range invalid {
		if validSlugRE.MatchString(s) {
			t.Errorf("expected %q to be invalid, but validSlugRE accepted it", s)
		}
	}
}

// TestDeploy_SlugValidation tests the slug validation logic directly.
// The invalid slug must be rejected before any network call is made.
func TestDeploy_SlugValidation(t *testing.T) {
	cases := []struct {
		slug    string
		wantErr bool
	}{
		{"my-app", false},
		{"myapp", false},
		{"MyApp", true},
		{"UPPER", true},
		{"my_app", true},
		{"-leading", true},
		{"trailing-", true},
		{"my app", true},
		{"", false}, // empty means "derive from dir name"
	}
	for _, tc := range cases {
		if tc.slug == "" {
			continue // auto-derived slugs are not user-validated here
		}
		matched := validSlugRE.MatchString(tc.slug)
		isInvalid := !matched
		if tc.wantErr && !isInvalid {
			t.Errorf("slug %q should be invalid but regex accepted it", tc.slug)
		}
		if !tc.wantErr && isInvalid {
			t.Errorf("slug %q should be valid but regex rejected it", tc.slug)
		}
	}
}

func TestSanitizeSlug(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"my-app", "my-app"},
		{"My App", "my-app"},
		{"Counter_App_2024", "counter-app-2024"},
		{"  spaces  ", "spaces"},
		{"UPPER", "upper"},
	}
	for _, tc := range cases {
		if got := sanitizeSlug(tc.in); got != tc.want {
			t.Errorf("sanitizeSlug(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestGitClone_InvalidURL(t *testing.T) {
	dir, err := gitClone("not-a-url", "main", "")
	if err == nil {
		t.Error("expected error for invalid URL, got nil")
		os.RemoveAll(dir)
	}
}

func TestGitClone_LocalRepo(t *testing.T) {
	// Create a minimal local git repo to clone from.
	src := t.TempDir()
	mustRun(t, src, "git", "init", "-b", "main")
	mustRun(t, src, "git", "config", "user.email", "test@test.com")
	mustRun(t, src, "git", "config", "user.name", "Test")
	if err := os.WriteFile(filepath.Join(src, "app.py"), []byte("# shiny app\n"), 0644); err != nil {
		t.Fatal(err)
	}
	mustRun(t, src, "git", "add", "app.py")
	mustRun(t, src, "git", "commit", "-m", "init")

	dir, err := gitClone("file://"+src, "main", "")
	if err != nil {
		t.Fatalf("gitClone local: %v", err)
	}
	defer os.RemoveAll(dir)

	if _, err := os.Stat(filepath.Join(dir, "app.py")); err != nil {
		t.Errorf("expected app.py in cloned dir: %v", err)
	}
}

func TestZipDir_OmitsDataAndCacheDirs(t *testing.T) {
	src := t.TempDir()
	must := func(p string, b []byte) {
		if err := os.MkdirAll(filepath.Dir(filepath.Join(src, p)), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(src, p), b, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	must("app.R", []byte("x"))
	must("data/seed.csv", []byte("a,b"))
	must(".git/HEAD", []byte("ref"))
	must("seed.parquet", []byte("PAR1"))

	buf, summary, err := zipDir(src)
	if err != nil {
		t.Fatalf("zipDir: %v", err)
	}
	zr, err := zip.NewReader(bytes.NewReader(buf.Bytes()), int64(buf.Len()))
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, f := range zr.File {
		seen[f.Name] = true
	}

	if !seen["app.R"] {
		t.Error("app.R missing from zip")
	}
	if seen["data/seed.csv"] {
		t.Error("data/seed.csv must not be zipped")
	}
	if seen[".git/HEAD"] {
		t.Error(".git/HEAD must not be zipped")
	}
	if seen["seed.parquet"] {
		t.Error("seed.parquet must not be zipped")
	}
	if !strings.Contains(summary, "data") {
		t.Errorf("summary should mention data exclusion: %q", summary)
	}
	if !strings.Contains(summary, "seed.parquet") {
		t.Errorf("summary should mention seed.parquet: %q", summary)
	}
}

func mustRun(t *testing.T, dir, cmd string, args ...string) {
	t.Helper()
	c := exec.Command(cmd, args...)
	c.Dir = dir
	if out, err := c.CombinedOutput(); err != nil {
		t.Fatalf("%s: %v\n%s", cmd, err, out)
	}
}
