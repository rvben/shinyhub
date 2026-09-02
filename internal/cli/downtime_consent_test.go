package cli

import (
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/spf13/cobra"
)

func TestSavedPlanApplySendsExplicitDowntimeConsent(t *testing.T) {
	var got string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Get("X-ShinyHub-Allow-Downtime")
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)
	loaded := &loadedSavedPlan{Envelope: savedPlanEnvelope{Target: savedPlanTarget{Slug: "demo"}}, Bundle: []byte("zip")}
	resp, err := postSavedPlanBundle(&cliConfig{Host: srv.URL, Token: "token"}, loaded, "revision", true)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if got != "1" {
		t.Fatalf("saved-plan downtime header = %q, want 1", got)
	}
}

func TestFleetDeploySendsExplicitDowntimeConsent(t *testing.T) {
	var got string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/api/apps/demo/deploy":
			got = r.Header.Get("X-ShinyHub-Allow-Downtime")
			_, _ = w.Write([]byte(`{"status":"ok"}`))
		case r.Method == http.MethodGet && r.URL.Path == "/api/apps/demo":
			_, _ = w.Write([]byte(`{"app":{"slug":"demo","status":"running","access":"private"}}`))
		case r.Method == http.MethodGet && r.URL.Path == "/api/apps":
			_, _ = w.Write([]byte(`[{"slug":"demo","access":"private","content_digest":"sha256:new"}]`))
		default:
			_, _ = w.Write([]byte(`{}`))
		}
	}))
	t.Cleanup(srv.Close)
	dir := t.TempDir()
	mustWrite(t, filepath.Join(dir, "app.py"), "print('ok')\n")
	_, _, _, _, err := deployAppBundleFromSpecWithDowntime(
		&cliConfig{Host: srv.URL, Token: "token"}, "demo", bundleBuildSpec{Dir: dir},
		"private", "", io.Discard, "run", time.Second, true,
	)
	if err != nil {
		t.Fatal(err)
	}
	if got != "1" {
		t.Fatalf("fleet downtime header = %q, want 1", got)
	}
}

func TestDowntimeConsentFlagsExist(t *testing.T) {
	commands := []struct {
		name string
		cmd  *cobra.Command
	}{
		{name: "deploy", cmd: newDeployCmd()},
		{name: "apply", cmd: newApplyCmd()},
		{name: "fleet apply", cmd: newFleetApplyCmd()},
	}
	for _, item := range commands {
		if flag := item.cmd.Flags().Lookup("allow-downtime"); flag == nil {
			t.Errorf("%s missing --allow-downtime", item.name)
		}
	}
}
