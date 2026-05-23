package cli

import (
	"archive/zip"
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	gitignore "github.com/sabhiram/go-gitignore"

	"github.com/rvben/shinyhub/internal/bundle"
	"github.com/rvben/shinyhub/internal/db"
	slugpkg "github.com/rvben/shinyhub/internal/slug"
	"github.com/spf13/cobra"
)

var slugInvalidRE = regexp.MustCompile(`[^a-z0-9]+`)

// sanitizeSlug lowercases the name, replaces runs of non-alphanumeric characters
// with a single dash, and produces a result that satisfies the canonical slug
// rule (see internal/slug). Truncation happens before the trailing-dash trim
// because cutting a 64th-position dash off mid-string would otherwise leave a
// slug ending in `-`, which slugpkg.Valid rejects.
func sanitizeSlug(name string) string {
	s := strings.ToLower(name)
	s = slugInvalidRE.ReplaceAllString(s, "-")
	if len(s) > slugpkg.MaxLen {
		s = s[:slugpkg.MaxLen]
	}
	s = strings.Trim(s, "-")
	return s
}

// deployFlags holds the parsed flags for a single `deploy` invocation. It is
// constructed fresh per command instance (no package-level state) so repeated
// or shuffled test runs cannot leak flag values between each other.
type deployFlags struct {
	slug        string
	wait        bool
	waitTimeout int    // seconds
	git         string // git repo URL; if set, clone instead of using local dir
	branch      string // branch/tag to check out (default: default branch)
	subdir      string // subdirectory within the repo containing the app
	visibility  string // app access level: private, shared, public (empty = use server default)
}

// newDeployCmd builds a fresh deploy command each time it is called, with its
// flags bound to a per-instance deployFlags value.
func newDeployCmd() *cobra.Command {
	f := &deployFlags{}
	cmd := &cobra.Command{
		Use:   "deploy [dir]",
		Short: "Deploy a Shiny app to ShinyHub",
		Long: `Deploy a Shiny app bundle to ShinyHub.

Bundle: the given directory is zipped and uploaded. Pass '.' to deploy the
current directory, or a path like './app'. Data and cache directories are
excluded automatically; add a .shinyhubignore (or .gitignore) to exclude more.
Validate the bundle's optional manifest first with 'shinyhub manifest validate'.

Manifest: if the bundle contains a shinyhub.toml at its root, ShinyHub applies
it on deploy - [app] scaling/hibernate overrides, [[hook]] post-deploy commands,
and [[schedule]] cron jobs. The manifest is optional.

Slug and URL: the app is served at <host>/app/<slug>/. The slug defaults to the
directory name (sanitized); override it with --slug. Slug rule: lowercase
letters, digits, and single hyphens; it must not start or end with a hyphen.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runDeploy(cmd, args, f)
		},
	}
	cmd.Flags().StringVar(&f.slug, "slug", "", "App slug; serves at /app/<slug>/ (lowercase letters, digits, single hyphens; no leading/trailing hyphen). Defaults to the directory name")
	cmd.Flags().BoolVar(&f.wait, "wait", false, "Wait until deployment is healthy")
	cmd.Flags().IntVar(&f.waitTimeout, "wait-timeout", 300, "Seconds to wait for healthy status when --wait is set (first-run dependency installs can take minutes)")
	cmd.Flags().StringVar(&f.git, "git", "", "Git repository URL to clone and deploy")
	cmd.Flags().StringVar(&f.branch, "branch", "", "Branch or tag to deploy (default: repo default)")
	cmd.Flags().StringVar(&f.subdir, "subdir", "", "Subdirectory within repo containing the app")
	cmd.Flags().StringVar(&f.visibility, "visibility", "", "App visibility for new apps: private, shared, or public (default: server config)")
	return cmd
}

func runDeploy(cmd *cobra.Command, args []string, f *deployFlags) error {
	var dir string

	if f.git != "" {
		cloned, err := gitClone(f.git, f.branch, f.subdir)
		if err != nil {
			return fmt.Errorf("git clone: %w", err)
		}
		defer os.RemoveAll(cloned)
		dir = cloned
	} else {
		// Require an explicit directory argument so a stray `shinyhub deploy`
		// from the wrong working directory cannot silently bundle whatever
		// happens to be in $PWD (e.g. /tmp, the project root with data/apps/,
		// $HOME). Pass `.` to opt in to the current directory.
		if len(args) == 0 {
			return fmt.Errorf("missing directory argument: pass `.` to deploy the current directory or a path like `./app`")
		}
		dir = args[0]
	}

	abs, err := filepath.Abs(dir)
	if err != nil {
		return err
	}

	slug := f.slug
	derived := false
	if slug == "" {
		derived = true
		if f.git != "" {
			// Derive slug from the repo name (last path component, strip .git suffix).
			repoName := filepath.Base(f.git)
			repoName = strings.TrimSuffix(repoName, ".git")
			slug = sanitizeSlug(repoName)
		} else {
			slug = sanitizeSlug(filepath.Base(abs))
		}
	}
	// Validate locally before any network call. The derived path matters
	// just as much as the user-supplied path: sanitizeSlug can collapse a
	// non-ASCII directory name (e.g. an emoji-only repo, "---", a single
	// `.`) to an empty or otherwise invalid string, which would otherwise
	// hit `/api/apps/` with a malformed URL and surface a confusing 404
	// instead of a clear local error.
	if !slugpkg.Valid(slug) {
		if derived {
			return fmt.Errorf("could not derive a valid slug from %q (got %q): pass --slug explicitly. Slug rule: %s",
				filepath.Base(abs), slug, slugpkg.HumanRule)
		}
		return fmt.Errorf("invalid slug %q: must be %s", slug, slugpkg.HumanRule)
	}

	cfg, err := loadConfig()
	if err != nil {
		return err
	}

	if f.visibility != "" && !db.IsValidAppVisibility(f.visibility) {
		return fmt.Errorf("invalid --visibility %q: must be one of %s",
			f.visibility, strings.Join(db.ValidAppVisibilities, ", "))
	}

	fmt.Printf("Bundling %s...\n", abs)
	bundleBuf, summary, err := zipDir(abs)
	if err != nil {
		return fmt.Errorf("bundle: %w", err)
	}
	if summary != "" {
		fmt.Fprintln(os.Stderr, summary)
	}

	if err := ensureApp(cfg, slug, f.visibility); err != nil {
		return err
	}

	fmt.Printf("Deploying %s to %s...\n", slug, cfg.Host)
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	part, _ := writer.CreateFormFile("bundle", "bundle.zip")
	io.Copy(part, bundleBuf)
	writer.Close()

	req, err := http.NewRequest("POST", cfg.Host+"/api/apps/"+slug+"/deploy", &body)
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Authorization", authHeader(cfg.Token))
	req.Header.Set("Content-Type", writer.FormDataContentType())

	// Deploy can take several minutes on first run (uv downloads packages).
	// Use http.DefaultClient (no timeout) to match the SSE logs command.
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("deploy request: %w", err)
	}
	defer resp.Body.Close()
	out, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 400 {
		return httpError(cfg.Token, "deploy", resp, out)
	}

	// Extract deployment number from the response so we can print a clean summary.
	var appResp map[string]any
	deployCount := 0
	if err := json.Unmarshal(out, &appResp); err == nil {
		if v, ok := appResp["deploy_count"].(float64); ok {
			deployCount = int(v)
		}
	}
	if deployCount > 0 {
		fmt.Printf("Deployed %s (deployment #%d)\nURL: %s/app/%s/\n", slug, deployCount, cfg.Host, slug)
	} else {
		fmt.Printf("Deployed %s\nURL: %s/app/%s/\n", slug, cfg.Host, slug)
	}
	for _, line := range formatManifestSummary(appResp["manifest"]) {
		fmt.Println(line)
	}
	if warn := formatHooksSkippedWarning(appResp["hooks_skipped"]); warn != "" {
		fmt.Fprintln(os.Stderr, warn)
	}

	if f.wait {
		if err := waitForHealthy(cfg, slug, time.Duration(f.waitTimeout)*time.Second); err != nil {
			return err
		}
	}
	return nil
}

// healthPollInterval is the delay between health polls. It is a package var so
// tests can shorten it; production keeps the 2-second cadence.
var healthPollInterval = 2 * time.Second

// waitForHealthy polls GET /api/apps/{slug} until status is "running" or
// the deadline expires. It writes progress dots to stdout and any failure
// log tail to os.Stderr.
func waitForHealthy(cfg *cliConfig, slug string, timeout time.Duration) error {
	return waitForHealthyWithOutput(cfg, slug, timeout, os.Stderr)
}

// waitForHealthyWithOutput is the testable core of waitForHealthy. It polls
// until the app is running, timed out, or enters a terminal failed state.
// On failure it fetches the last 20 log lines and writes them to errOut,
// followed by a hint pointing to the full logs command.
//
// A 4xx poll response (auth, gone, forbidden) is treated as fatal: continuing
// to poll would only delay the inevitable failure. 5xx and transport errors
// are treated as transient and keep the loop going.
func waitForHealthyWithOutput(cfg *cliConfig, slug string, timeout time.Duration, errOut io.Writer) error {
	deadline := time.Now().Add(timeout)
	fmt.Printf("Waiting for %s to be healthy", slug)
	var lastErr error
	var lastPollOK bool
	for time.Now().Before(deadline) {
		ready, status, err := pollAppStatus(cfg, slug)
		if err == nil && ready {
			fmt.Println(" ready.")
			return nil
		}
		lastPollOK = err == nil
		if err != nil {
			lastErr = err
			var he *httpStatusError
			if errors.As(err, &he) && he.fatal() {
				fmt.Println()
				return fmt.Errorf("checking %s: %w", slug, err)
			}
		}
		if isTerminalStatus(status) {
			fmt.Println()
			printLogTail(cfg, slug, errOut)
			return fmt.Errorf("%s %s during startup - check logs above or run: shinyhub apps logs %s", slug, status, slug)
		}
		fmt.Print(".")
		time.Sleep(healthPollInterval)
	}
	fmt.Println()
	printLogTail(cfg, slug, errOut)
	// If the most recent poll could not reach the app, surface that error: we
	// have no fresh evidence the app is merely still booting, and a persistent
	// transport/5xx failure is the actionable diagnostic. This also covers the
	// case where we never reached the app at all (lastStatus still empty).
	if !lastPollOK && lastErr != nil {
		return fmt.Errorf("timed out after %s waiting for %s to be healthy (last error: %v)", timeout, slug, lastErr)
	}
	// The app was reachable and still in a non-terminal startup state: the
	// deploy was committed and the app is still booting (first-run dependency
	// installs can outlast the wait window). Make clear this is not a failure.
	return fmt.Errorf("deploy committed, but %s is still starting after %s (timed out). "+
		"First-run dependency installs can take longer than this; the app has not failed. "+
		"Check progress with `shinyhub apps logs %s`, or re-run with a larger --wait-timeout", slug, timeout, slug)
}

// isTerminalStatus reports whether an app status indicates a non-recoverable
// failure during startup (as opposed to a transient state like "starting" or
// "stopped", which is a normal intentional stop). Only "crashed" is unambiguously
// a failed-startup state.
func isTerminalStatus(status string) bool {
	return status == "crashed"
}

// printLogTail fetches the last 20 lines of the app log via the SSE endpoint
// and writes them to w, followed by a hint for the full logs command.
// On fetch error or non-2xx response, a warning line is written to w so the
// caller always sees actionable output even when the log endpoint is unavailable.
func printLogTail(cfg *cliConfig, slug string, w io.Writer) {
	const tailLines = 20
	req, err := http.NewRequest("GET", cfg.Host+"/api/apps/"+slug+"/logs?follow=false", nil)
	if err != nil {
		fmt.Fprintf(w, "warning: could not fetch logs: %s\n", err)
		return
	}
	req.Header.Set("Authorization", authHeader(cfg.Token))
	resp, err := httpClient.Do(req)
	if err != nil {
		fmt.Fprintf(w, "warning: could not fetch logs: %s\n", err)
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		fmt.Fprintf(w, "warning: could not fetch logs: %s\n", resp.Status)
		return
	}
	lines := parseSSELines(resp.Body, tailLines)
	if len(lines) == 0 {
		return
	}
	fmt.Fprintln(w, "--- last log lines ---")
	for _, l := range lines {
		fmt.Fprintln(w, l)
	}
	fmt.Fprintf(w, "--- run `shinyhub apps logs %s` for full logs ---\n", slug)
}

// parseSSELines reads Server-Sent Events from r and returns the last n
// non-empty data lines. Comment lines (starting with ':') are ignored.
func parseSSELines(r io.Reader, n int) []string {
	var all []string
	scanner := bufio.NewScanner(r)
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "data: ") {
			all = append(all, strings.TrimPrefix(line, "data: "))
		}
	}
	if len(all) <= n {
		return all
	}
	return all[len(all)-n:]
}

// httpStatusError carries the response status code and body so callers can
// distinguish fatal (4xx) from transient (5xx) HTTP failures while still
// surfacing the server's error envelope to the user.
type httpStatusError struct {
	statusCode int
	body       string
}

func (e *httpStatusError) Error() string {
	if e.body != "" {
		return fmt.Sprintf("HTTP %d: %s", e.statusCode, strings.TrimSpace(e.body))
	}
	return fmt.Sprintf("HTTP %d", e.statusCode)
}

// fatal returns true for 4xx codes — auth, not-found, forbidden — which won't
// resolve themselves on retry. 5xx is treated as transient.
func (e *httpStatusError) fatal() bool {
	return e.statusCode >= 400 && e.statusCode < 500
}

// pollAppStatus issues a single GET /api/apps/{slug} and reports whether the
// app is running and the current status string. It exists as a separate
// function so each iteration's response body is closed before the next poll —
// `defer` inside the loop would keep bodies open for the lifetime of the
// command on long --wait-timeout values.
//
// A non-2xx response is returned as an *httpStatusError so the caller can
// distinguish "permanent" failures (401/403/404) from transient ones (5xx).
func pollAppStatus(cfg *cliConfig, slug string) (bool, string, error) {
	req, err := http.NewRequest("GET", cfg.Host+"/api/apps/"+slug, nil)
	if err != nil {
		return false, "", fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Authorization", authHeader(cfg.Token))
	resp, err := httpClient.Do(req)
	if err != nil {
		return false, "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return false, "", &httpStatusError{statusCode: resp.StatusCode, body: string(body)}
	}
	var result struct {
		App struct {
			Status string `json:"status"`
		} `json:"app"`
		// RedeployInFlight is set by the server while an async replica redeploy
		// is cycling the pool. The app row still reports "running" throughout,
		// so this flag is the only honest signal that the new pool is not yet
		// up. Treat the app as not-ready until it clears.
		RedeployInFlight bool `json:"redeploy_in_flight"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return false, "", err
	}
	status := result.App.Status
	return status == "running" && !result.RedeployInFlight, status, nil
}

// ensureApp checks whether the app exists and creates it if not. When visibility
// is non-empty (one of "private", "shared", "public") it is forwarded in the
// creation request body; an empty string lets the server apply its configured
// default.
//
// If the app already exists and visibility is non-empty, a warning is printed to
// stderr — the flag is ignored for existing apps and the user should use
// `shinyhub apps access set` instead.
func ensureApp(cfg *cliConfig, slug, visibility string) error {
	return ensureAppWithOutput(cfg, slug, visibility, os.Stderr)
}

// ensureAppWithOutput is the testable core of ensureApp. errOut receives any
// warnings emitted during the call.
func ensureAppWithOutput(cfg *cliConfig, slug, visibility string, errOut io.Writer) error {
	return ensureAppCore(cfg, slug, visibility, errOut, true)
}

// ensureAppCore is the shared implementation. warnExisting controls whether a
// non-empty visibility on an already-existing app produces the corrective
// warning. The interactive `deploy` path sets it; the fleet path clears it,
// because fleet reconciles visibility through its own config-drift mechanism
// and the deploy-layer warning would otherwise leak once per retry.
func ensureAppCore(cfg *cliConfig, slug, visibility string, errOut io.Writer, warnExisting bool) error {
	checkReq, err := http.NewRequest("GET", cfg.Host+"/api/apps/"+slug, nil)
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	checkReq.Header.Set("Authorization", authHeader(cfg.Token))
	resp, err := httpClient.Do(checkReq)
	if err != nil {
		return err
	}
	resp.Body.Close()
	if resp.StatusCode == 200 {
		if visibility != "" && warnExisting {
			fmt.Fprintf(errOut, "warning: --visibility is ignored for existing apps; use `shinyhub apps access set %s %s` instead\n", slug, visibility)
		}
		return nil
	}

	createBody := map[string]string{"slug": slug, "name": slug}
	if visibility != "" {
		createBody["access"] = visibility
	}
	bodyBytes, err := json.Marshal(createBody)
	if err != nil {
		return fmt.Errorf("encode create body: %w", err)
	}
	createReq, err := http.NewRequest("POST", cfg.Host+"/api/apps",
		bytes.NewReader(bodyBytes))
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	createReq.Header.Set("Authorization", authHeader(cfg.Token))
	createReq.Header.Set("Content-Type", "application/json")
	cr, err := httpClient.Do(createReq)
	if err != nil {
		return err
	}
	defer cr.Body.Close()
	if cr.StatusCode != 201 {
		raw, _ := io.ReadAll(cr.Body)
		// Surface the server's `{"error": "..."}` envelope so the user gets
		// enough context to diagnose quota / permission / validation failures;
		// a lapsed session is reported as a re-login hint instead.
		return httpError(cfg.Token, "create app "+slug, cr, raw)
	}
	return nil
}

// gitClone shallow-clones repoURL at the given branch into a temp directory
// and returns the path. The caller is responsible for removing the directory.
func gitClone(repoURL, branch, subdir string) (string, error) {
	dir, err := os.MkdirTemp("", "shiny-git-*")
	if err != nil {
		return "", fmt.Errorf("create temp dir: %w", err)
	}

	args := []string{"clone", "--depth=1"}
	if branch != "" {
		args = append(args, "--branch", branch)
	}
	args = append(args, repoURL, dir)

	cmd := exec.Command("git", args...)
	if out, err := cmd.CombinedOutput(); err != nil {
		os.RemoveAll(dir)
		return "", gitCmdError("git clone", err, out)
	}

	if subdir != "" {
		appDir := filepath.Join(dir, subdir)
		if _, err := os.Stat(appDir); err != nil {
			os.RemoveAll(dir) // dir still holds the root clone path
			return "", fmt.Errorf("subdir %q not found in repo", subdir)
		}
		dir = appDir
	}

	return dir, nil
}

func zipDir(dir string) (*bytes.Buffer, string, error) {
	var buf bytes.Buffer
	w := zip.NewWriter(&buf)
	rules := bundle.DefaultRules()
	rejected := map[bundle.FilterDecision][]string{}

	matcher, ignoreHasNegation, matcherErr := loadIgnoreMatcher(dir)
	if matcherErr != nil {
		return nil, "", fmt.Errorf("load ignore file: %w", matcherErr)
	}

	err := filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(dir, path)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		relSlash := filepath.ToSlash(rel)

		// Per-tree ignore filter runs before bundle.Rules so that ignore-file
		// rejections never show up in summarizeRejections (intentional excludes
		// are silent; only platform-policy rejections surface to the operator).
		if matcher != nil {
			query := relSlash
			if info.IsDir() {
				query = relSlash + "/"
			}
			if matcher.MatchesPath(query) {
				if info.IsDir() {
					// Only prune the subtree when no negation pattern could
					// re-include a descendant; otherwise descend and let
					// file-level matching handle each child.
					if !ignoreHasNegation {
						return filepath.SkipDir
					}
					return nil
				}
				return nil
			}
		}

		size := int64(0)
		if !info.IsDir() {
			size = info.Size()
		}
		decision := rules.Inspect(relSlash, size)
		switch decision {
		case bundle.FilterAccept:
			// fall through to include
		case bundle.FilterSkipCacheDir:
			if info.IsDir() {
				return filepath.SkipDir
			}
			return nil
		default:
			rejected[decision] = append(rejected[decision], relSlash)
			if info.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}

		if info.IsDir() {
			return nil
		}
		f, err := os.Open(path)
		if err != nil {
			return err
		}
		defer f.Close()
		h, err := zip.FileInfoHeader(info)
		if err != nil {
			return err
		}
		h.Name = relSlash
		h.Method = zip.Deflate
		zw, err := w.CreateHeader(h)
		if err != nil {
			return err
		}
		if _, err := io.Copy(zw, f); err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		return nil, "", err
	}
	if err := w.Close(); err != nil {
		return nil, "", err
	}
	return &buf, summarizeRejections(rejected), nil
}

// loadIgnoreMatcher returns a gitignore-style matcher built from the first
// of .shinyhubignore or .gitignore found in dir. Returns (nil, false, nil)
// when neither file exists. The ignoreHasNegation bool reports whether the
// source file contains any negation patterns (`!`-prefixed lines), which
// determines whether directory matches can safely `filepath.SkipDir`-prune
// their subtree. Non-ENOENT read errors are surfaced rather than silently
// swallowed.
func loadIgnoreMatcher(dir string) (*gitignore.GitIgnore, bool, error) {
	for _, name := range []string{".shinyhubignore", ".gitignore"} {
		p := filepath.Join(dir, name)
		raw, err := os.ReadFile(p)
		if err == nil {
			matcher := gitignore.CompileIgnoreLines(strings.Split(string(raw), "\n")...)
			return matcher, ignoreFileHasNegation(raw), nil
		} else if !errors.Is(err, os.ErrNotExist) {
			return nil, false, fmt.Errorf("read %s: %w", name, err)
		}
	}
	return nil, false, nil
}

// ignoreFileHasNegation reports whether the gitignore-format content has any
// negation line (a non-comment, non-blank line whose first non-space rune is
// `!`). Used to decide if directory pruning is safe: when no negation patterns
// exist, a directory match means no descendant can be re-included, so
// filepath.SkipDir is correct. When negation patterns are present, the walker
// must descend and apply per-file matching instead.
func ignoreFileHasNegation(raw []byte) bool {
	for _, line := range strings.Split(string(raw), "\n") {
		t := strings.TrimSpace(line)
		if t == "" || strings.HasPrefix(t, "#") {
			continue
		}
		if strings.HasPrefix(t, "!") {
			return true
		}
	}
	return false
}

func summarizeRejections(r map[bundle.FilterDecision][]string) string {
	if len(r) == 0 {
		return ""
	}
	var parts []string
	for d, paths := range r {
		if len(paths) == 0 {
			continue
		}
		parts = append(parts, fmt.Sprintf("%s: %s", d, strings.Join(paths, ", ")))
	}
	sort.Strings(parts)
	return "Skipped from bundle (push with `shinyhub data push`): " + strings.Join(parts, "; ")
}
