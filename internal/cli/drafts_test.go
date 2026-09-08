package cli

import (
	"archive/zip"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rvben/shinyhub/internal/bundle"
	"github.com/rvben/shinyhub/internal/db"
)

func TestDeployDraftUsesDedicatedEndpointAndRetainsDigest(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "app.py"), []byte("from shiny import App\n"), 0600); err != nil {
		t.Fatal(err)
	}
	var calls []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls = append(calls, r.Method+" "+r.URL.Path)
		if r.URL.Path != "/api/apps/demo/drafts" {
			t.Errorf("unexpected request: %s", r.URL.Path)
			http.NotFound(w, r)
			return
		}
		if r.URL.Query().Get("ttl") != "2h0m0s" {
			t.Errorf("ttl=%s", r.URL.Query().Get("ttl"))
		}
		if err := r.ParseMultipartForm(1 << 20); err != nil {
			t.Error(err)
			return
		}
		defer r.MultipartForm.RemoveAll()
		f, h, err := r.FormFile("bundle")
		if err != nil {
			t.Error(err)
			return
		}
		defer f.Close()
		zr, err := zip.NewReader(f, h.Size)
		if err != nil {
			t.Error(err)
			return
		}
		digest, err := bundle.DigestZipReader(zr)
		if err != nil {
			t.Error(err)
			return
		}
		w.WriteHeader(201)
		json.NewEncoder(w).Encode(db.DeploymentDraft{ID: "draft123", ContentDigest: digest})
	}))
	defer srv.Close()
	t.Setenv("SHINYHUB_HOST", srv.URL)
	t.Setenv("SHINYHUB_TOKEN", "shk_test")
	stdout, _, err := execCLISplit(t, "deploy", dir, "--slug", "demo", "--draft", "--draft-ttl", "2h", "-o", "json")
	if err != nil {
		t.Fatal(err)
	}
	if len(calls) != 1 {
		t.Fatalf("draft unexpectedly mutated other endpoints: %v", calls)
	}
	var d db.DeploymentDraft
	if err := json.Unmarshal([]byte(stdout), &d); err != nil {
		t.Fatal(err)
	}
	if d.ID != "draft123" || d.ContentDigest == "" {
		t.Fatalf("output: %s", stdout)
	}
}

func TestDraftOldServerCannotFallBackToProductionDeploy(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "app.py"), []byte("from shiny import App\n"), 0600)
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.URL.Path != "/api/apps/demo/drafts" {
			t.Errorf("unsafe fallback: %s", r.URL.Path)
		}
		http.NotFound(w, r)
	}))
	defer srv.Close()
	t.Setenv("SHINYHUB_HOST", srv.URL)
	t.Setenv("SHINYHUB_TOKEN", "shk_test")
	_, err := execCLI(t, "deploy", dir, "--slug", "demo", "--draft")
	if err == nil || calls != 1 {
		t.Fatalf("err=%v calls=%d", err, calls)
	}
}

func TestDraftFlagsRejectAmbiguousMutations(t *testing.T) {
	for _, flag := range []string{"--watch", "--start", "--wait", "--allow-downtime", "--wait-for-warm"} {
		_, err := execCLI(t, "deploy", ".", "--draft", flag)
		if err == nil || !strings.Contains(err.Error(), "--draft cannot") {
			t.Errorf("%s: %v", flag, err)
		}
	}
}

func TestDraftPromotePassesOnlyExplicitDowntime(t *testing.T) {
	for _, allow := range []bool{false, true} {
		t.Run(map[bool]string{false: "guarded", true: "explicit"}[allow], func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != "POST" || r.URL.Path != "/api/apps/demo/drafts/draft123/promote" {
					t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
				}
				if (r.Header.Get("X-ShinyHub-Allow-Downtime") == "1") != allow {
					t.Errorf("downtime header mismatch")
				}
				w.Write([]byte(`{"status":"stopped","kept_stopped":true}`))
			}))
			defer srv.Close()
			t.Setenv("SHINYHUB_HOST", srv.URL)
			t.Setenv("SHINYHUB_TOKEN", "shk_test")
			args := []string{"drafts", "promote", "demo", "draft123", "-o", "json"}
			if allow {
				args = append(args, "--allow-downtime")
			}
			stdout, err := execCLI(t, args...)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(stdout, `"kept_stopped":true`) {
				t.Fatalf("lost deployment result: %s", stdout)
			}
		})
	}
}
