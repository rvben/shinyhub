package cli

import (
	"archive/zip"
	"bytes"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

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

// TestDeploy_DerivedSlugInvalid_FailsLocally guards against the local
// path: a directory whose basename sanitizes to an empty/invalid slug
// (e.g. "---", emoji-only, all punctuation) used to fall through into
// /api/apps/ and surface a confusing server-side 404 or 400. The CLI
// must catch this before any network call so the user gets a clear
// "pass --slug explicitly" hint instead.
func TestDeploy_DerivedSlugInvalid_FailsLocally(t *testing.T) {
	// `---` collapses to "" through sanitizeSlug (regex replaces non-
	// alphanumerics with `-`, then strings.Trim strips leading/trailing
	// dashes). Other equivalent triggers: a single `.`, an emoji-only
	// name, etc. — they all produce an empty result that slugpkg.Valid
	// rejects.
	parent := t.TempDir()
	badDir := filepath.Join(parent, "---")
	if err := os.MkdirAll(badDir, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	prevSlug, prevGit := deployFlags.slug, deployFlags.git
	deployFlags.slug = ""
	deployFlags.git = ""
	t.Cleanup(func() {
		deployFlags.slug = prevSlug
		deployFlags.git = prevGit
	})

	err := runDeploy(deployCmd, []string{badDir})
	if err == nil {
		t.Fatal("expected error from invalid derived slug, got nil — runDeploy should reject before any network call")
	}
	if !strings.Contains(err.Error(), "could not derive a valid slug") {
		t.Fatalf("expected error mentioning 'could not derive a valid slug' so the user knows to pass --slug, got: %v", err)
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

// TestPollAppStatus_NonOKStatusReturnsHTTPError guards against the previous
// behaviour where any non-2xx response (token expired, app gone, server 500)
// silently decoded into a zero-value status and degraded to ready=false.
// waitForHealthy then polled until --wait-timeout for an outcome that was
// already decided. pollAppStatus must surface *httpStatusError so the caller
// can distinguish fatal (4xx) vs transient (5xx).
func TestPollAppStatus_NonOKStatusReturnsHTTPError(t *testing.T) {
	cases := []struct {
		name       string
		statusCode int
		body       string
		fatal      bool
	}{
		{"401 unauthorized is fatal", http.StatusUnauthorized, `{"error":"invalid token"}`, true},
		{"403 forbidden is fatal", http.StatusForbidden, `{"error":"no view access"}`, true},
		{"404 not found is fatal", http.StatusNotFound, ``, true},
		{"500 server error is transient", http.StatusInternalServerError, `internal error`, false},
		{"502 bad gateway is transient", http.StatusBadGateway, `upstream timeout`, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tc.statusCode)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer srv.Close()
			ready, err := pollAppStatus(&cliConfig{Host: srv.URL, Token: "tok"}, "demo")
			if err == nil {
				t.Fatalf("pollAppStatus: expected error for HTTP %d, got nil", tc.statusCode)
			}
			if ready {
				t.Errorf("ready = true, want false on HTTP %d", tc.statusCode)
			}
			var he *httpStatusError
			if !errors.As(err, &he) {
				t.Fatalf("pollAppStatus: error %v is not *httpStatusError; waitForHealthy can't classify it", err)
			}
			if he.statusCode != tc.statusCode {
				t.Errorf("httpStatusError.statusCode = %d, want %d", he.statusCode, tc.statusCode)
			}
			if he.fatal() != tc.fatal {
				t.Errorf("httpStatusError.fatal() = %v, want %v for HTTP %d", he.fatal(), tc.fatal, tc.statusCode)
			}
			if tc.body != "" && !strings.Contains(he.Error(), strings.TrimSpace(tc.body)) {
				t.Errorf("httpStatusError.Error() = %q, want it to surface server body %q", he.Error(), tc.body)
			}
		})
	}
}

// TestWaitForHealthy_FailFastOn4xx guards the user-visible behaviour: if the
// token is invalid or the app vanished, waitForHealthy must return promptly
// instead of polling for the full --wait-timeout. We use a tight timeout so a
// regression (no fail-fast) blows up the test runner instead of silently
// passing.
func TestWaitForHealthy_FailFastOn4xx(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":"invalid token"}`))
	}))
	defer srv.Close()

	start := time.Now()
	err := waitForHealthy(&cliConfig{Host: srv.URL, Token: "tok"}, "demo", 30*time.Second)
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("waitForHealthy: expected error on 401, got nil")
	}
	if !strings.Contains(err.Error(), "invalid token") {
		t.Errorf("error should surface server body, got %q", err.Error())
	}
	// Fail-fast: should return well under the 30s timeout. Allow generous
	// slack for slow CI but flag anything that suggests the loop kept polling.
	if elapsed > 5*time.Second {
		t.Errorf("waitForHealthy took %v on 401; should fail fast in <5s, not poll until --wait-timeout", elapsed)
	}
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
