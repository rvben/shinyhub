//go:build integration

package integration_test

import (
	"bytes"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/rvben/shinyhub/internal/api"
	"github.com/rvben/shinyhub/internal/auth"
	"github.com/rvben/shinyhub/internal/config"
	"github.com/rvben/shinyhub/internal/db"
	"github.com/rvben/shinyhub/internal/process"
	"github.com/rvben/shinyhub/internal/proxy"
)

// TestDataPersistsAcrossDeploysAndIsClearedOnDelete exercises the contract
// that defines per-app persistent data: a file pushed to the data dir must
// survive a re-deploy, and recreating the app with the same slug must start
// from an empty data dir.
//
// The deploy step shells out to `uv sync`, so the test is skipped when uv
// is not in PATH.
func TestDataPersistsAcrossDeploysAndIsClearedOnDelete(t *testing.T) {
	if _, err := exec.LookPath("uv"); err != nil {
		t.Skip("uv not in PATH")
	}

	store, err := db.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
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
		Auth: config.AuthConfig{Secret: "test-secret"},
		Storage: config.StorageConfig{
			AppsDir:    t.TempDir(),
			AppDataDir: t.TempDir(),
		},
	}
	mgr := process.NewManager(cfg.Storage.AppsDir, process.NewNativeRuntime())
	mgr.SetAppDataRoot(cfg.Storage.AppDataDir)
	prx := proxy.New()
	srv := api.New(cfg, store, mgr, prx)
	ts := httptest.NewServer(srv.Router())
	defer ts.Close()
	t.Cleanup(func() { _ = mgr.Stop("demo") })

	client := &http.Client{Timeout: 180 * time.Second}
	token := loginAdmin(t, ts.URL)

	createApp(t, client, ts.URL, token, "demo", "Demo")
	deployBundle(t, client, ts.URL, token, "demo", map[string]string{
		"app.py":           helloShinyApp,
		"requirements.txt": "shiny\n",
	})

	const seedBody = "hello from seed\n"
	putData(t, client, ts.URL, token, "demo", "seed.txt", seedBody)

	files := listData(t, client, ts.URL, token, "demo")
	if got := findFile(files, "seed.txt"); got == nil {
		t.Fatalf("seed.txt missing after upload: %+v", files)
	} else if got.Size != int64(len(seedBody)) {
		t.Fatalf("seed.txt size = %d, want %d", got.Size, len(seedBody))
	}

	deployBundle(t, client, ts.URL, token, "demo", map[string]string{
		"app.py":           helloShinyApp + "# v2\n",
		"requirements.txt": "shiny\n",
	})

	files = listData(t, client, ts.URL, token, "demo")
	if findFile(files, "seed.txt") == nil {
		t.Fatalf("seed.txt did not survive re-deploy: %+v", files)
	}

	deleteApp(t, client, ts.URL, token, "demo")
	createApp(t, client, ts.URL, token, "demo", "Demo")

	files = listData(t, client, ts.URL, token, "demo")
	if len(files) != 0 {
		t.Fatalf("data dir should be empty after slug recycle, got %+v", files)
	}
}

const helloShinyApp = `from shiny import App, ui
app_ui = ui.page_fluid(ui.h1("Hello"))
def server(input, output, session): pass
app = App(app_ui, server)
`

type dataFile struct {
	Path       string `json:"path"`
	Size       int64  `json:"size"`
	ModifiedAt int64  `json:"modified_at"`
	SHA256     string `json:"sha256"`
}

func loginAdmin(t *testing.T, base string) string {
	t.Helper()
	body, _ := json.Marshal(map[string]string{"username": "admin", "password": "pass"})
	resp, err := http.Post(base+"/api/auth/login", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		dumpBody(t, "login", resp)
	}
	var out struct {
		Token string `json:"token"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	if out.Token == "" {
		t.Fatal("no token from login")
	}
	return out.Token
}

func createApp(t *testing.T, client *http.Client, base, token, slug, name string) {
	t.Helper()
	body, _ := json.Marshal(map[string]string{"slug": slug, "name": name})
	req, _ := http.NewRequest("POST", base+"/api/apps", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		dumpBody(t, "create app "+slug, resp)
	}
}

func deleteApp(t *testing.T, client *http.Client, base, token, slug string) {
	t.Helper()
	req, _ := http.NewRequest("DELETE", base+"/api/apps/"+slug, nil)
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent && resp.StatusCode != http.StatusOK {
		dumpBody(t, "delete app "+slug, resp)
	}
}

func deployBundle(t *testing.T, client *http.Client, base, token, slug string, files map[string]string) {
	t.Helper()
	zipPath := filepath.Join(t.TempDir(), "bundle.zip")
	createTestBundle(t, zipPath, files)
	zipBytes, err := os.ReadFile(zipPath)
	if err != nil {
		t.Fatal(err)
	}
	var body bytes.Buffer
	w := multipart.NewWriter(&body)
	part, err := w.CreateFormFile("bundle", "bundle.zip")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := part.Write(zipBytes); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	req, _ := http.NewRequest("POST", base+"/api/apps/"+slug+"/deploy", &body)
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", w.FormDataContentType())
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("deploy: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		dumpBody(t, "deploy "+slug, resp)
	}
}

func putData(t *testing.T, client *http.Client, base, token, slug, dest, body string) {
	t.Helper()
	req, _ := http.NewRequest("PUT", base+"/api/apps/"+slug+"/data/"+dest, bytes.NewReader([]byte(body)))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/octet-stream")
	req.ContentLength = int64(len(body))
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		dumpBody(t, "put data "+dest, resp)
	}
}

func listData(t *testing.T, client *http.Client, base, token, slug string) []dataFile {
	t.Helper()
	req, _ := http.NewRequest("GET", base+"/api/apps/"+slug+"/data", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		dumpBody(t, "list data "+slug, resp)
	}
	var env struct {
		Files []dataFile `json:"files"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&env); err != nil {
		t.Fatal(err)
	}
	return env.Files
}

func findFile(files []dataFile, path string) *dataFile {
	for i := range files {
		if files[i].Path == path {
			return &files[i]
		}
	}
	return nil
}

func dumpBody(t *testing.T, label string, resp *http.Response) {
	t.Helper()
	b, _ := io.ReadAll(resp.Body)
	t.Fatalf("%s: status %d: %s", label, resp.StatusCode, string(b))
}

// TestDeployRejectsBundleWithDataDir asserts that a bundle containing a
// `data/` first-segment is refused with 422 — the symmetric server-side
// guard that backstops the CLI/UI bundle-rules filter.
func TestDeployRejectsBundleWithDataDir(t *testing.T) {
	if _, err := exec.LookPath("uv"); err != nil {
		t.Skip("uv not in PATH")
	}

	store, err := db.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.Migrate(); err != nil {
		t.Fatal(err)
	}
	hash, _ := auth.HashPassword("pass")
	store.CreateUser(db.CreateUserParams{Username: "admin", PasswordHash: hash, Role: "admin"})

	cfg := &config.Config{
		Auth: config.AuthConfig{Secret: "test-secret"},
		Storage: config.StorageConfig{
			AppsDir:    t.TempDir(),
			AppDataDir: t.TempDir(),
		},
	}
	mgr := process.NewManager(cfg.Storage.AppsDir, process.NewNativeRuntime())
	mgr.SetAppDataRoot(cfg.Storage.AppDataDir)
	srv := api.New(cfg, store, mgr, proxy.New())
	ts := httptest.NewServer(srv.Router())
	defer ts.Close()

	client := &http.Client{Timeout: 30 * time.Second}
	token := loginAdmin(t, ts.URL)
	createApp(t, client, ts.URL, token, "rejectme", "Reject")

	zipPath := filepath.Join(t.TempDir(), "bad.zip")
	createTestBundle(t, zipPath, map[string]string{
		"app.py":           helloShinyApp,
		"requirements.txt": "shiny\n",
		"data/forbidden":   "should be rejected",
	})
	zipBytes, _ := os.ReadFile(zipPath)

	var body bytes.Buffer
	w := multipart.NewWriter(&body)
	part, _ := w.CreateFormFile("bundle", "bundle.zip")
	part.Write(zipBytes)
	w.Close()

	req, _ := http.NewRequest("POST", ts.URL+"/api/apps/rejectme/deploy", &body)
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", w.FormDataContentType())
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnprocessableEntity {
		dumpBody(t, "deploy with data/ should be 422", resp)
	}
}

