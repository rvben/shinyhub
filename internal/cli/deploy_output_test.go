package cli

import (
	"encoding/json"
	"errors"
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
	var deployChannel string
	mux := http.NewServeMux()
	mux.HandleFunc("/api/apps/demo", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"app":{"slug":"demo","status":"running","deploy_count":3,"current_version":"v3"}}`))
	})
	mux.HandleFunc("/api/apps/demo/deploy", func(w http.ResponseWriter, r *http.Request) {
		deployChannel = r.Header.Get("X-Shinyhub-Deploy-Channel")
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
	if obj["url"] != srv.URL+"/app/demo/" || obj["opened"] != false {
		t.Errorf("stdout should always carry canonical URL and browser state; got %#v", obj)
	}
	if deployChannel != "cli" {
		t.Errorf("deploy channel = %q, want cli", deployChannel)
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

func TestDeploy_PostCommitWarningReachesHumanAndJSONOutput(t *testing.T) {
	const warning = "deployment committed; schedule convergence will repair asynchronously"
	for _, tc := range []struct {
		name   string
		format string
	}{
		{name: "human", format: "table"},
		{name: "json", format: "json"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := deployStubServer(t,
				`{"slug":"demo","status":"running","deploy_count":4,"current_version":"v4","warning":"`+warning+`"}`, nil)
			writeTestCLIConfig(t, srv.URL)
			stdout, stderr, err := execCLISplit(t, "deploy", deployTestBundleDir(t), "--slug", "demo", "-o", tc.format)
			if err != nil {
				t.Fatalf("deploy: %v stdout=%q stderr=%q", err, stdout, stderr)
			}
			if !strings.Contains(stderr, warning) {
				t.Fatalf("stderr=%q, want warning", stderr)
			}
			if tc.format == "json" {
				var result map[string]any
				if err := json.Unmarshal([]byte(stdout), &result); err != nil {
					t.Fatal(err)
				}
				if result["warning"] != warning {
					t.Fatalf("json warning=%v", result["warning"])
				}
			}
		})
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

func TestDeploy_OpenStartsWaitsVerifiesAndLaunches(t *testing.T) {
	var deployQuery string
	var routeHits int
	mux := http.NewServeMux()
	mux.HandleFunc("/api/apps/demo", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"app":{"slug":"demo","status":"running","access":"public","deploy_count":4}}`))
	})
	mux.HandleFunc("/api/apps/demo/deploy", func(w http.ResponseWriter, r *http.Request) {
		deployQuery = r.URL.RawQuery
		_, _ = w.Write([]byte(`{"slug":"demo","status":"running","access":"public","deploy_count":5,"current_version":"v5"}`))
	})
	mux.HandleFunc("/app/demo/", func(w http.ResponseWriter, r *http.Request) {
		routeHits++
		if got := r.Header.Get("Authorization"); got != "" {
			t.Errorf("route check leaked CLI auth header: %q", got)
		}
		_, _ = w.Write([]byte("ready"))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	writeTestCLIConfig(t, srv.URL)
	var opened string
	stubBrowser(t, func(target string) error { opened = target; return nil })

	stdout, stderr, err := execCLISplit(t, "deploy", deployTestBundleDir(t), "--slug", "demo", "--open", "-o", "json")
	if err != nil {
		t.Fatalf("deploy --open: %v (stdout=%q stderr=%q)", err, stdout, stderr)
	}
	wantURL := srv.URL + "/app/demo/"
	if deployQuery != "start=true" {
		t.Errorf("deploy query = %q, want start=true", deployQuery)
	}
	if routeHits != 1 || opened != wantURL {
		t.Errorf("route hits = %d, opened = %q, want verified and opened %q", routeHits, opened, wantURL)
	}
	result := decodeOpenResult(t, stdout)
	if result["status"] != "deployed" || result["url"] != wantURL || result["opened"] != true || result["kept_stopped"] != false {
		t.Errorf("result = %#v", result)
	}
	if !strings.Contains(stderr, "Waiting for demo to be healthy ready") || !strings.Contains(stderr, "Opened in your browser") {
		t.Errorf("stderr should narrate readiness and browser success: %q", stderr)
	}
}

func TestDeploy_OpenBrowserFailureKeepsSuccessfulDeploy(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/apps/demo", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"app":{"slug":"demo","status":"running","access":"private","deploy_count":1}}`))
	})
	mux.HandleFunc("/api/apps/demo/deploy", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"slug":"demo","status":"running","access":"private","deploy_count":2}`))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	writeTestCLIConfig(t, srv.URL)
	stubBrowser(t, func(string) error { return errors.New("browser unavailable") })

	stdout, stderr, err := execCLISplit(t, "deploy", deployTestBundleDir(t), "--slug", "demo", "--open", "-o", "json")
	if err != nil {
		t.Fatalf("browser failure should not undo a successful deploy: %v", err)
	}
	if result := decodeOpenResult(t, stdout); result["opened"] != false {
		t.Errorf("opened = %v, want false", result["opened"])
	}
	if !strings.Contains(stderr, "browser unavailable") || !strings.Contains(stderr, srv.URL+"/app/demo/") {
		t.Errorf("fallback should include cause and copyable URL: %q", stderr)
	}
}

func TestDeploy_OpenRecoversWhenServerKeepsAppStopped(t *testing.T) {
	var restartHits int
	mux := http.NewServeMux()
	mux.HandleFunc("/api/apps/demo", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"app":{"slug":"demo","status":"running","access":"private","deploy_count":2}}`))
	})
	mux.HandleFunc("/api/apps/demo/deploy", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"slug":"demo","status":"stopped","access":"private","deploy_count":3,"kept_stopped":true}`))
	})
	mux.HandleFunc("/api/apps/demo/restart", func(w http.ResponseWriter, r *http.Request) {
		restartHits++
		if r.URL.Query().Get("if_not_running") != "true" {
			t.Errorf("restart query = %q", r.URL.RawQuery)
		}
		_, _ = w.Write([]byte(`{"status":"running"}`))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	writeTestCLIConfig(t, srv.URL)
	stubBrowser(t, func(string) error { return nil })

	stdout, stderr, err := execCLISplit(t, "deploy", deployTestBundleDir(t), "--slug", "demo", "--open", "-o", "json")
	if err != nil {
		t.Fatalf("deploy --open recovery: %v", err)
	}
	if restartHits != 1 || !strings.Contains(stderr, "starting it now") {
		t.Errorf("restart hits = %d, stderr = %q", restartHits, stderr)
	}
	if result := decodeOpenResult(t, stdout); result["kept_stopped"] != false || result["opened"] != true {
		t.Errorf("final result should reflect recovered live state: %#v", result)
	}
}

func TestDeploy_OpenPublicRouteFailureExplainsDeploySucceeded(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/apps/demo", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"app":{"slug":"demo","status":"running","access":"public","deploy_count":1}}`))
	})
	mux.HandleFunc("/api/apps/demo/deploy", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"slug":"demo","status":"running","access":"public","deploy_count":2}`))
	})
	mux.HandleFunc("/app/demo/", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	writeTestCLIConfig(t, srv.URL)
	stubBrowser(t, func(string) error { t.Fatal("browser launched despite failed route check"); return nil })

	_, _, err := execCLISplit(t, "deploy", deployTestBundleDir(t), "--slug", "demo", "--open", "-o", "json")
	if err == nil || !strings.Contains(err.Error(), "deployed and became healthy") {
		t.Fatalf("error = %v, want successful-deploy distinction", err)
	}
	if !strings.Contains(hintOf(err), "deployment succeeded") || !strings.Contains(hintOf(err), "apps open demo") {
		t.Errorf("hint = %q", hintOf(err))
	}
}

// A manifest advisory from the server (a setting accepted but inert as
// declared) reaches both output modes: table mode prints it as a note under
// the deploy summary, and the JSON envelope carries it verbatim in `warnings`
// so a pipeline learns it without parsing prose. The negative control pins
// that the key is absent when the server sends no advisory.
func TestDeploy_ManifestWarningsReachBothOutputModes(t *testing.T) {
	const advisory = "min_warm_replicas=1 has no effect under worker.isolation=grouped: set worker.warm_spares to keep workers pre-booted"
	body := `{"slug":"demo","status":"idle","deploy_count":4,"current_version":"v4",` +
		`"manifest":{"app":{"min_warm_replicas":1},"warnings":["` + advisory + `"]}}`

	srv := deployStubServer(t, body, nil)
	writeTestCLIConfig(t, srv.URL)
	stdout, _, err := execCLISplit(t, "deploy", deployTestBundleDir(t), "--slug", "demo", "-o", "table")
	if err != nil {
		t.Fatalf("deploy failed: %v", err)
	}
	if !strings.Contains(stdout, "Note: "+advisory) {
		t.Errorf("table mode should print the advisory as a note; got %q", stdout)
	}

	stdout, _, err = execCLISplit(t, "deploy", deployTestBundleDir(t), "--slug", "demo")
	if err != nil {
		t.Fatalf("deploy failed: %v", err)
	}
	var obj map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSpace(stdout)), &obj); err != nil {
		t.Fatalf("stdout is not a JSON object: %q: %v", stdout, err)
	}
	warnings, _ := obj["warnings"].([]any)
	if len(warnings) != 1 || warnings[0] != advisory {
		t.Errorf("warnings = %v, want [%q]; full stdout: %q", obj["warnings"], advisory, stdout)
	}

	srv2 := deployStubServer(t, `{"slug":"demo","status":"running","deploy_count":4,"current_version":"v4"}`, nil)
	writeTestCLIConfig(t, srv2.URL)
	stdout, _, err = execCLISplit(t, "deploy", deployTestBundleDir(t), "--slug", "demo")
	if err != nil {
		t.Fatalf("deploy failed: %v", err)
	}
	obj = nil
	if err := json.Unmarshal([]byte(strings.TrimSpace(stdout)), &obj); err != nil {
		t.Fatalf("stdout is not a JSON object: %q: %v", stdout, err)
	}
	if _, present := obj["warnings"]; present {
		t.Errorf("warnings must be omitted when the server sends none; got %v", obj["warnings"])
	}
}
