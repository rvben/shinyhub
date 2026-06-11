package cli

import (
	"archive/zip"
	"bytes"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
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

// DEP-4: `deploy --help` must explain the things a first-time deployer needs:
// the shinyhub.toml manifest, the slug→URL mapping, the slug rules, and that
// the bundle excludes data/cache dirs. The --slug flag usage must state the rule.
func TestDeploy_HelpDocumentsKeyConcepts(t *testing.T) {
	cmd := newDeployCmd()
	long := cmd.Long
	for _, want := range []string{"shinyhub.toml", "/app/", "slug", "exclud"} {
		if !strings.Contains(strings.ToLower(long), strings.ToLower(want)) {
			t.Errorf("deploy --help Long should mention %q, got:\n%s", want, long)
		}
	}
	slugFlag := cmd.Flags().Lookup("slug")
	if slugFlag == nil {
		t.Fatal("expected --slug flag")
	}
	if !strings.Contains(strings.ToLower(slugFlag.Usage), "lowercase") {
		t.Errorf("--slug usage should describe the slug rule, got: %q", slugFlag.Usage)
	}
}

// DEP-3: the default --wait-timeout must be generous enough to cover a
// first-run dependency install (uv/renv can take minutes); a 60s default makes
// a perfectly good deploy time out and read as a failure.
func TestDeploy_WaitTimeoutDefaultIsGenerous(t *testing.T) {
	cmd := newDeployCmd()
	f := cmd.Flags().Lookup("wait-timeout")
	if f == nil {
		t.Fatal("expected --wait-timeout flag")
	}
	if f.DefValue != "300" {
		t.Errorf("expected a generous default wait-timeout (300s), got %s", f.DefValue)
	}
}

// DEP-3: when --wait times out while the app is still in a non-terminal
// "starting" state, the message must make clear the deploy was committed and the
// app is still booting (not failed), and point to logs - rather than reading as
// a hard deploy failure.
// CR2-3: if --wait reads one healthy-but-not-running status and then can no
// longer reach the API (transient 5xx/transport errors) until the timeout, the
// last observations failed, so the actionable diagnostic is the poll error, not
// a reassuring "still starting / has not failed" message.
func TestWaitForHealthy_TimeoutAfterStaleStatusSurfacesPollError(t *testing.T) {
	prev := healthPollInterval
	healthPollInterval = 3 * time.Millisecond
	t.Cleanup(func() { healthPollInterval = prev })

	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == "GET" && r.URL.Path == "/api/apps/demo" && r.URL.RawQuery == "":
			// First poll succeeds (starting); every later poll fails with 5xx.
			if atomic.AddInt32(&calls, 1) == 1 {
				_, _ = w.Write([]byte(`{"app":{"status":"starting"}}`))
				return
			}
			w.WriteHeader(http.StatusBadGateway)
		case r.Method == "GET" && r.URL.Path == "/api/apps/demo/logs":
			w.Header().Set("Content-Type", "text/event-stream")
		default:
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		}
	}))
	defer srv.Close()

	var stderrBuf bytes.Buffer
	err := waitForHealthyWithOutput(&cliConfig{Host: srv.URL, Token: "tok"}, "demo", 60*time.Millisecond, &stderrBuf)
	if err == nil {
		t.Fatal("expected an error when the last polls failed")
	}
	msg := err.Error()
	if strings.Contains(msg, "still starting") || strings.Contains(msg, "has not failed") {
		t.Errorf("must not reassure when the latest polls could not reach the API, got: %q", msg)
	}
	if !strings.Contains(strings.ToLower(msg), "last error") {
		t.Errorf("should surface the last poll error, got: %q", msg)
	}
}

func TestWaitForHealthy_TimeoutWhileStartingReadsAsStillBooting(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == "GET" && r.URL.Path == "/api/apps/demo" && r.URL.RawQuery == "":
			_, _ = w.Write([]byte(`{"app":{"status":"starting"}}`))
		case r.Method == "GET" && r.URL.Path == "/api/apps/demo/logs":
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = w.Write([]byte("data: installing dependencies...\n\n"))
		default:
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		}
	}))
	defer srv.Close()

	var stderrBuf bytes.Buffer
	err := waitForHealthyWithOutput(&cliConfig{Host: srv.URL, Token: "tok"}, "demo", 50*time.Millisecond, &stderrBuf)
	if err == nil {
		t.Fatal("expected a timeout error")
	}
	msg := err.Error()
	if !strings.Contains(msg, "still starting") {
		t.Errorf("timeout message should say the app is still starting, got: %q", msg)
	}
	if !strings.Contains(strings.ToLower(msg), "deploy") {
		t.Errorf("timeout message should clarify the deploy was committed, got: %q", msg)
	}
	if !strings.Contains(msg, "--wait-timeout") {
		t.Errorf("timeout message should suggest raising --wait-timeout, got: %q", msg)
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

	err := runDeploy(&cobra.Command{}, []string{badDir}, &deployFlags{})
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

	err := ensureApp(&cliConfig{Host: srv.URL, Token: "tok"}, "full", "")
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

	// A fresh command tree means no --git/--slug can leak in from a prior test.
	_, err := execCLI(t, "deploy")
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
		ready, status, err := pollAppStatus(&cliConfig{Host: srv.URL, Token: "tok"}, "demo")
		if err != nil {
			t.Fatalf("pollAppStatus: %v", err)
		}
		if !ready {
			t.Errorf("ready = false, want true")
		}
		if status != "running" {
			t.Errorf("status = %q, want \"running\"", status)
		}
	})
	t.Run("starting returns false", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte(`{"app":{"status":"starting"}}`))
		}))
		defer srv.Close()
		ready, status, err := pollAppStatus(&cliConfig{Host: srv.URL, Token: "tok"}, "demo")
		if err != nil {
			t.Fatalf("pollAppStatus: %v", err)
		}
		if ready {
			t.Errorf("ready = true, want false")
		}
		if status != "starting" {
			t.Errorf("status = %q, want \"starting\"", status)
		}
	})
}

// TestPollAppStatus_NonOKStatusReturnsHTTPError guards against the previous
// behaviour where any non-2xx response (token expired, app gone, server 500)
// silently decoded into a zero-value status and degraded to ready=false.
// waitForHealthy then polled until --wait-timeout for an outcome that was
// already decided. pollAppStatus must surface *deployHTTPError so the caller
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
			ready, _, err := pollAppStatus(&cliConfig{Host: srv.URL, Token: "tok"}, "demo")
			if err == nil {
				t.Fatalf("pollAppStatus: expected error for HTTP %d, got nil", tc.statusCode)
			}
			if ready {
				t.Errorf("ready = true, want false on HTTP %d", tc.statusCode)
			}
			var he *deployHTTPError
			if !errors.As(err, &he) {
				t.Fatalf("pollAppStatus: error %v is not *deployHTTPError; waitForHealthy can't classify it", err)
			}
			if he.statusCode != tc.statusCode {
				t.Errorf("deployHTTPError.statusCode = %d, want %d", he.statusCode, tc.statusCode)
			}
			if he.fatal() != tc.fatal {
				t.Errorf("deployHTTPError.fatal() = %v, want %v for HTTP %d", he.fatal(), tc.fatal, tc.statusCode)
			}
			if tc.body != "" && !strings.Contains(he.Error(), strings.TrimSpace(tc.body)) {
				t.Errorf("deployHTTPError.Error() = %q, want it to surface server body %q", he.Error(), tc.body)
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

	err := ensureApp(&cliConfig{Host: srv.URL, Token: "tok"}, "proxy", "")
	if err == nil || !strings.Contains(err.Error(), "upstream timeout") {
		t.Errorf("expected raw body in error, got %v", err)
	}
}

// TestParseSSELines_LastN verifies that parseSSELines reads SSE data: lines
// from an io.Reader and returns the last n lines.
func TestParseSSELines_LastN(t *testing.T) {
	lines := []string{
		"line one",
		"line two",
		"line three",
		"line four",
		"line five",
	}
	var buf strings.Builder
	for _, l := range lines {
		buf.WriteString("data: " + l + "\n\n")
	}
	// Also include a heartbeat comment to verify it is ignored.
	buf.WriteString(": heartbeat\n\n")

	got := parseSSELines(strings.NewReader(buf.String()), 3)
	want := []string{"line three", "line four", "line five"}
	if len(got) != len(want) {
		t.Fatalf("parseSSELines returned %d lines, want %d: %v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("line[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

// TestParseSSELines_FewerThanN verifies that parseSSELines returns all lines
// when the stream contains fewer than n lines.
func TestParseSSELines_FewerThanN(t *testing.T) {
	var buf strings.Builder
	buf.WriteString("data: only line\n\n")

	got := parseSSELines(strings.NewReader(buf.String()), 20)
	if len(got) != 1 || got[0] != "only line" {
		t.Errorf("parseSSELines returned %v, want [\"only line\"]", got)
	}
}

// TestParseSSELines_Empty verifies that parseSSELines returns nil on empty input.
func TestParseSSELines_Empty(t *testing.T) {
	got := parseSSELines(strings.NewReader(""), 20)
	if len(got) != 0 {
		t.Errorf("parseSSELines on empty input returned %v, want empty", got)
	}
}

// TestWaitForHealthy_TimeoutPrintsLogTail verifies that when waitForHealthy
// times out, it fetches the app logs and prints the tail to stderr.
func TestWaitForHealthy_TimeoutPrintsLogTail(t *testing.T) {
	sseBody := "data: Error in library(shiny) : there is no package called 'shiny'\n\n" +
		"data: Execution halted\n\n"

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == "GET" && r.URL.Path == "/api/apps/demo" && r.URL.RawQuery == "":
			_, _ = w.Write([]byte(`{"app":{"status":"starting"}}`))
		case r.Method == "GET" && r.URL.Path == "/api/apps/demo/logs":
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = w.Write([]byte(sseBody))
		default:
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		}
	}))
	defer srv.Close()

	var stderrBuf bytes.Buffer
	err := waitForHealthyWithOutput(&cliConfig{Host: srv.URL, Token: "tok"}, "demo", 50*time.Millisecond, &stderrBuf)

	if err == nil {
		t.Fatal("waitForHealthyWithOutput: expected timeout error, got nil")
	}
	if !strings.Contains(err.Error(), "timed out") {
		t.Errorf("error should mention timed out, got %q", err.Error())
	}

	stderr := stderrBuf.String()
	if !strings.Contains(stderr, "Error in library(shiny)") {
		t.Errorf("stderr should contain log line, got %q", stderr)
	}
	if !strings.Contains(stderr, "Execution halted") {
		t.Errorf("stderr should contain log line, got %q", stderr)
	}
	if !strings.Contains(stderr, "shinyhub apps logs demo") {
		t.Errorf("stderr should contain hint pointing to shinyhub apps logs, got %q", stderr)
	}
}

// TestWaitForHealthy_CrashLoopPrintsLogTail verifies that when the app enters
// a crashed state during the wait window, the log tail is printed.
func TestWaitForHealthy_CrashLoopPrintsLogTail(t *testing.T) {
	sseBody := "data: Traceback (most recent call last):\n\n" +
		"data: ImportError: No module named 'shiny'\n\n"

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == "GET" && r.URL.Path == "/api/apps/crash-app" && r.URL.RawQuery == "":
			_, _ = w.Write([]byte(`{"app":{"status":"crashed"}}`))
		case r.Method == "GET" && r.URL.Path == "/api/apps/crash-app/logs":
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = w.Write([]byte(sseBody))
		default:
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		}
	}))
	defer srv.Close()

	var stderrBuf bytes.Buffer
	err := waitForHealthyWithOutput(&cliConfig{Host: srv.URL, Token: "tok"}, "crash-app", 30*time.Second, &stderrBuf)

	if err == nil {
		t.Fatal("waitForHealthyWithOutput: expected error on crashed status, got nil")
	}
	if !strings.Contains(err.Error(), "crash-app") {
		t.Errorf("error should mention slug, got %q", err.Error())
	}

	stderr := stderrBuf.String()
	if !strings.Contains(stderr, "ImportError") {
		t.Errorf("stderr should contain log line, got %q", stderr)
	}
	if !strings.Contains(stderr, "shinyhub apps logs crash-app") {
		t.Errorf("stderr should contain hint, got %q", stderr)
	}
}

// TestWaitForHealthy_StoppedIsNotTerminal verifies that "stopped" does not
// trigger an immediate failure, because a deliberate stop followed by a
// redeploy can briefly surface "stopped" as a transient state.
func TestWaitForHealthy_StoppedIsNotTerminal(t *testing.T) {
	callCount := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == "GET" && r.URL.Path == "/api/apps/demo" && r.URL.RawQuery == "":
			callCount++
			if callCount == 1 {
				_, _ = w.Write([]byte(`{"app":{"status":"stopped"}}`))
			} else {
				_, _ = w.Write([]byte(`{"app":{"status":"running"}}`))
			}
		default:
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		}
	}))
	defer srv.Close()

	var stderrBuf bytes.Buffer
	err := waitForHealthyWithOutput(&cliConfig{Host: srv.URL, Token: "tok"}, "demo", 10*time.Second, &stderrBuf)
	if err != nil {
		t.Fatalf("waitForHealthyWithOutput: unexpected error for stopped→running transition: %v", err)
	}
	if stderrBuf.Len() > 0 {
		t.Errorf("stderr should be empty on success, got %q", stderrBuf.String())
	}
}

// TestPrintLogTail_WarnsOnLogsEndpointFailure verifies that printLogTail
// writes a warning line when the logs endpoint returns a non-2xx status,
// so the user sees actionable output instead of silence.
func TestPrintLogTail_WarnsOnLogsEndpointFailure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	var buf bytes.Buffer
	printLogTail(&cliConfig{Host: srv.URL, Token: "tok"}, "demo", &buf)

	out := buf.String()
	if !strings.Contains(out, "warning: could not fetch logs") {
		t.Errorf("printLogTail should write a warning on 500, got %q", out)
	}
	if !strings.Contains(out, "500") {
		t.Errorf("printLogTail warning should mention the status, got %q", out)
	}
}

// TestWaitForHealthy_SuccessEmitsNoLogTail verifies that on success, no log
// lines are printed to stderr.
func TestWaitForHealthy_SuccessEmitsNoLogTail(t *testing.T) {
	logRequested := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api/apps/demo" && r.URL.RawQuery == "":
			_, _ = w.Write([]byte(`{"app":{"status":"running"}}`))
		case r.URL.Path == "/api/apps/demo/logs":
			logRequested = true
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = w.Write([]byte("data: some log line\n\n"))
		}
	}))
	defer srv.Close()

	var stderrBuf bytes.Buffer
	err := waitForHealthyWithOutput(&cliConfig{Host: srv.URL, Token: "tok"}, "demo", 30*time.Second, &stderrBuf)
	if err != nil {
		t.Fatalf("waitForHealthyWithOutput: unexpected error %v", err)
	}
	if logRequested {
		t.Error("log endpoint should not be called on success")
	}
	if stderrBuf.Len() > 0 {
		t.Errorf("stderr should be empty on success, got %q", stderrBuf.String())
	}
}

// TestEnsureApp_ExistingAppWithVisibility_Warns verifies that when the app
// already exists and --visibility is set, a warning is written to stderr and
// the call succeeds without making a create request.
func TestEnsureApp_ExistingAppWithVisibility_Warns(t *testing.T) {
	createCalled := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/apps/demo":
			// App already exists.
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"app":{"slug":"demo","status":"running"}}`))
		case "/api/apps":
			createCalled = true
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"slug":"demo"}`))
		default:
			t.Errorf("unexpected request to %s", r.URL.Path)
		}
	}))
	defer srv.Close()

	var stderrBuf bytes.Buffer
	err := ensureAppWithOutput(&cliConfig{Host: srv.URL, Token: "tok"}, "demo", "public", &stderrBuf)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if createCalled {
		t.Error("create endpoint should not be called when app already exists")
	}

	out := stderrBuf.String()
	if !strings.Contains(out, "warning:") {
		t.Errorf("expected a warning on stderr, got: %q", out)
	}
	if !strings.Contains(out, "--visibility") {
		t.Errorf("warning should mention --visibility, got: %q", out)
	}
	if !strings.Contains(out, "shinyhub apps access set demo public") {
		t.Errorf("warning should include the corrective command, got: %q", out)
	}
}

// TestEnsureApp_ExistingAppWithoutVisibility_NoWarn verifies that when the app
// exists and visibility is empty, no warning is emitted.
func TestEnsureApp_ExistingAppWithoutVisibility_NoWarn(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"app":{"slug":"demo"}}`))
	}))
	defer srv.Close()

	var stderrBuf bytes.Buffer
	err := ensureAppWithOutput(&cliConfig{Host: srv.URL, Token: "tok"}, "demo", "", &stderrBuf)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if stderrBuf.Len() > 0 {
		t.Errorf("expected no stderr output when visibility is empty, got: %q", stderrBuf.String())
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

func writeFile(t *testing.T, dir, rel, content string) {
	t.Helper()
	full := filepath.Join(dir, rel)
	if err := os.MkdirAll(filepath.Dir(full), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
}

func zipNames(t *testing.T, buf *bytes.Buffer) []string {
	t.Helper()
	r, err := zip.NewReader(bytes.NewReader(buf.Bytes()), int64(buf.Len()))
	if err != nil {
		t.Fatal(err)
	}
	names := make([]string, 0, len(r.File))
	for _, f := range r.File {
		names = append(names, f.Name)
	}
	return names
}

func contains(ss []string, s string) bool {
	for _, x := range ss {
		if x == s {
			return true
		}
	}
	return false
}

func containsPrefix(ss []string, prefix string) bool {
	for _, x := range ss {
		if strings.HasPrefix(x, prefix) {
			return true
		}
	}
	return false
}

// TestEnsureApp_ForwardsVisibility verifies that a non-empty visibility is
// forwarded in the JSON create body and an empty visibility is omitted.
func TestEnsureApp_ForwardsVisibility(t *testing.T) {
	cases := []struct {
		name       string
		visibility string
		wantAccess string // "" means the field should be absent in the body
	}{
		{"empty omits field", "", ""},
		{"public is forwarded", "public", "public"},
		{"shared is forwarded", "shared", "shared"},
		{"private is forwarded", "private", "private"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var capturedBody string
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/api/apps/demo":
					w.WriteHeader(http.StatusNotFound)
				case "/api/apps":
					b := new(bytes.Buffer)
					b.ReadFrom(r.Body)
					capturedBody = b.String()
					w.WriteHeader(http.StatusCreated)
					_, _ = w.Write([]byte(`{"slug":"demo"}`))
				default:
					t.Errorf("unexpected request to %s", r.URL.Path)
				}
			}))
			defer srv.Close()

			if err := ensureApp(&cliConfig{Host: srv.URL, Token: "tok"}, "demo", tc.visibility); err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			if tc.wantAccess == "" {
				if strings.Contains(capturedBody, `"access"`) {
					t.Errorf("access field should not be present when visibility is empty, got body: %s", capturedBody)
				}
			} else {
				if !strings.Contains(capturedBody, `"access":"`+tc.wantAccess+`"`) {
					t.Errorf("want access=%q in body, got: %s", tc.wantAccess, capturedBody)
				}
			}
		})
	}
}

func TestZipDir_HonorsShinyhubIgnore(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "app.py", "print('hi')")
	writeFile(t, dir, "cached_data/large.bin", "x")
	writeFile(t, dir, ".shinyhubignore", "cached_data/\n")

	buf, _, err := zipDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	names := zipNames(t, buf)
	if !contains(names, "app.py") {
		t.Errorf("app.py missing: %v", names)
	}
	if containsPrefix(names, "cached_data/") {
		t.Errorf("cached_data should be excluded: %v", names)
	}
}

func TestZipDir_FallsBackToGitignore(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "app.py", "print('hi')")
	writeFile(t, dir, "scratch/x.txt", "x")
	writeFile(t, dir, ".gitignore", "scratch/\n")

	buf, _, err := zipDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	names := zipNames(t, buf)
	if containsPrefix(names, "scratch/") {
		t.Errorf("scratch should be excluded via .gitignore fallback: %v", names)
	}
	if !contains(names, "app.py") {
		t.Errorf("app.py missing: %v", names)
	}
}

func TestZipDir_ShinyhubIgnoreShadowsGitignore(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "app.py", "print('hi')")
	writeFile(t, dir, "wanted/keep.txt", "k")
	writeFile(t, dir, ".gitignore", "wanted/\n")
	writeFile(t, dir, ".shinyhubignore", "# nothing to ignore\n")

	buf, _, err := zipDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	names := zipNames(t, buf)
	if !contains(names, "wanted/keep.txt") {
		t.Errorf("wanted/keep.txt should ship when .shinyhubignore overrides .gitignore: %v", names)
	}
}

func TestZipDir_NoIgnoreFilesPreservesCurrentBehavior(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "app.py", "print('hi')")
	writeFile(t, dir, "extra.py", "")

	buf, _, err := zipDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	names := zipNames(t, buf)
	if !contains(names, "app.py") || !contains(names, "extra.py") {
		t.Errorf("expected both files to ship without an ignore file: %v", names)
	}
}

func TestZipDir_IgnoreFileNegation(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "app.py", "")
	writeFile(t, dir, "scratch/keep.txt", "k")
	writeFile(t, dir, "scratch/skip.txt", "s")
	writeFile(t, dir, ".shinyhubignore", "scratch/*\n!scratch/keep.txt\n")

	buf, _, err := zipDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	names := zipNames(t, buf)
	if !contains(names, "scratch/keep.txt") {
		t.Errorf("keep.txt should be included via !-negation: %v", names)
	}
	if contains(names, "scratch/skip.txt") {
		t.Errorf("skip.txt should be excluded: %v", names)
	}
}

func TestZipDir_IgnoreFileDoesNotShipItself(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "app.py", "")
	writeFile(t, dir, ".shinyhubignore", "*.log\n")

	buf, _, err := zipDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	names := zipNames(t, buf)
	if !contains(names, ".shinyhubignore") {
		t.Errorf(".shinyhubignore should ship by default; users can self-exclude: %v", names)
	}
}

// TestZipDir_IgnoreFileSelfExcludes confirms operators can omit the ignore
// file from the bundle by listing it in its own patterns.
func TestZipDir_IgnoreFileSelfExcludes(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "app.py", "")
	writeFile(t, dir, ".shinyhubignore", ".shinyhubignore\n")

	buf, _, err := zipDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	names := zipNames(t, buf)
	if contains(names, ".shinyhubignore") {
		t.Errorf(".shinyhubignore should be excluded when self-listed: %v", names)
	}
	if !contains(names, "app.py") {
		t.Errorf("app.py missing: %v", names)
	}
}

// TestZipDir_PrunesIgnoredDirectory verifies that files inside a directory
// matched by a trailing-slash pattern (e.g. `cached_data/`) do not appear in
// the bundle. The absence of `!`-negation lines in the ignore file means the
// walker can safely prune the subtree via filepath.SkipDir rather than
// descending and filtering each child individually.
func TestZipDir_PrunesIgnoredDirectory(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "app.py", "")
	for i := 0; i < 5; i++ {
		writeFile(t, dir, fmt.Sprintf("cached_data/file_%d.bin", i), "x")
	}
	writeFile(t, dir, ".shinyhubignore", "cached_data/\n")

	buf, _, err := zipDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	names := zipNames(t, buf)
	for _, n := range names {
		if strings.HasPrefix(n, "cached_data/") {
			t.Errorf("cached_data file should not appear in bundle: %s", n)
		}
	}
}

// TestLoadIgnoreMatcher_DetectsNegation verifies that ignoreFileHasNegation
// correctly identifies files containing `!`-prefixed patterns. This pins the
// pruning decision: when hasNegation is false, the walker may safely
// filepath.SkipDir on a matched directory; when true, it must descend.
func TestLoadIgnoreMatcher_DetectsNegation(t *testing.T) {
	cases := []struct {
		name    string
		content string
		wantNeg bool
	}{
		{"no patterns", "", false},
		{"plain pattern", "foo\nbar/\n", false},
		{"comment with bang", "# !this is just a comment\nfoo\n", false},
		{"negation present", "foo/*\n!foo/keep.txt\n", true},
		{"leading whitespace bang", "  !indented\n", true},
		{"blank lines", "\n\nfoo\n\n", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, ".shinyhubignore"), []byte(tc.content), 0o644); err != nil {
				t.Fatal(err)
			}
			_, hasNeg, err := loadIgnoreMatcher(dir)
			if err != nil {
				t.Fatal(err)
			}
			if hasNeg != tc.wantNeg {
				t.Errorf("hasNegation = %v, want %v", hasNeg, tc.wantNeg)
			}
		})
	}
}

// TestZipDir_StatErrorOnIgnoreFile surfaces non-ENOENT read failures rather
// than silently falling through. Make .shinyhubignore unreadable via parent
// dir chmod so ReadFile itself fails with EACCES rather than ENOENT. Skip on
// Windows where chmod semantics differ.
func TestZipDir_StatErrorOnIgnoreFile(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unix permission semantics required")
	}
	dir := t.TempDir()
	writeFile(t, dir, "app.py", "")
	if err := os.MkdirAll(filepath.Join(dir, "subroot"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "subroot", ".shinyhubignore"), []byte("foo\n"), 0o000); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(filepath.Join(dir, "subroot"), 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(filepath.Join(dir, "subroot"), 0o755) })

	if _, _, err := loadIgnoreMatcher(filepath.Join(dir, "subroot")); err == nil {
		t.Errorf("expected error from read-on-unreadable parent, got nil")
	}
}

// TestDeploy_FirstFire_ReportedAndWaited verifies that --wait-for-warm polls
// the first-fire run endpoint after deploy and exits 0 on "succeeded".
func TestDeploy_FirstFire_ReportedAndWaited(t *testing.T) {
	var runPolls int
	mux := http.NewServeMux()
	mux.HandleFunc("/api/apps/warmapp", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"app":{"status":"running"}}`))
	})
	mux.HandleFunc("/api/apps/warmapp/deploy", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"deploy_count":1,"manifest":{"schedules":[{"name":"warm","action":"created","schedule_id":5,"first_fire":{"run_id":42}}]}}`))
	})
	mux.HandleFunc("/api/apps/warmapp/schedules/5/runs/42", func(w http.ResponseWriter, r *http.Request) {
		runPolls++
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"status":"succeeded"}`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	// Write a minimal bundle dir with an app.py so zipDir succeeds.
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "app.py"), []byte("# shiny\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Point loadConfig() at the test server via writeTestCLIConfig.
	writeTestCLIConfig(t, srv.URL)

	cmd := newDeployCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{dir, "--slug", "warmapp", "--wait-for-warm"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("execute: %v", err)
	}
	if runPolls == 0 {
		t.Errorf("--wait-for-warm did not poll the run endpoint")
	}
}

// TestDeploy_FirstFire_FailureIsFatal verifies that a genuine first-fire failure
// causes --wait-for-warm to return a non-nil error (non-zero exit).
func TestDeploy_FirstFire_FailureIsFatal(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/apps/warmapp", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"app":{"status":"running"}}`))
	})
	mux.HandleFunc("/api/apps/warmapp/deploy", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"deploy_count":1,"manifest":{"schedules":[{"name":"warm","action":"created","schedule_id":5,"first_fire":{"run_id":42}}]}}`))
	})
	mux.HandleFunc("/api/apps/warmapp/schedules/5/runs/42", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"status":"failed"}`))
	})
	mux.HandleFunc("/api/apps/warmapp/schedules/5/runs/42/logs", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("data: boom\n\n"))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "app.py"), []byte("# shiny\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	writeTestCLIConfig(t, srv.URL)

	cmd := newDeployCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{dir, "--slug", "warmapp", "--wait-for-warm"})
	if err := cmd.Execute(); err == nil {
		t.Fatalf("expected non-nil error when first-fire fails under --wait-for-warm")
	}
}
