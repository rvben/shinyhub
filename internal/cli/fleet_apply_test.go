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

func TestFleetApply_NoManifestHelpful(t *testing.T) {
	_, _, _ = setupCLITest(t)
	out, err := execCLI(t, "fleet", "apply", "-f", "does-not-exist.toml")
	if err == nil || exitCode(err) != 1 {
		t.Fatalf("want exit 1, got %v", err)
	}
	if !strings.Contains(out, "fleet init") {
		t.Fatalf("not helpful: %q", out)
	}
}

func TestFleetApply_DryRunIsPlan(t *testing.T) {
	_, _, setResp := setupCLITest(t)
	setResp(200, `[]`)
	dir := t.TempDir()
	mustWrite(t, dir+"/a/app.py", "print(1)\n")
	writeFleetManifest(t, dir, "fleet_id=\"eu\"\n\n[[app]]\nslug=\"ops\"\nsource=\"./a\"\n")
	out, err := execCLI(t, "fleet", "apply", "--dry-run", "-f", dir+"/shinyhub-fleet.toml")
	if err != nil {
		t.Fatalf("dry-run should be a clean plan: %v\n%s", err, out)
	}
	if !strings.Contains(out, "Plan:") {
		t.Fatalf("dry-run must print the plan summary: %q", out)
	}
	// FLT-10: the dry-run header must name the originating command, not
	// masquerade as `shinyhub fleet plan`.
	if !strings.Contains(out, "shinyhub fleet apply --dry-run  ·") {
		t.Fatalf("dry-run header must say `fleet apply --dry-run`: %q", out)
	}
	if strings.Contains(out, "shinyhub fleet plan  ·") {
		t.Fatalf("dry-run must not print the `fleet plan` header: %q", out)
	}
}

func TestFleetApply_PruneNonTTYWithoutYesFailsWithExactCommand(t *testing.T) {
	// Use a per-path server so /api/server-info can advertise fleet_preconditions.
	// With preconditions enabled, willPrune is true and the non-TTY --yes guard fires.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/apps":
			w.WriteHeader(200)
			_, _ = w.Write([]byte(`[{"slug":"gone","access":"private","managed_by":"fleet:eu","content_digest":"sha256:X"}]`))
		case "/api/server-info":
			w.WriteHeader(200)
			_, _ = w.Write([]byte(`{"capabilities":{"fleet_preconditions":true,"content_digest":true}}`))
		default:
			w.WriteHeader(200)
			_, _ = w.Write([]byte(`{}`))
		}
	}))
	t.Cleanup(srv.Close)

	home := t.TempDir()
	t.Setenv("HOME", home)
	cfgDir := filepath.Join(home, ".config", "shinyhub")
	if err := os.MkdirAll(cfgDir, 0700); err != nil {
		t.Fatal(err)
	}
	cfg := cliConfig{Host: srv.URL, Token: "shk_test"}
	f, err := os.Create(filepath.Join(cfgDir, "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.NewEncoder(f).Encode(cfg); err != nil {
		t.Fatal(err)
	}
	f.Close()

	dir := t.TempDir()
	writeFleetManifest(t, dir, "fleet_id=\"eu\"\n")
	prev := isStdinTTY
	isStdinTTY = func() bool { return false }
	t.Cleanup(func() { isStdinTTY = prev })

	out, err := execCLI(t, "fleet", "apply", "--prune", "-f", dir+"/shinyhub-fleet.toml")
	if err == nil {
		t.Fatalf("non-tty --prune without --yes must fail; out=%q", out)
	}
	if !strings.Contains(out, "--yes") {
		t.Fatalf("error must show the exact --yes invocation: %q", out)
	}
}
