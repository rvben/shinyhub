package cli

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rvben/shinyhub/internal/fleet"
)

// ERR-5: git command failures must not leak a trailing blank line. With no
// command output, the formatted error is just the prefix + cause; with output,
// the (trimmed) text follows on its own line. The cause stays unwrappable.
func TestGitCmdError_TrimsOutput(t *testing.T) {
	cause := errors.New("exit status 128")

	noOut := gitCmdError("git clone", cause, []byte("   \n\n"))
	if got := noOut.Error(); got != "git clone: exit status 128" {
		t.Fatalf("empty-output error = %q, want no trailing whitespace/newline", got)
	}

	withOut := gitCmdError("git clone", cause, []byte("fatal: repository not found\n"))
	want := "git clone: exit status 128\nfatal: repository not found"
	if got := withOut.Error(); got != want {
		t.Fatalf("with-output error = %q, want %q", got, want)
	}
	if !errors.Is(withOut, cause) {
		t.Fatal("gitCmdError must wrap the underlying cause")
	}
}

// makeLocalGitRepo creates a real local git repo (origin) so the test does no
// network I/O; gitClone clones a file:// URL.
func makeLocalGitRepo(t *testing.T) (url string, headSHA string) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	repo := t.TempDir()
	run := func(args ...string) string {
		cmd := exec.Command("git", args...)
		cmd.Dir = repo
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t",
			"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
		return string(out)
	}
	run("init", "-q", "-b", "main")
	if err := os.WriteFile(filepath.Join(repo, "app.py"), []byte("print(1)\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run("add", ".")
	run("commit", "-q", "-m", "init")
	sha := run("rev-parse", "HEAD")
	return repo, trimNL(sha)
}

func trimNL(s string) string {
	for len(s) > 0 && (s[len(s)-1] == '\n' || s[len(s)-1] == '\r') {
		s = s[:len(s)-1]
	}
	return s
}

func TestResolveGitSource_BranchClone(t *testing.T) {
	url, head := makeLocalGitRepo(t)
	s := fleet.ParsedSource{Kind: fleet.SourceGit, GitURL: url, GitRef: "main"}
	dir, ref, commit, cleanup, err := resolveGitSource(s)
	if err != nil {
		t.Fatalf("resolveGitSource: %v", err)
	}
	defer cleanup()
	if _, statErr := os.Stat(filepath.Join(dir, "app.py")); statErr != nil {
		t.Fatalf("clone missing app.py: %v", statErr)
	}
	if ref != "main" {
		t.Fatalf("ref = %q, want main", ref)
	}
	if commit != head {
		t.Fatalf("commit = %q, want %q", commit, head)
	}
	cleanup()
	if _, statErr := os.Stat(dir); !os.IsNotExist(statErr) {
		t.Fatalf("cleanup did not remove clone dir %s", dir)
	}
}

func TestResolveGitSource_DefaultRefWhenEmpty(t *testing.T) {
	url, head := makeLocalGitRepo(t)
	s := fleet.ParsedSource{Kind: fleet.SourceGit, GitURL: url}
	dir, ref, commit, cleanup, err := resolveGitSource(s)
	if err != nil {
		t.Fatalf("resolveGitSource: %v", err)
	}
	defer cleanup()
	if dir == "" || commit != head {
		t.Fatalf("dir=%q commit=%q (want commit %q)", dir, commit, head)
	}
	if ref != "HEAD" {
		t.Fatalf("ref = %q, want HEAD (default)", ref)
	}
}

func TestResolveGitSource_MultiSegmentSubdirCleanup(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	repo := t.TempDir()
	run := func(args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = repo
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t",
			"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("init", "-q", "-b", "main")
	if err := os.MkdirAll(filepath.Join(repo, "apps", "ui"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "apps", "ui", "app.py"), []byte("print(1)\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run("add", ".")
	run("commit", "-q", "-m", "init")

	s := fleet.ParsedSource{Kind: fleet.SourceGit, GitURL: repo, GitRef: "main", GitSubdir: "apps/ui"}
	dir, _, _, cleanup, err := resolveGitSource(s)
	if err != nil {
		t.Fatalf("resolveGitSource: %v", err)
	}
	if _, statErr := os.Stat(filepath.Join(dir, "app.py")); statErr != nil {
		t.Fatalf("subdir clone missing app.py: %v", statErr)
	}
	// dir is <tmpRoot>/apps/ui; the whole temp root must be removed.
	tmpRoot := strings.TrimSuffix(dir, "/apps/ui")
	cleanup()
	if _, statErr := os.Stat(tmpRoot); !os.IsNotExist(statErr) {
		t.Fatalf("cleanup leaked temp root %s (multi-segment subdir not fully removed)", tmpRoot)
	}
}
