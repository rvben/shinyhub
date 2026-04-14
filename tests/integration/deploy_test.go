//go:build integration

package integration_test

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"fmt"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"testing"
	"time"

	"github.com/rvben/shinyhost/internal/api"
	"github.com/rvben/shinyhost/internal/auth"
	"github.com/rvben/shinyhost/internal/config"
	"github.com/rvben/shinyhost/internal/db"
	"github.com/rvben/shinyhost/internal/process"
	"github.com/rvben/shinyhost/internal/proxy"
)

// createTestBundle writes a zip file at zipPath containing the given files map.
func createTestBundle(t *testing.T, zipPath string, files map[string]string) {
	t.Helper()
	f, err := os.Create(zipPath)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	w := zip.NewWriter(f)
	for name, content := range files {
		fw, err := w.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := fw.Write([]byte(content)); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestFullDeployCycle(t *testing.T) {
	if _, err := exec.LookPath("uv"); err != nil {
		t.Skip("uv not in PATH")
	}

	store, err := db.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Migrate(); err != nil {
		t.Fatal(err)
	}
	hash, err := auth.HashPassword("pass")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.CreateUser(db.CreateUserParams{
		Username:     "admin",
		PasswordHash: hash,
		Role:         "admin",
	}); err != nil {
		t.Fatal(err)
	}

	cfg := &config.Config{
		Auth:    config.AuthConfig{Secret: "test-secret"},
		Storage: config.StorageConfig{AppsDir: t.TempDir()},
	}
	mgr := process.NewManager()
	prx := proxy.New()
	srv := api.New(cfg, store, mgr, prx)
	ts := httptest.NewServer(srv.Router())
	defer ts.Close()

	// 1. Login
	loginBody, _ := json.Marshal(map[string]string{"username": "admin", "password": "pass"})
	lr, err := http.Post(ts.URL+"/api/auth/login", "application/json", bytes.NewReader(loginBody))
	if err != nil {
		t.Fatal(err)
	}
	var loginResp map[string]string
	if err := json.NewDecoder(lr.Body).Decode(&loginResp); err != nil {
		t.Fatal(err)
	}
	token := loginResp["token"]
	if token == "" {
		t.Fatal("no token from login")
	}

	// 2. Create app
	appBody, _ := json.Marshal(map[string]string{"slug": "hello", "name": "Hello"})
	req, _ := http.NewRequest("POST", ts.URL+"/api/apps", bytes.NewReader(appBody))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	cr, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if cr.StatusCode != 201 {
		t.Fatalf("expected 201, got %d", cr.StatusCode)
	}

	// 3. Build a minimal hello-world shiny bundle
	bundleDir := t.TempDir()
	zipPath := bundleDir + "/app.zip"
	createTestBundle(t, zipPath, map[string]string{
		"app.py": `from shiny import App, ui, render
app_ui = ui.page_fluid(ui.h1("Hello"))
def server(input, output, session): pass
app = App(app_ui, server)
`,
		"requirements.txt": "shiny\n",
	})
	zipBytes, err := os.ReadFile(zipPath)
	if err != nil {
		t.Fatal(err)
	}

	// 4. Deploy via multipart
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	part, err := writer.CreateFormFile("bundle", "bundle.zip")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := part.Write(zipBytes); err != nil {
		t.Fatal(err)
	}
	writer.Close()

	dreq, err := http.NewRequest("POST", ts.URL+"/api/apps/hello/deploy", &body)
	if err != nil {
		t.Fatal(err)
	}
	dreq.Header.Set("Authorization", "Bearer "+token)
	dreq.Header.Set("Content-Type", writer.FormDataContentType())

	// Allow longer timeout for uv sync on first run
	client := &http.Client{Timeout: 120 * time.Second}
	dr, err := client.Do(dreq)
	if err != nil {
		t.Fatalf("deploy request: %v", err)
	}
	if dr.StatusCode != 200 {
		var out bytes.Buffer
		out.ReadFrom(dr.Body)
		t.Fatalf("deploy failed (%d): %s", dr.StatusCode, out.String())
	}
	fmt.Println("Deploy succeeded")

	// 5. Check app list
	listReq, _ := http.NewRequest("GET", ts.URL+"/api/apps", nil)
	listReq.Header.Set("Authorization", "Bearer "+token)
	listResp, err := http.DefaultClient.Do(listReq)
	if err != nil {
		t.Fatal(err)
	}
	var apps []map[string]any
	if err := json.NewDecoder(listResp.Body).Decode(&apps); err != nil {
		t.Fatal(err)
	}
	if len(apps) == 0 {
		t.Error("expected at least one app")
	}
}
