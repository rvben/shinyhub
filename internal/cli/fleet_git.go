package cli

import (
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/rvben/shinyhub/internal/fleet"
)

// resolveGitSource clones a git source to a temp dir and returns the working
// directory (the #subdir if any), the human ref label, the resolved commit
// SHA, and a cleanup func the caller MUST defer (temp clones removed on exit,
// spec §6). It reuses the existing gitClone for the common branch/tag case
// and falls back to a full clone + checkout for refs git cannot use as
// --branch (e.g. a bare SHA).
func resolveGitSource(s fleet.ParsedSource) (dir, ref, commit string, cleanup func(), err error) {
	noop := func() {}
	if s.Kind != fleet.SourceGit {
		return "", "", "", noop, fmt.Errorf("resolveGitSource called for non-git source %q", s.Raw)
	}

	d, cerr := gitClone(s.GitURL, s.GitRef, s.GitSubdir)
	if cerr != nil && s.GitRef != "" {
		// --branch could not select the ref (e.g. a full SHA). Full clone,
		// then checkout the ref explicitly.
		d2, ferr := gitCloneCheckout(s.GitURL, s.GitRef, s.GitSubdir)
		if ferr != nil {
			return "", "", "", noop, fmt.Errorf("clone %s@%s: %v; fallback: %w", s.GitURL, s.GitRef, cerr, ferr)
		}
		d = d2
	} else if cerr != nil {
		return "", "", "", noop, fmt.Errorf("clone %s: %w", s.GitURL, cerr)
	}

	// cloneRoot is the dir to remove: gitClone returns <tmp>/<subdir> when a
	// subdir is set, but the temp root is its parent in that case.
	cloneRoot := d
	if s.GitSubdir != "" {
		cloneRoot = strings.TrimSuffix(d, "/"+s.GitSubdir)
	}
	clean := func() { _ = os.RemoveAll(cloneRoot) }

	sha, rerr := gitRevParseHEAD(d)
	if rerr != nil {
		clean()
		return "", "", "", noop, fmt.Errorf("resolve commit for %s: %w", s.GitURL, rerr)
	}
	label := s.GitRef
	if label == "" {
		label = "HEAD"
	}
	return d, label, sha, clean, nil
}

// gitCloneCheckout does a non-shallow clone then `git checkout <ref>` so an
// arbitrary commit SHA (not valid for clone --branch) still resolves. The
// returned dir already accounts for subdir, matching gitClone's contract.
func gitCloneCheckout(url, ref, subdir string) (string, error) {
	root, err := os.MkdirTemp("", "shiny-git-*")
	if err != nil {
		return "", fmt.Errorf("create temp dir: %w", err)
	}
	if out, e := exec.Command("git", "clone", url, root).CombinedOutput(); e != nil {
		_ = os.RemoveAll(root)
		return "", fmt.Errorf("git clone: %w\n%s", e, out)
	}
	co := exec.Command("git", "checkout", ref)
	co.Dir = root
	if out, e := co.CombinedOutput(); e != nil {
		_ = os.RemoveAll(root)
		return "", fmt.Errorf("git checkout %s: %w\n%s", ref, e, out)
	}
	if subdir != "" {
		sub := root + "/" + subdir
		if _, e := os.Stat(sub); e != nil {
			_ = os.RemoveAll(root)
			return "", fmt.Errorf("subdir %q not found in repo", subdir)
		}
		return sub, nil
	}
	return root, nil
}

func gitRevParseHEAD(dir string) (string, error) {
	cmd := exec.Command("git", "rev-parse", "HEAD")
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("git rev-parse: %w\n%s", err, out)
	}
	return strings.TrimSpace(string(out)), nil
}
