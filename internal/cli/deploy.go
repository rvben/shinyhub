package cli

import (
	"archive/zip"
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

	"github.com/rvben/shinyhub/internal/bundle"
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

var deployCmd = &cobra.Command{
	Use:   "deploy [dir]",
	Short: "Deploy a Shiny app to ShinyHub",
	Args:  cobra.MaximumNArgs(1),
	RunE:  runDeploy,
}

var deployFlags struct {
	slug        string
	wait        bool
	waitTimeout int // seconds
	git         string // git repo URL; if set, clone instead of using local dir
	branch      string // branch/tag to check out (default: default branch)
	subdir      string // subdirectory within the repo containing the app
}

func init() {
	deployCmd.Flags().StringVar(&deployFlags.slug, "slug", "", "App slug (defaults to directory name)")
	deployCmd.Flags().BoolVar(&deployFlags.wait, "wait", false, "Wait until deployment is healthy")
	deployCmd.Flags().IntVar(&deployFlags.waitTimeout, "wait-timeout", 60, "Seconds to wait for healthy status when --wait is set")
	deployCmd.Flags().StringVar(&deployFlags.git, "git", "", "Git repository URL to clone and deploy")
	deployCmd.Flags().StringVar(&deployFlags.branch, "branch", "", "Branch or tag to deploy (default: repo default)")
	deployCmd.Flags().StringVar(&deployFlags.subdir, "subdir", "", "Subdirectory within repo containing the app")
}

func runDeploy(cmd *cobra.Command, args []string) error {
	var dir string

	if deployFlags.git != "" {
		cloned, err := gitClone(deployFlags.git, deployFlags.branch, deployFlags.subdir)
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

	slug := deployFlags.slug
	if slug == "" {
		if deployFlags.git != "" {
			// Derive slug from the repo name (last path component, strip .git suffix).
			repoName := filepath.Base(deployFlags.git)
			repoName = strings.TrimSuffix(repoName, ".git")
			slug = sanitizeSlug(repoName)
		} else {
			slug = sanitizeSlug(filepath.Base(abs))
		}
	} else {
		// Validate the user-supplied slug locally before making any network call.
		if !slugpkg.Valid(slug) {
			return fmt.Errorf("invalid slug %q: must be %s", slug, slugpkg.HumanRule)
		}
	}

	cfg, err := loadConfig()
	if err != nil {
		return err
	}

	fmt.Printf("Bundling %s...\n", abs)
	bundleBuf, summary, err := zipDir(abs)
	if err != nil {
		return fmt.Errorf("bundle: %w", err)
	}
	if summary != "" {
		fmt.Fprintln(os.Stderr, summary)
	}

	if err := ensureApp(cfg, slug); err != nil {
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
		return fmt.Errorf("deploy failed (%s): %s", resp.Status, out)
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

	if deployFlags.wait {
		if err := waitForHealthy(cfg, slug, time.Duration(deployFlags.waitTimeout)*time.Second); err != nil {
			return err
		}
	}
	return nil
}

// waitForHealthy polls GET /api/apps/{slug} until status is "running" or
// the deadline expires. It prints progress dots to stdout. A 4xx response
// (auth, gone, forbidden) is treated as fatal: continuing to poll would only
// delay the inevitable failure and waste the user's --wait-timeout. 5xx and
// transport errors are treated as transient and keep the loop going.
func waitForHealthy(cfg *cliConfig, slug string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	interval := 2 * time.Second
	fmt.Printf("Waiting for %s to be healthy", slug)
	var lastErr error
	for time.Now().Before(deadline) {
		ready, err := pollAppStatus(cfg, slug)
		if err == nil && ready {
			fmt.Println(" ready.")
			return nil
		}
		if err != nil {
			lastErr = err
			var he *httpStatusError
			if errors.As(err, &he) && he.fatal() {
				fmt.Println()
				return fmt.Errorf("checking %s: %w", slug, err)
			}
		}
		fmt.Print(".")
		time.Sleep(interval)
	}
	fmt.Println()
	if lastErr != nil {
		return fmt.Errorf("timed out after %s waiting for %s to be healthy (last error: %v)", timeout, slug, lastErr)
	}
	return fmt.Errorf("timed out after %s waiting for %s to be healthy", timeout, slug)
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
// app is running. It exists as a separate function so each iteration's
// response body is closed before the next poll — `defer` inside the loop in
// waitForHealthy used to leak file descriptors for the lifetime of the
// command on long --wait-timeout values.
//
// A non-2xx response is returned as an *httpStatusError so the caller can
// distinguish "permanent" failures (401/403/404) from transient ones (5xx).
// Without this, every error silently degraded to "not running" and the user
// waited the full --wait-timeout for an outcome that was already decided.
func pollAppStatus(cfg *cliConfig, slug string) (bool, error) {
	req, err := http.NewRequest("GET", cfg.Host+"/api/apps/"+slug, nil)
	if err != nil {
		return false, fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Authorization", authHeader(cfg.Token))
	resp, err := httpClient.Do(req)
	if err != nil {
		return false, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return false, &httpStatusError{statusCode: resp.StatusCode, body: string(body)}
	}
	var result struct {
		App struct {
			Status string `json:"status"`
		} `json:"app"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return false, err
	}
	return result.App.Status == "running", nil
}

func ensureApp(cfg *cliConfig, slug string) error {
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
		return nil
	}

	body := fmt.Sprintf(`{"slug":%q,"name":%q}`, slug, slug)
	createReq, err := http.NewRequest("POST", cfg.Host+"/api/apps",
		bytes.NewBufferString(body))
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
		// Try to surface the server's `{"error": "..."}` envelope, falling
		// back to the raw body so the user gets enough context to diagnose
		// quota / permission / validation failures.
		msg := strings.TrimSpace(string(raw))
		var env struct{ Error string `json:"error"` }
		if err := json.Unmarshal(raw, &env); err == nil && env.Error != "" {
			msg = env.Error
		}
		if msg == "" {
			msg = cr.Status
		}
		return fmt.Errorf("could not create app %s: %s", slug, msg)
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
		return "", fmt.Errorf("git clone: %w\n%s", err, out)
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
