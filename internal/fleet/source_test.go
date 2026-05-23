package fleet

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseSource_LocalExisting(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "apps", "alpha"), 0o755); err != nil {
		t.Fatal(err)
	}
	s, prob := ParseSource("./apps/alpha", dir)
	if prob != nil {
		t.Fatalf("unexpected problem: %v", prob)
	}
	if s.Kind != SourceLocal {
		t.Fatalf("Kind = %q, want local", s.Kind)
	}
	if s.LocalPath != filepath.Join(dir, "apps", "alpha") {
		t.Fatalf("LocalPath = %q", s.LocalPath)
	}
}

func TestParseSource_LocalMissing(t *testing.T) {
	dir := t.TempDir()
	_, prob := ParseSource("./apps/nope", dir)
	if prob == nil {
		t.Fatal("expected a problem for a missing local dir")
	}
	if got := prob.Error(); !strings.Contains(got, "not found") {
		t.Fatalf("problem = %q, want it to mention 'not found'", got)
	}
}

func TestParseSource_Git(t *testing.T) {
	cases := []struct {
		in, url, ref, subdir string
	}{
		{"git+https://x.com/r.git", "https://x.com/r.git", "", ""},
		{"git+https://x.com/r.git@main", "https://x.com/r.git", "main", ""},
		{"git+https://x.com/r.git#sub/dir", "https://x.com/r.git", "", "sub/dir"},
		{"git+https://x.com/r.git@v1.2#apps/a", "https://x.com/r.git", "v1.2", "apps/a"},
		{"git+ssh://git@h/r.git@deadbeef", "ssh://git@h/r.git", "deadbeef", ""},
	}
	for _, c := range cases {
		s, prob := ParseSource(c.in, t.TempDir())
		if prob != nil {
			t.Fatalf("%s: unexpected problem %v", c.in, prob)
		}
		if s.Kind != SourceGit || s.GitURL != c.url || s.GitRef != c.ref || s.GitSubdir != c.subdir {
			t.Fatalf("%s -> %+v, want url=%q ref=%q subdir=%q", c.in, s, c.url, c.ref, c.subdir)
		}
	}
}

func TestParseSource_Invalid(t *testing.T) {
	for _, in := range []string{"git+", "ftp://x", "https://x.com/r.git", "", "git+https://x.com/r.git@"} {
		if _, prob := ParseSource(in, t.TempDir()); prob == nil {
			t.Fatalf("%q: expected a problem", in)
		}
	}
}

// ERR-5: a git+ source without an explicit URL scheme (bare host/repo, or
// SCP-style git@host:path) must be rejected at parse time with a message that
// names the accepted schemes, not silently accepted and surfaced as a clone
// `exit status 128` minutes later.
func TestParseSource_GitRequiresScheme(t *testing.T) {
	for _, in := range []string{
		"git+github.com/acme/app",     // bare host/repo, no scheme
		"git+git@github.com:acme/app", // SCP-style, no "://"
		"git+./local/path",            // relative, no scheme
	} {
		_, prob := ParseSource(in, t.TempDir())
		if prob == nil {
			t.Fatalf("%q: expected a scheme problem", in)
		}
		if !strings.Contains(prob.Error(), "scheme") {
			t.Fatalf("%q: problem %q should mention the missing scheme", in, prob.Error())
		}
	}
}

