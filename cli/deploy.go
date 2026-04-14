package cli

import (
	"archive/zip"
	"bytes"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"
)

var deployCmd = &cobra.Command{
	Use:   "deploy [dir]",
	Short: "Deploy a Shiny app to ShinyHost",
	Args:  cobra.MaximumNArgs(1),
	RunE:  runDeploy,
}

var deployFlags struct {
	slug string
	wait bool
}

func init() {
	deployCmd.Flags().StringVar(&deployFlags.slug, "slug", "", "App slug (defaults to directory name)")
	deployCmd.Flags().BoolVar(&deployFlags.wait, "wait", false, "Wait until deployment is healthy")
}

func runDeploy(cmd *cobra.Command, args []string) error {
	dir := "."
	if len(args) > 0 {
		dir = args[0]
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return err
	}

	slug := deployFlags.slug
	if slug == "" {
		slug = filepath.Base(abs)
	}

	cfg, err := loadConfig()
	if err != nil {
		return err
	}

	fmt.Printf("Bundling %s...\n", abs)
	bundle, err := zipDir(abs)
	if err != nil {
		return fmt.Errorf("bundle: %w", err)
	}

	if err := ensureApp(cfg, slug); err != nil {
		return err
	}

	fmt.Printf("Deploying %s to %s...\n", slug, cfg.Host)
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	part, _ := writer.CreateFormFile("bundle", "bundle.zip")
	io.Copy(part, bundle)
	writer.Close()

	req, err := http.NewRequest("POST", cfg.Host+"/api/apps/"+slug+"/deploy", &body)
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+cfg.Token)
	req.Header.Set("Content-Type", writer.FormDataContentType())

	resp, err := httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("deploy request: %w", err)
	}
	defer resp.Body.Close()
	out, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 400 {
		return fmt.Errorf("deploy failed (%s): %s", resp.Status, out)
	}
	fmt.Printf("Deployed: %s\n", out)
	return nil
}

func ensureApp(cfg *cliConfig, slug string) error {
	checkReq, err := http.NewRequest("GET", cfg.Host+"/api/apps/"+slug, nil)
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	checkReq.Header.Set("Authorization", "Bearer "+cfg.Token)
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
	createReq.Header.Set("Authorization", "Bearer "+cfg.Token)
	createReq.Header.Set("Content-Type", "application/json")
	cr, err := httpClient.Do(createReq)
	if err != nil {
		return err
	}
	cr.Body.Close()
	if cr.StatusCode != 201 {
		return fmt.Errorf("could not create app %s", slug)
	}
	return nil
}

func zipDir(dir string) (*bytes.Buffer, error) {
	var buf bytes.Buffer
	w := zip.NewWriter(&buf)
	err := filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			switch info.Name() {
			case ".git", ".venv", "__pycache__", "node_modules", ".renv", ".Rproj.user":
				return filepath.SkipDir
			}
			return nil
		}
		rel, err := filepath.Rel(dir, path)
		if err != nil {
			return err
		}
		fw, err := w.Create(rel)
		if err != nil {
			return err
		}
		f, err := os.Open(path)
		if err != nil {
			return err
		}
		defer f.Close()
		_, err = io.Copy(fw, f)
		return err
	})
	if err != nil {
		return nil, err
	}
	return &buf, w.Close()
}
