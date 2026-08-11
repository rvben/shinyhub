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

// deployStubServer serves the two endpoints a deploy touches. deployBody is the
// raw JSON the deploy endpoint returns; record, when non-nil, captures the
// deploy request's raw query string.
func deployStubServer(t *testing.T, deployBody string, record *string) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/api/apps/demo", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"app":{"slug":"demo","status":"stopped","deploy_count":3,"current_version":"v3"}}`))
	})
	mux.HandleFunc("/api/apps/demo/deploy", func(w http.ResponseWriter, r *http.Request) {
		if record != nil {
			*record = r.URL.RawQuery
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(deployBody))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

// deployTestBundleDir writes a minimal deployable bundle.
func deployTestBundleDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "app.py"), []byte("# shiny\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

// A deploy that left the app stopped must say so and name the command that
// brings it up. Reporting a bare "Deployed" would leave the developer waiting
// on a URL that serves a stopped page.
func TestDeploy_KeptStoppedIsReported(t *testing.T) {
	srv := deployStubServer(t,
		`{"slug":"demo","status":"stopped","deploy_count":4,"current_version":"v4","kept_stopped":true}`, nil)
	writeTestCLIConfig(t, srv.URL)

	stdout, _, err := execCLISplit(t, "deploy", deployTestBundleDir(t), "--slug", "demo", "-o", "table")
	if err != nil {
		t.Fatalf("deploy failed: %v", err)
	}
	if !strings.Contains(stdout, "This app is stopped") {
		t.Errorf("stopped deploy should say the app is stopped; got %q", stdout)
	}
	if !strings.Contains(stdout, "shinyhub apps start demo") {
		t.Errorf("stopped deploy should name the start command; got %q", stdout)
	}
}

// The negative control: a normal deploy must not carry the stopped note.
func TestDeploy_LiveDeployHasNoStoppedNote(t *testing.T) {
	srv := deployStubServer(t,
		`{"slug":"demo","status":"running","deploy_count":4,"current_version":"v4"}`, nil)
	writeTestCLIConfig(t, srv.URL)

	stdout, _, err := execCLISplit(t, "deploy", deployTestBundleDir(t), "--slug", "demo", "-o", "table")
	if err != nil {
		t.Fatalf("deploy failed: %v", err)
	}
	if strings.Contains(stdout, "This app is stopped") {
		t.Errorf("a running deploy must not claim the app is stopped; got %q", stdout)
	}
}

// --wait on a kept-stopped deploy must not poll for a health it was told will
// never arrive: the app is down on purpose, so the poll could only time out and
// fail a deploy that succeeded. The stub serves status "stopped", so a poll
// would run to the deadline.
func TestDeploy_KeptStoppedSkipsWait(t *testing.T) {
	srv := deployStubServer(t,
		`{"slug":"demo","status":"stopped","deploy_count":4,"current_version":"v4","kept_stopped":true}`, nil)
	writeTestCLIConfig(t, srv.URL)

	stdout, stderr, err := execCLISplit(t, "deploy", deployTestBundleDir(t), "--slug", "demo",
		"--wait", "--wait-timeout", "1", "-o", "table")
	if err != nil {
		t.Fatalf("deploy failed: %v (stdout=%q stderr=%q)", err, stdout, stderr)
	}
	if strings.Contains(stderr, "Waiting") {
		t.Errorf("--wait should be skipped for a kept-stopped deploy; stderr=%q", stderr)
	}
}

// --start is the explicit override, and it reaches the server as ?start=true.
func TestDeploy_StartFlagSetsQuery(t *testing.T) {
	var query string
	srv := deployStubServer(t,
		`{"slug":"demo","status":"running","deploy_count":4,"current_version":"v4"}`, &query)
	writeTestCLIConfig(t, srv.URL)

	if _, _, err := execCLISplit(t, "deploy", deployTestBundleDir(t), "--slug", "demo", "--start", "-o", "table"); err != nil {
		t.Fatalf("deploy failed: %v", err)
	}
	if query != "start=true" {
		t.Errorf("deploy query = %q, want %q", query, "start=true")
	}
}

// Without --start the CLI sends no query at all, so the server applies the
// stopped-stays-stopped default.
func TestDeploy_WithoutStartFlagSendsNoQuery(t *testing.T) {
	var query string
	srv := deployStubServer(t,
		`{"slug":"demo","status":"running","deploy_count":4,"current_version":"v4"}`, &query)
	writeTestCLIConfig(t, srv.URL)

	if _, _, err := execCLISplit(t, "deploy", deployTestBundleDir(t), "--slug", "demo", "-o", "table"); err != nil {
		t.Fatalf("deploy failed: %v", err)
	}
	if query != "" {
		t.Errorf("deploy query = %q, want empty", query)
	}
}

// The JSON envelope carries the flag so a pipeline can branch on it instead of
// matching prose.
func TestDeploy_JSONEnvelopeCarriesKeptStopped(t *testing.T) {
	srv := deployStubServer(t,
		`{"slug":"demo","status":"stopped","deploy_count":4,"current_version":"v4","kept_stopped":true}`, nil)
	writeTestCLIConfig(t, srv.URL)

	stdout, _, err := execCLISplit(t, "deploy", deployTestBundleDir(t), "--slug", "demo")
	if err != nil {
		t.Fatalf("deploy failed: %v", err)
	}
	var obj map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSpace(stdout)), &obj); err != nil {
		t.Fatalf("stdout is not a JSON object: %q: %v", stdout, err)
	}
	if obj["kept_stopped"] != true {
		t.Errorf("kept_stopped = %v, want true; full stdout: %q", obj["kept_stopped"], stdout)
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
