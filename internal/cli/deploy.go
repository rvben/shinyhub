package cli

import (
	"archive/zip"
	"bytes"
	"encoding/json"
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
	"github.com/spf13/cobra"
)

var slugInvalidRE = regexp.MustCompile(`[^a-z0-9]+`)
// validSlugRE matches slugs that start with an alphanumeric character, end
// with an alphanumeric character, and contain only lowercase letters, digits,
// and hyphens — matching the server's constraint.
var validSlugRE = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)

// sanitizeSlug lowercases the name, replaces runs of non-alphanumeric characters
// with a single dash, and trims leading/trailing dashes to produce a slug that
// matches the server's ^[a-z0-9][a-z0-9-]{0,62}$ constraint.
func sanitizeSlug(name string) string {
	s := strings.ToLower(name)
	s = slugInvalidRE.ReplaceAllString(s, "-")
	s = strings.Trim(s, "-")
	if len(s) > 63 {
		s = s[:63]
	}
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
		dir = "."
		if len(args) > 0 {
			dir = args[0]
		}
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
		if !validSlugRE.MatchString(slug) {
			return fmt.Errorf("invalid slug %q: must match [a-z0-9][a-z0-9-]{0,62} (lowercase letters, digits, and hyphens only)", slug)
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
// the deadline expires. It prints progress dots to stdout.
func waitForHealthy(cfg *cliConfig, slug string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	interval := 2 * time.Second
	fmt.Printf("Waiting for %s to be healthy", slug)
	for time.Now().Before(deadline) {
		req, err := http.NewRequest("GET", cfg.Host+"/api/apps/"+slug, nil)
		if err != nil {
			return fmt.Errorf("build request: %w", err)
		}
		req.Header.Set("Authorization", authHeader(cfg.Token))
		resp, err := httpClient.Do(req)
		if err == nil {
			defer resp.Body.Close()
			var result struct {
				App struct {
					Status string `json:"status"`
				} `json:"app"`
			}
			if err := json.NewDecoder(resp.Body).Decode(&result); err == nil {
				if result.App.Status == "running" {
					fmt.Println(" ready.")
					return nil
				}
			}
		}
		fmt.Print(".")
		time.Sleep(interval)
	}
	fmt.Println()
	return fmt.Errorf("timed out after %s waiting for %s to be healthy", timeout, slug)
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
