package cli

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestDeploy_JSONModeKeepsStdoutClean drives deploy against an httptest server
// and asserts that stdout contains exactly one JSON object (the result
// envelope) while progress text goes to stderr.
func TestDeploy_JSONModeKeepsStdoutClean(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/apps/demo", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"app":{"slug":"demo","status":"running","deploy_count":3,"current_version":"v3"}}`))
	})
	mux.HandleFunc("/api/apps/demo/deploy", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"slug":"demo","status":"running","deploy_count":3,"current_version":"v3"}`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "app.py"), []byte("# shiny\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	writeTestCLIConfig(t, srv.URL)

	stdout, stderr, err := execCLISplit(t, "deploy", dir, "--slug", "demo")
	if err != nil {
		t.Fatalf("deploy failed: %v (stdout=%q stderr=%q)", err, stdout, stderr)
	}

	// Stdout must be a single JSON object with a status field.
	var obj map[string]any
	trimmed := strings.TrimSpace(stdout)
	if err := json.Unmarshal([]byte(trimmed), &obj); err != nil {
		t.Fatalf("stdout is not a JSON object: %q: %v", stdout, err)
	}
	if obj["status"] != "deployed" {
		t.Errorf("stdout status = %q, want %q; full stdout: %q", obj["status"], "deployed", stdout)
	}
	if obj["slug"] != "demo" {
		t.Errorf("stdout slug = %v, want %q", obj["slug"], "demo")
	}

	// Progress text must go to stderr.
	if !strings.Contains(stderr, "Bundling") && !strings.Contains(stderr, "Deploying") {
		t.Errorf("progress should appear on stderr; stderr=%q stdout=%q", stderr, stdout)
	}
}

// TestDeploy_TableModeKeepsProseOnStdout verifies that in table mode (explicit
// -o table) the human-readable prose appears on stdout as before.
func TestDeploy_TableModeKeepsProseOnStdout(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/apps/demo", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"app":{"slug":"demo","status":"running","deploy_count":2,"current_version":"v2"}}`))
	})
	mux.HandleFunc("/api/apps/demo/deploy", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"slug":"demo","status":"running","deploy_count":2,"current_version":"v2"}`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "app.py"), []byte("# shiny\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	writeTestCLIConfig(t, srv.URL)

	stdout, _, err := execCLISplit(t, "deploy", dir, "--slug", "demo", "-o", "table")
	if err != nil {
		t.Fatalf("deploy failed: %v", err)
	}

	if !strings.Contains(stdout, "Deployed") {
		t.Errorf("table mode should print Deployed prose on stdout; got %q", stdout)
	}
}
