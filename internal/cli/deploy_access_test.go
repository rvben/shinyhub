package cli

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestDeploy_PrintsAccessLineForPrivateApp verifies deploy surfaces the app's
// visibility, so the printed URL returning 401 for a brand-new private app is no
// longer a confusing surprise.
func TestDeploy_PrintsAccessLineForPrivateApp(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/apps/demo", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"app":{"slug":"demo","status":"running","deploy_count":1,"access":"private"}}`))
	})
	mux.HandleFunc("/api/apps/demo/deploy", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"slug":"demo","status":"running","deploy_count":1,"access":"private"}`))
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
	if !strings.Contains(stdout, "Access: private") {
		t.Errorf("expected an access line for a private app, got %q", stdout)
	}
	// The hint for sharing a private app is a scoped member grant, never a
	// visibility widening (public would open the app to everyone, unauthenticated).
	if !strings.Contains(stdout, "apps access grant demo") {
		t.Errorf("expected a grant hint for sharing a private app, got %q", stdout)
	}
	if strings.Contains(stdout, "apps access set demo public") {
		t.Errorf("the private-app hint must not push toward public visibility, got %q", stdout)
	}
}
