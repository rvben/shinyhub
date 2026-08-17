package cli

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rvben/shinyhub/internal/bundle"
)

func planTestBundle(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "app.py"), []byte("from shiny import App\napp = App()\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "requirements.txt"), []byte("shiny\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestPlanUnchangedJSONAndDetailedExitCode(t *testing.T) {
	dir := planTestBundle(t)
	preview, err := buildBundlePreview(dir)
	if err != nil {
		t.Fatal(err)
	}
	var methods []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		methods = append(methods, r.Method+" "+r.URL.Path)
		switch r.URL.Path {
		case "/api/server-info":
			_, _ = io.WriteString(w, `{"version":"dev","capabilities":{"content_digest":true},"runtimes":{"python":true}}`)
		case "/api/apps/demo":
			_, _ = io.WriteString(w, `{"app":{"slug":"demo","status":"running","access":"private","deploy_count":4,"content_digest":"`+preview.Digest+`"},"can_manage":true}`)
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
	}))
	defer srv.Close()
	writeTestCLIConfig(t, srv.URL)

	stdout, stderr, err := execCLISplit(t, "plan", dir, "--slug", "demo", "--detailed-exitcode", "-o", "json")
	if err != nil {
		t.Fatalf("plan: %v (stdout=%q stderr=%q)", err, stdout, stderr)
	}
	var got deploymentPlan
	if err := json.Unmarshal([]byte(stdout), &got); err != nil {
		t.Fatalf("decode plan: %v\n%s", err, stdout)
	}
	if got.Action != "redeploy" || got.ChangeStatus != "unchanged" || got.Changes == nil || *got.Changes {
		t.Fatalf("change result = action %q status %q changes %v", got.Action, got.ChangeStatus, got.Changes)
	}
	if got.ExitCode != 0 || got.Bundle.Digest != preview.Digest {
		t.Fatalf("exit/digest = %d/%q, want 0/%q", got.ExitCode, got.Bundle.Digest, preview.Digest)
	}
	if got.Plan.SchemaVersion != planModelSchemaVersion || got.Plan.Scope != "single-app" || got.Plan.Counts.Update != 1 {
		t.Fatalf("shared plan model missing from single-app JSON: %+v", got.Plan)
	}
	for _, request := range methods {
		if !strings.HasPrefix(request, "GET ") {
			t.Fatalf("plan made a mutating request: %s", request)
		}
	}
}

func TestPlanNewAppIsReadOnlyAndSignalsChanges(t *testing.T) {
	dir := planTestBundle(t)
	var methods []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		methods = append(methods, r.Method+" "+r.URL.Path)
		switch r.URL.Path {
		case "/api/server-info":
			_, _ = io.WriteString(w, `{"version":"dev","capabilities":{"content_digest":true},"runtimes":{"python":true}}`)
		case "/api/apps/new-app":
			http.Error(w, `{"error":"not found"}`, http.StatusNotFound)
		case "/api/auth/me":
			_, _ = io.WriteString(w, `{"user":{"username":"alice","role":"developer"},"can_create_apps":true}`)
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
	}))
	defer srv.Close()
	writeTestCLIConfig(t, srv.URL)

	stdout, stderr, err := execCLISplit(t, "plan", dir, "--slug", "new-app", "--visibility", "public", "--fail-on-changes", "-o", "json")
	if ExitCode(err) != 2 {
		t.Fatalf("exit = %d (%v), want 2 (stdout=%q stderr=%q)", ExitCode(err), err, stdout, stderr)
	}
	var got deploymentPlan
	if err := json.Unmarshal([]byte(stdout), &got); err != nil {
		t.Fatalf("decode plan: %v\n%s", err, stdout)
	}
	if got.Action != "create" || got.ChangeStatus != "new" || got.ExitCode != 2 || got.Visibility != "public" {
		t.Fatalf("unexpected plan: %+v", got)
	}
	if !strings.Contains(got.DeployCommand, "--visibility public") || !strings.Contains(got.DeployCommand, "--wait") {
		t.Fatalf("deploy command is not reusable: %q", got.DeployCommand)
	}
	for _, request := range methods {
		if !strings.HasPrefix(request, "GET ") {
			t.Fatalf("plan made a mutating request: %s", request)
		}
	}
}

func TestPlanLegacyServerReportsUnknownInsteadOfBlocking(t *testing.T) {
	dir := planTestBundle(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/server-info":
			http.NotFound(w, r)
		case "/api/apps/demo":
			// Legacy shape: neither can_manage nor content_digest is present.
			_, _ = io.WriteString(w, `{"app":{"slug":"demo","status":"running","access":"private","deploy_count":1}}`)
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
	}))
	defer srv.Close()
	writeTestCLIConfig(t, srv.URL)

	stdout, stderr, err := execCLISplit(t, "plan", dir, "--slug", "demo", "--detailed-exitcode", "-o", "json")
	if ExitCode(err) != 2 {
		t.Fatalf("exit = %d (%v), want conservative 2 (stdout=%q stderr=%q)", ExitCode(err), err, stdout, stderr)
	}
	var got deploymentPlan
	if err := json.Unmarshal([]byte(stdout), &got); err != nil {
		t.Fatal(err)
	}
	if got.ChangeStatus != "unknown" || got.Changes != nil || !strings.Contains(got.Permission, "not reported") {
		t.Fatalf("legacy comparison was not honest: %+v", got)
	}
}

func TestPlanBundleExplainsIgnoredAndProtectedPaths(t *testing.T) {
	dir := planTestBundle(t)
	if err := os.WriteFile(filepath.Join(dir, ".shinyhubignore"), []byte("secret.env\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "secret.env"), []byte("TOKEN=secret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(dir, "data"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "data", "large.csv"), []byte("private data"), 0o644); err != nil {
		t.Fatal(err)
	}

	preview, err := buildBundlePreview(dir)
	if err != nil {
		t.Fatal(err)
	}
	if preview.IgnoreFile != ".shinyhubignore" || len(preview.IgnoredPaths) != 1 || preview.IgnoredPaths[0] != "secret.env" {
		t.Fatalf("ignored metadata = file %q paths %#v", preview.IgnoreFile, preview.IgnoredPaths)
	}
	if len(preview.ProtectedPaths) == 0 || !strings.Contains(summarizeSkippedPaths(preview.ProtectedPaths), "data") {
		t.Fatalf("protected metadata = %#v", preview.ProtectedPaths)
	}
	for _, path := range preview.Files {
		if path == "secret.env" || strings.HasPrefix(path, "data/") {
			t.Fatalf("excluded path included in bundle: %q", path)
		}
	}
}

func TestPlanDigestMatchesSubsequentDeployUpload(t *testing.T) {
	dir := planTestBundle(t)
	var uploadedDigest string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/server-info":
			_, _ = io.WriteString(w, `{"version":"dev","capabilities":{"content_digest":true},"runtimes":{"python":true}}`)
		case r.Method == http.MethodGet && r.URL.Path == "/api/apps/demo":
			_, _ = io.WriteString(w, `{"app":{"slug":"demo","status":"running","access":"shared","content_digest":"sha256:old"},"can_manage":true}`)
		case r.Method == http.MethodPost && r.URL.Path == "/api/apps/demo/deploy":
			file, _, err := r.FormFile("bundle")
			if err != nil {
				t.Errorf("bundle form file: %v", err)
				http.Error(w, "bad bundle", http.StatusBadRequest)
				return
			}
			raw, _ := io.ReadAll(file)
			_ = file.Close()
			zr, err := zip.NewReader(bytes.NewReader(raw), int64(len(raw)))
			if err != nil {
				t.Errorf("read zip: %v", err)
				return
			}
			uploadedDigest, err = bundle.DigestZipReader(zr)
			if err != nil {
				t.Errorf("digest zip: %v", err)
			}
			_, _ = io.WriteString(w, `{"slug":"demo","status":"running","deploy_count":2,"current_version":"v2"}`)
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
	}))
	defer srv.Close()
	writeTestCLIConfig(t, srv.URL)

	stdout, _, err := execCLISplit(t, "plan", dir, "--slug", "demo", "-o", "json")
	if err != nil {
		t.Fatal(err)
	}
	var planned deploymentPlan
	if err := json.Unmarshal([]byte(stdout), &planned); err != nil {
		t.Fatal(err)
	}
	if _, _, err := execCLISplit(t, "deploy", dir, "--slug", "demo", "-o", "table"); err != nil {
		t.Fatal(err)
	}
	if uploadedDigest == "" || uploadedDigest != planned.Bundle.Digest {
		t.Fatalf("uploaded digest %q != planned digest %q", uploadedDigest, planned.Bundle.Digest)
	}
}

func TestPlanRequiresExplicitDirectory(t *testing.T) {
	_, _, err := execCLISplit(t, "plan", "-o", "table")
	if err == nil || !strings.Contains(err.Error(), "pass `.`") {
		t.Fatalf("error = %v, want explicit-directory guidance", err)
	}
}

func TestRenderDeploymentPlanExplainsIdenticalRedeploy(t *testing.T) {
	unchanged := false
	plan := deploymentPlan{
		Status: "planned", Action: "redeploy", ChangeStatus: "unchanged", Changes: &unchanged,
		Host: "https://hub.example.com", AppURL: "https://hub.example.com/app/demo/", Slug: "demo",
		Source: "/work/demo", Permission: "manage existing app", Visibility: "private",
		Lifecycle: "deploy new version and replace running replicas",
		Bundle:    &bundlePreview{Digest: "sha256:same", FileCount: 1, Files: []string{"app.py"}},
		Launch:    deploymentLaunchPreview{Runtime: "python", Command: []string{"uv", "run", "shiny"}, ReadinessPath: "/", ReadinessStatus: "any 2xx or 3xx", StartupTimeoutSeconds: 120},
		Manifest:  deploymentManifestPreview{Effects: []string{}}, DeployCommand: "shinyhub deploy /work/demo --slug demo --wait",
	}
	var out bytes.Buffer
	renderDeploymentPlan(&out, plan)
	for _, want := range []string{"Deployment plan (read-only)", "Bundle", "Launch", "Manifest", "No content change", "would still record a deployment", "Run: shinyhub deploy"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("output should contain %q:\n%s", want, out.String())
		}
	}
}
