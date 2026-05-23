package fleet

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// SourceKind enumerates resolved source forms.
type SourceKind string

const (
	SourceLocal SourceKind = "local"
	SourceGit   SourceKind = "git"
)

// ParsedSource is the classified, not-yet-fetched source of one app.
// For SourceLocal, LocalPath is the absolute on-disk directory.
// For SourceGit, GitURL/GitRef/GitSubdir are decomposed; cloning happens
// later in the CLI layer (pre-flight step 3), never here.
type ParsedSource struct {
	Raw       string
	Kind      SourceKind
	LocalPath string
	GitURL    string
	GitRef    string
	GitSubdir string
}

// ParseSource classifies raw per the source spec. manifestDir is the directory
// containing the manifest file; relative local sources resolve against it.
// Returns a *Problem (never panics) on an unresolvable/ill-formed source.
func ParseSource(raw, manifestDir string) (ParsedSource, *Problem) {
	if raw == "" {
		return ParsedSource{}, &Problem{Msg: "source is empty"}
	}
	if strings.HasPrefix(raw, "git+") {
		return parseGitSource(raw)
	}
	abs := raw
	if !filepath.IsAbs(abs) {
		abs = filepath.Join(manifestDir, raw)
	}
	info, err := os.Stat(abs)
	if err != nil || !info.IsDir() {
		return ParsedSource{}, &Problem{Msg: fmt.Sprintf(
			"source %q not found (resolved to %q) and not a git+ URL", raw, abs)}
	}
	return ParsedSource{Raw: raw, Kind: SourceLocal, LocalPath: abs}, nil
}

// hasGitURLScheme reports whether body begins with a git-clonable URL scheme.
// SCP-style "git@host:path" and bare "host/repo" have no scheme and are
// rejected: they only fail later as an opaque clone "exit status 128".
func hasGitURLScheme(body string) bool {
	for _, scheme := range []string{"https://", "http://", "ssh://", "git://"} {
		if strings.HasPrefix(body, scheme) {
			return true
		}
	}
	return false
}

// parseGitSource decomposes git+<url>[@ref][#subdir]. The "git+" prefix is
// stripped; the optional "#subdir" is split first (so a ref cannot contain
// '#'), then the optional "@ref" (rightmost '@' after the scheme so URLs
// containing '@', e.g. ssh://git@host, keep their userinfo).
func parseGitSource(raw string) (ParsedSource, *Problem) {
	body := strings.TrimPrefix(raw, "git+")
	// Only scheme URLs (https://, ssh://, git://) are supported; SCP-style
	// "git@host:path" is not (no "://" to anchor the userinfo split).
	if body == "" {
		return ParsedSource{}, &Problem{Msg: fmt.Sprintf("invalid git source %q: empty URL", raw)}
	}
	var subdir string
	if i := strings.IndexByte(body, '#'); i >= 0 {
		subdir = body[i+1:]
		body = body[:i]
	}
	if !hasGitURLScheme(body) {
		return ParsedSource{}, &Problem{Msg: fmt.Sprintf(
			"invalid git source %q: missing URL scheme "+
				"(use git+https://, git+ssh://, git+http://, or git+git://)", raw)}
	}
	var ref string
	// Find a '@' that is part of "@ref", not the userinfo of an ssh URL.
	// Strategy: ignore any '@' before the scheme's "://"; take the last '@'
	// in the remainder.
	scan := body
	off := 0
	if k := strings.Index(body, "://"); k >= 0 {
		off = k + 3
		scan = body[off:]
	}
	if i := strings.LastIndexByte(scan, '@'); i >= 0 {
		ref = scan[i+1:]
		if ref == "" {
			return ParsedSource{}, &Problem{Msg: fmt.Sprintf(
				"invalid git source %q: '@' with no ref", raw)}
		}
		body = body[:off+i]
	}
	if body == "" {
		return ParsedSource{}, &Problem{Msg: fmt.Sprintf("invalid git source %q: empty URL", raw)}
	}
	return ParsedSource{
		Raw: raw, Kind: SourceGit,
		GitURL: body, GitRef: ref, GitSubdir: subdir,
	}, nil
}
