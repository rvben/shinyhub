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

	req, _ := http.NewRequest("POST", cfg.Host+"/api/apps/"+slug+"/deploy", &body)
	req.Header.Set("Authorization", "Bearer "+cfg.Token)
	req.Header.Set("Content-Type", writer.FormDataContentType())

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("deploy request: %w", err)
	}
	defer resp.Body.Close()
	out, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 {
		return fmt.Errorf("deploy failed (%s): %s", resp.Status, out)
	}
	fmt.Printf("Deployed: %s\n", out)
	return nil
}

func ensureApp(cfg *cliConfig, slug string) error {
	checkReq, _ := http.NewRequest("GET", cfg.Host+"/api/apps/"+slug, nil)
	checkReq.Header.Set("Authorization", "Bearer "+cfg.Token)
	resp, err := http.DefaultClient.Do(checkReq)
	if err != nil {
		return err
	}
	resp.Body.Close()
	if resp.StatusCode == 200 {
		return nil
	}

	body := fmt.Sprintf(`{"slug":%q,"name":%q}`, slug, slug)
	createReq, _ := http.NewRequest("POST", cfg.Host+"/api/apps",
		bytes.NewBufferString(body))
	createReq.Header.Set("Authorization", "Bearer "+cfg.Token)
	createReq.Header.Set("Content-Type", "application/json")
	cr, err := http.DefaultClient.Do(createReq)
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
			return nil
		}
		rel, _ := filepath.Rel(dir, path)
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
