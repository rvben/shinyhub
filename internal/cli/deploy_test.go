package cli

import (
	"archive/zip"
	"bytes"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	slugpkg "github.com/rvben/shinyhub/internal/slug"
)

// TestDeploy_SlugValidation_DelegatesToSharedPackage guards against the CLI
// regex drifting from the shared validator. The CLI used to define its own
// regex that differed from the API server's, which let users create slugs in
// the UI that the CLI then refused to deploy.
func TestDeploy_SlugValidation_DelegatesToSharedPackage(t *testing.T) {
	cases := []struct {
		slug  string
		valid bool
	}{
		{"my-app", true},
		{"myapp", true},
		{"a", true},
		{"a0", true},
		{"MyApp", false},
		{"UPPER", false},
		{"my_app", false},
		{"-leading", false},
		{"trailing-", false}, // DNS labels cannot end in a hyphen
		{"my app", false},
	}
	for _, tc := range cases {
		if got := slugpkg.Valid(tc.slug); got != tc.valid {
			t.Errorf("slugpkg.Valid(%q) = %v, want %v", tc.slug, got, tc.valid)
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

// TestSanitizeSlug_TruncationProducesValidSlug guards against the bug where
// sanitizeSlug trimmed trailing dashes *before* truncating, so an input long
// enough to be cut at a `-` character produced a slug ending in `-` — which
// slugpkg.Valid rejects, causing a server-side 400 on deploy.
//
// Each case is constructed so that the 63rd byte after dash-collapsing is `-`.
// We assert (a) the output is no longer than 63 chars and (b) slugpkg.Valid
// accepts it.
func TestSanitizeSlug_TruncationProducesValidSlug(t *testing.T) {
	cases := []string{
		// 62 'a's then a dash then more — truncating to 63 lands on '-'.
		strings.Repeat("a", 62) + "-bcdef",
		// Many short tokens separated by spaces — each space becomes a dash.
		// Long enough that truncation will land in the middle of a dash run.
		strings.Repeat("ab ", 30),
		// Pathological: alternating chars and dashes for >63 bytes.
		strings.Repeat("a-", 40),
	}
	for _, in := range cases {
		got := sanitizeSlug(in)
		if len(got) > slugpkg.MaxLen {
			t.Errorf("sanitizeSlug(%q): len=%d > %d", in, len(got), slugpkg.MaxLen)
		}
		if !slugpkg.Valid(got) {
			t.Errorf("sanitizeSlug(%q) = %q, which slugpkg.Valid rejects", in, got)
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

// ensureApp must surface the server's error envelope so the user sees
// "quota exceeded" / "invalid slug" / etc. instead of the generic
// "could not create app".
func TestEnsureApp_SurfacesServerErrorBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/apps/full":
			w.WriteHeader(http.StatusNotFound)
		case "/api/apps":
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(`{"error":"app quota exceeded"}`))
		default:
			t.Errorf("unexpected request to %s", r.URL.Path)
		}
	}))
	defer srv.Close()

	err := ensureApp(&cliConfig{Host: srv.URL, Token: "tok"}, "full")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "app quota exceeded") {
		t.Errorf("error should surface the server message, got %q", err.Error())
	}
}

// TestDeploy_RequiresExplicitDirArgument guards against the "stray
// `shinyhub deploy` from $PWD" footgun. Without an explicit positional arg
// the command should refuse to run rather than silently bundling the
// current working directory.
func TestDeploy_RequiresExplicitDirArgument(t *testing.T) {
	_, reqs, _ := setupCLITest(t)
	// Reset deploy flags so a previous --git or --slug doesn't leak in.
	deployFlags.git = ""
	deployFlags.slug = ""
	deployFlags.wait = false
	deployFlags.branch = ""
	deployFlags.subdir = ""

	rootForTest := &cobra.Command{Use: "root"}
	rootForTest.AddCommand(deployCmd)
	rootForTest.SetArgs([]string{"deploy"})
	err := rootForTest.Execute()
	if err == nil {
		t.Fatal("expected error when no directory argument is given, got nil")
	}
	if !strings.Contains(err.Error(), "missing directory argument") {
		t.Errorf("error should mention 'missing directory argument', got: %v", err)
	}
	if len(*reqs) != 0 {
		t.Errorf("expected no HTTP requests when arg validation fails, got %d", len(*reqs))
	}
}

// TestPollAppStatus_RunningAndStarting guards the contract of the helper
// extracted from waitForHealthy. The extraction was driven by an fd leak —
// the previous loop body called `defer resp.Body.Close()` inside the
// polling for-range, so bodies stayed open until the command returned.
// Running each poll inside its own function ensures `defer` fires per
// iteration; this test pins the function's true/false return semantics so
// nobody silently inlines the loop again.
func TestPollAppStatus_RunningAndStarting(t *testing.T) {
	t.Run("running returns true", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte(`{"app":{"status":"running"}}`))
		}))
		defer srv.Close()
		ready, err := pollAppStatus(&cliConfig{Host: srv.URL, Token: "tok"}, "demo")
		if err != nil {
			t.Fatalf("pollAppStatus: %v", err)
		}
		if !ready {
			t.Errorf("ready = false, want true")
		}
	})
	t.Run("starting returns false", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte(`{"app":{"status":"starting"}}`))
		}))
		defer srv.Close()
		ready, err := pollAppStatus(&cliConfig{Host: srv.URL, Token: "tok"}, "demo")
		if err != nil {
			t.Fatalf("pollAppStatus: %v", err)
		}
		if ready {
			t.Errorf("ready = true, want false")
		}
	})
}

func TestEnsureApp_FallsBackToRawBodyWhenNotJSON(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/apps/proxy":
			w.WriteHeader(http.StatusNotFound)
		case "/api/apps":
			w.WriteHeader(http.StatusBadGateway)
			_, _ = w.Write([]byte("upstream timeout"))
		}
	}))
	defer srv.Close()

	err := ensureApp(&cliConfig{Host: srv.URL, Token: "tok"}, "proxy")
	if err == nil || !strings.Contains(err.Error(), "upstream timeout") {
		t.Errorf("expected raw body in error, got %v", err)
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
