package process_test

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/rvben/shinyhub/internal/process"
)

func writeBundleFile(t *testing.T, dir, rel, content string) {
	t.Helper()
	path := filepath.Join(dir, rel)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func readIndex(t *testing.T, dir string, env ...string) process.RequirementsIndex {
	t.Helper()
	idx, err := process.ReadRequirementsIndex(dir, env)
	if err != nil {
		t.Fatalf("ReadRequirementsIndex: %v", err)
	}
	return idx
}

func TestReadRequirementsIndex_OptionForms(t *testing.T) {
	dir := t.TempDir()
	writeBundleFile(t, dir, "requirements.txt", strings.Join([]string{
		"# private registry",
		"--index-url=https://first.example/simple",
		"-i https://second.example/simple",
		"--extra-index-url https://extra-a.example/simple  # trailing comment",
		"--extra-index-url=https://extra-b.example/simple",
		"--extra-index-url https://extra-a.example/simple",
		"-f ./wheels",
		"--find-links=https://links.example/",
		"--no-index",
		"shiny>=1.0 \\",
		"  ; python_version >= '3.9'",
		"pandas#not-a-comment-without-space",
		"",
	}, "\n"))

	got := readIndex(t, dir)
	want := process.RequirementsIndex{
		DefaultIndex: "https://second.example/simple",
		Indexes:      []string{"https://extra-a.example/simple", "https://extra-b.example/simple"},
		FindLinks:    []string{"wheels", "https://links.example/"},
		NoIndex:      true,
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v\nwant %+v", got, want)
	}
}

func TestReadRequirementsIndex_ShortOptionAttachedValue(t *testing.T) {
	dir := t.TempDir()
	writeBundleFile(t, dir, "requirements.txt", "-ihttps://attached.example/simple\n-f/opt/wheels\nsix\n")
	got := readIndex(t, dir)
	if got.DefaultIndex != "https://attached.example/simple" {
		t.Errorf("DefaultIndex = %q", got.DefaultIndex)
	}
	if !reflect.DeepEqual(got.FindLinks, []string{"/opt/wheels"}) {
		t.Errorf("FindLinks = %q", got.FindLinks)
	}
}

func TestReadRequirementsIndex_OptionOnContinuationLine(t *testing.T) {
	dir := t.TempDir()
	writeBundleFile(t, dir, "requirements.txt", "--index-url \\\n  https://cont.example/simple\nsix\n")
	if got := readIndex(t, dir).DefaultIndex; got != "https://cont.example/simple" {
		t.Fatalf("DefaultIndex = %q, want the value from the continuation line", got)
	}
}

func TestReadRequirementsIndex_FollowsIncludesInsideBundle(t *testing.T) {
	dir := t.TempDir()
	writeBundleFile(t, dir, "requirements.txt", "-r reqs/base.txt\n--constraint=reqs/pins.txt\nsix\n")
	writeBundleFile(t, dir, "reqs/base.txt", "--extra-index-url https://nested.example/simple\n-f wheels\n--requirement ../requirements.txt\n")
	writeBundleFile(t, dir, "reqs/pins.txt", "-i https://pins.example/simple\n")

	got := readIndex(t, dir)
	want := process.RequirementsIndex{
		DefaultIndex: "https://pins.example/simple",
		Indexes:      []string{"https://nested.example/simple"},
		// uv resolves a relative find-links path against its working
		// directory, the bundle root, even inside an include.
		FindLinks: []string{"wheels"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v\nwant %+v", got, want)
	}
}

func TestReadRequirementsIndex_RejectsIncludeOutsideBundle(t *testing.T) {
	parent := t.TempDir()
	dir := filepath.Join(parent, "bundle")
	writeBundleFile(t, parent, "outside.txt", "-i https://outside.example/simple\n")
	writeBundleFile(t, dir, "requirements.txt", "-r ../outside.txt\n")

	if _, err := process.ReadRequirementsIndex(dir, nil); err == nil {
		t.Fatal("an include that leaves the bundle must be an error, not silently followed or skipped")
	}
}

func TestReadRequirementsIndex_RejectsIncludeSymlinkedOutsideBundle(t *testing.T) {
	parent := t.TempDir()
	dir := filepath.Join(parent, "bundle")
	writeBundleFile(t, parent, "outside.txt", "-i https://outside.example/simple\n")
	writeBundleFile(t, dir, "requirements.txt", "-r linked.txt\n")
	if err := os.Symlink(filepath.Join(parent, "outside.txt"), filepath.Join(dir, "linked.txt")); err != nil {
		t.Fatal(err)
	}

	if _, err := process.ReadRequirementsIndex(dir, nil); err == nil {
		t.Fatal("an include that resolves outside the bundle through a symlink must be an error")
	}
}

// A missing include is uv's error to report, with its own message.
func TestReadRequirementsIndex_SkipsMissingInclude(t *testing.T) {
	dir := t.TempDir()
	writeBundleFile(t, dir, "requirements.txt", "-r missing.txt\n-i https://kept.example/simple\n")
	if got := readIndex(t, dir).DefaultIndex; got != "https://kept.example/simple" {
		t.Fatalf("DefaultIndex = %q", got)
	}
}

// uv fetches a remote include and installs what it lists, but any index
// options in it would be lost, so it is refused rather than skipped.
func TestReadRequirementsIndex_RejectsRemoteInclude(t *testing.T) {
	for _, line := range []string{"-r https://example.com/reqs.txt", "--constraint=http://example.com/c.txt", "-rfile:///etc/reqs.txt"} {
		dir := t.TempDir()
		writeBundleFile(t, dir, "requirements.txt", line+"\nsix\n")
		if _, err := process.ReadRequirementsIndex(dir, nil); err == nil || !strings.Contains(err.Error(), "remote") {
			t.Errorf("%q: err = %v, want a remote-include error", line, err)
		}
	}
	// The error names the include as written, never what a reference in it
	// expanded to, and masks credentials written literally into the URL.
	dir := t.TempDir()
	writeBundleFile(t, dir, "requirements.txt",
		"-r https://svc:pw-lit-77@reqs.example/r.txt?token=${TOKEN}&sig=lit-sig-88\n")
	_, err := process.ReadRequirementsIndex(dir, []string{"TOKEN=tk-5150"})
	if err == nil || !strings.Contains(err.Error(), "https://***@reqs.example/r.txt?token=***&sig=***") {
		t.Errorf("err = %v, want the include with its credentials masked", err)
	}
	for _, leak := range []string{"tk-5150", "pw-lit-77", "lit-sig-88"} {
		if err != nil && strings.Contains(err.Error(), leak) {
			t.Errorf("error leaks %q: %v", leak, err)
		}
	}
}

// An include path is expanded before it is resolved and read, so every error
// the include walk returns masks what a ${NAME} reference expanded to.
func TestReadRequirementsIndex_IncludeErrorsMaskExpandedValues(t *testing.T) {
	t.Run("outside the bundle", func(t *testing.T) {
		outside := t.TempDir()
		writeBundleFile(t, outside, "r.txt", "six\n")
		dir := t.TempDir()
		writeBundleFile(t, dir, "requirements.txt", "-r ${INCLUDE_DIR}/r.txt\n")
		_, err := process.ReadRequirementsIndex(dir, []string{"INCLUDE_DIR=" + outside})
		if err == nil || !strings.Contains(err.Error(), "outside the bundle") {
			t.Fatalf("err = %v, want an outside-the-bundle error", err)
		}
		if strings.Contains(err.Error(), outside) {
			t.Errorf("error leaks the expanded value: %v", err)
		}
		if !strings.Contains(err.Error(), "***/r.txt") {
			t.Errorf("error lost the masked include path: %v", err)
		}
	})
	t.Run("unreadable include", func(t *testing.T) {
		const secret = "zq-dir-7781"
		dir := t.TempDir()
		if err := os.Mkdir(filepath.Join(dir, secret), 0o755); err != nil {
			t.Fatal(err)
		}
		writeBundleFile(t, dir, "requirements.txt", "-r ${INCLUDE_NAME}\n")
		_, err := process.ReadRequirementsIndex(dir, []string{"INCLUDE_NAME=" + secret})
		if err == nil {
			t.Fatal("reading a directory as an include succeeded")
		}
		if strings.Contains(err.Error(), secret) {
			t.Errorf("error leaks the expanded value: %v", err)
		}
		if !errors.Is(err, syscall.EISDIR) {
			t.Errorf("err = %v, want it to still wrap EISDIR", err)
		}
	})
}

func TestReadRequirementsIndex_ExpandsEnvReferences(t *testing.T) {
	dir := t.TempDir()
	writeBundleFile(t, dir, "requirements.txt", strings.Join([]string{
		"--index-url https://__token__:${PYPI_TOKEN}@private.example/simple",
		"--extra-index-url https://${UNSET_HOST}/simple",
		"--extra-index-url https://${lower}/simple",
	}, "\n"))

	got := readIndex(t, dir, "PYPI_TOKEN=s3cret", "lower=nope")
	if got.DefaultIndex != "https://__token__:s3cret@private.example/simple" {
		t.Errorf("DefaultIndex = %q, want ${PYPI_TOKEN} expanded", got.DefaultIndex)
	}
	// An undefined variable stays literal (uv and pip do the same), and only
	// ${UPPER_CASE} references are variables.
	want := []string{"https://${UNSET_HOST}/simple", "https://${lower}/simple"}
	if !reflect.DeepEqual(got.Indexes, want) {
		t.Errorf("Indexes = %q, want %q", got.Indexes, want)
	}
}

// Redact masks what a ${NAME} reference expanded to wherever it lands in a
// URL, not only in userinfo, and leaves everything else readable.
func TestRequirementsIndex_RedactMasksExpandedValues(t *testing.T) {
	dir := t.TempDir()
	writeBundleFile(t, dir, "requirements.txt",
		"--index-url https://private.example/simple?token=${TOKEN}\n"+
			"--find-links https://wheels.example/${TOKEN_PREFIX}/w\n")
	// TOKEN_PREFIX's value is a prefix of TOKEN's, so masking it first
	// would leave the rest of TOKEN readable.
	idx := readIndex(t, dir, "TOKEN=zq-abc-123", "TOKEN_PREFIX=zq-abc", "UNUSED=example")

	got := idx.Redact(idx.DefaultIndex + " " + idx.FindLinks[0])
	want := "https://private.example/simple?token=*** https://wheels.example/***/w"
	if got != want {
		t.Errorf("Redact = %q, want %q", got, want)
	}
	// A value no reference used is not masked.
	if got := idx.Redact("https://example.org"); got != "https://example.org" {
		t.Errorf("Redact masked an unreferenced value: %q", got)
	}
}

func TestReadRequirementsIndex_ZeroCases(t *testing.T) {
	t.Run("no requirements.txt", func(t *testing.T) {
		if got := readIndex(t, t.TempDir()); !got.IsZero() {
			t.Fatalf("got %+v", got)
		}
	})
	t.Run("no index options", func(t *testing.T) {
		dir := t.TempDir()
		writeBundleFile(t, dir, "requirements.txt", "shiny\n-e ./localpkg\n--hash=sha256:abc\n")
		if got := readIndex(t, dir); !got.IsZero() {
			t.Fatalf("got %+v", got)
		}
	})
	t.Run("author-shipped pyproject owns the index configuration", func(t *testing.T) {
		dir := t.TempDir()
		writeBundleFile(t, dir, "requirements.txt", "-i https://ignored.example/simple\n")
		writeBundleFile(t, dir, "pyproject.toml", "[project]\nname = \"x\"\n")
		if got := readIndex(t, dir); !got.IsZero() {
			t.Fatalf("got %+v", got)
		}
	})
	t.Run("synthesized pyproject still follows requirements.txt", func(t *testing.T) {
		dir := t.TempDir()
		writeBundleFile(t, dir, "requirements.txt", "-i https://kept.example/simple\n")
		writeBundleFile(t, dir, "pyproject.toml", "[project]\nname = \"x\"\n")
		writeBundleFile(t, dir, process.SynthesizedProjectMarker, "1\n")
		if got := readIndex(t, dir).DefaultIndex; got != "https://kept.example/simple" {
			t.Fatalf("DefaultIndex = %q", got)
		}
	})
}

func TestRequirementsIndex_Env(t *testing.T) {
	idx := process.RequirementsIndex{
		DefaultIndex: "https://bundle.example/simple",
		Indexes:      []string{"https://b1.example/simple", "https://shared.example/simple"},
		FindLinks:    []string{"wheels"},
	}
	base := []string{
		"UV_DEFAULT_INDEX=https://server.example/simple",
		"UV_INDEX=https://shared.example/simple https://server-extra.example/simple",
		"UV_FIND_LINKS=/srv/wheels",
		"UV_EXTRA_INDEX_URL=https://legacy.example/simple",
	}
	got := idx.Env(base)
	want := []string{
		"UV_DEFAULT_INDEX=https://bundle.example/simple",
		// uv tries UV_INDEX before UV_EXTRA_INDEX_URL, and each list in
		// order, so the bundle's extras go last in UV_EXTRA_INDEX_URL: the
		// server's own indexes in both variables keep their priority, and an
		// index the server already lists is not repeated.
		"UV_EXTRA_INDEX_URL=https://legacy.example/simple https://b1.example/simple",
		"UV_FIND_LINKS=/srv/wheels,wheels",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got  %q\nwant %q", got, want)
	}
	for _, e := range got {
		if strings.HasPrefix(e, "UV_INDEX=") {
			t.Errorf("the server's UV_INDEX must be left alone, got %q", e)
		}
	}
}

func TestRequirementsIndex_EnvLastOccurrenceWins(t *testing.T) {
	idx := process.RequirementsIndex{Indexes: []string{"https://b.example/simple"}}
	base := []string{"UV_EXTRA_INDEX_URL=https://stale.example/simple", "UV_EXTRA_INDEX_URL=https://app.example/simple"}
	want := []string{"UV_EXTRA_INDEX_URL=https://app.example/simple https://b.example/simple"}
	if got := idx.Env(base); !reflect.DeepEqual(got, want) {
		t.Fatalf("got %q, want %q (base must be read the way os/exec resolves duplicates)", got, want)
	}
}

func TestRequirementsIndex_ZeroHasNoEnvOrArgs(t *testing.T) {
	var idx process.RequirementsIndex
	if got := idx.Env([]string{"UV_INDEX=https://x.example/simple"}); got != nil {
		t.Errorf("Env = %q, want nil", got)
	}
	if got := idx.Args(); got != nil {
		t.Errorf("Args = %q, want nil", got)
	}
}

func TestRequirementsIndex_Args(t *testing.T) {
	idx := process.RequirementsIndex{NoIndex: true}
	if got := idx.Args(); !reflect.DeepEqual(got, []string{"--no-index"}) {
		t.Fatalf("Args = %q", got)
	}
}

func TestRequirementsIndex_Overrides(t *testing.T) {
	idx := process.RequirementsIndex{DefaultIndex: "https://bundle.example/simple"}
	got := idx.Overrides([]string{
		"UV_INDEX_URL=https://server.example/simple",
		"UV_DEFAULT_INDEX=https://bundle.example/simple",
	})
	if !reflect.DeepEqual(got, []string{"UV_INDEX_URL"}) {
		t.Fatalf("Overrides = %q, want only the base var whose value differs", got)
	}
	if got := (process.RequirementsIndex{}).Overrides([]string{"UV_INDEX_URL=https://x"}); got != nil {
		t.Fatalf("no default index declared, Overrides = %q", got)
	}
}

func TestHasURLCredentials(t *testing.T) {
	cases := map[string]bool{
		"UV_DEFAULT_INDEX=https://u:p@host/simple":                      true,
		"UV_INDEX=https://plain.example/simple https://tok@host/simple": true,
		"UV_FIND_LINKS=wheels,https://u:p@links.example/":               true,
		"UV_INDEX=https://plain.example/simple":                         false,
		"UV_FIND_LINKS=wheels":                                          false,
		"UV_DEFAULT_INDEX=https://host/simple?next=a@b":                 false,
	}
	for entry, want := range cases {
		if got := process.HasURLCredentials(entry); got != want {
			t.Errorf("HasURLCredentials(%q) = %v, want %v", entry, got, want)
		}
	}
}

// Options apply in declaration order, so a file included a second time is
// read again: its --index-url, coming last, is the one that holds.
func TestReadRequirementsIndex_RepeatedIncludeKeepsDeclarationOrder(t *testing.T) {
	dir := t.TempDir()
	writeBundleFile(t, dir, "base.txt", "--index-url https://a.example/simple\n")
	writeBundleFile(t, dir, "requirements.txt", "-r base.txt\n--index-url https://b.example/simple\n-r base.txt\nsix\n")
	idx, err := process.ReadRequirementsIndex(dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	if idx.DefaultIndex != "https://a.example/simple" {
		t.Errorf("DefaultIndex = %q, want the index of the include declared last", idx.DefaultIndex)
	}
}

// An include cycle ends at the file already being read, and an include graph
// that fans out into a huge number of reads fails rather than stalling the
// deploy.
func TestReadRequirementsIndex_IncludeCycleAndFanOutAreBounded(t *testing.T) {
	dir := t.TempDir()
	writeBundleFile(t, dir, "requirements.txt", "--index-url https://a.example/simple\n-r other.txt\n")
	writeBundleFile(t, dir, "other.txt", "-r requirements.txt\nsix\n")
	idx, err := process.ReadRequirementsIndex(dir, nil)
	if err != nil || idx.DefaultIndex != "https://a.example/simple" {
		t.Fatalf("cycle: DefaultIndex = %q, err = %v", idx.DefaultIndex, err)
	}

	fan := t.TempDir()
	names := []string{"requirements.txt", "l1.txt", "l2.txt", "l3.txt", "l4.txt", "l5.txt"}
	for i := 0; i < len(names)-1; i++ {
		writeBundleFile(t, fan, names[i], strings.Repeat("-r "+names[i+1]+"\n", 10))
	}
	writeBundleFile(t, fan, names[len(names)-1], "six\n")
	done := make(chan error, 1)
	go func() { _, err := process.ReadRequirementsIndex(fan, nil); done <- err }()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("want an error for 100000 include reads")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("reading a fanned-out include graph did not finish")
	}
}

// UV_FIND_LINKS is comma-separated, so a find-links URL containing a comma
// cannot reach uv intact; the read fails, with the URL's query masked, rather
// than handing uv two broken links.
func TestReadRequirementsIndex_RejectsCommaInFindLinks(t *testing.T) {
	dir := t.TempDir()
	writeBundleFile(t, dir, "requirements.txt", "-f https://w.example/x?sig=lit-m1,lit-m2\nsix\n")
	_, err := process.ReadRequirementsIndex(dir, nil)
	if err == nil {
		t.Fatal("want an error for a find-links URL containing a comma")
	}
	if strings.Contains(err.Error(), "lit-m") {
		t.Errorf("error leaks the query value: %v", err)
	}
	if !strings.Contains(err.Error(), "https://w.example/x?sig=***") {
		t.Errorf("error %q does not name the masked URL", err)
	}
}
