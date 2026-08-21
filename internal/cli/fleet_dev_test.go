package cli

import (
	"bytes"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

func TestFleetDevHelpExposesFleetAwareLocalRunInterface(t *testing.T) {
	out, err := execCLI(t, "fleet", "dev", "--help")
	if err != nil {
		t.Fatalf("fleet dev --help: %v\n%s", err, out)
	}
	for _, want := range []string{
		"dev APP", "--file", "--port", "--no-sync", "--no-reload", "--env",
		"--env-file", "--data-dir", "--state-dir", "--fresh", "--open", "--check",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("fleet dev help missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "--slug") {
		t.Fatalf("fleet dev slug comes from APP and must not expose --slug:\n%s", out)
	}
}

func TestFleetDevCheckComposesSelectedAppsSharedFilesOffline(t *testing.T) {
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 not in PATH")
	}
	root := t.TempDir()
	appDir := filepath.Join(root, "apps", "sales")
	if err := os.MkdirAll(appDir, 0o755); err != nil {
		t.Fatal(err)
	}
	server := `import http.server, os
assert open("helpers/theme.py").read() == "orange\n"
assert os.environ["FLEET_DEV_ENV"] == "selected-app"
http.server.HTTPServer(("127.0.0.1", int(os.environ["PORT"])), http.server.SimpleHTTPRequestHandler).serve_forever()
`
	for path, body := range map[string]string{
		filepath.Join(appDir, "server.py"):         server,
		filepath.Join(appDir, "shinyhub.toml"):     "[app]\ncommand = [\"python3\", \"server.py\"]\n",
		filepath.Join(appDir, ".env"):              "FLEET_DEV_ENV=selected-app\n",
		filepath.Join(root, "_shared", "theme.py"): "orange\n",
		filepath.Join(root, ".env"):                "FLEET_DEV_ENV=manifest-root\n",
	} {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	manifest := filepath.Join(root, "fleet.toml")
	if err := os.WriteFile(manifest, []byte(`
fleet_id = "analytics"
[[bundle_file]]
from = "_shared/theme.py"
to = "helpers/theme.py"
consumers = ["sales"]
[[app]]
slug = "sales"
source = "./apps/sales"
[[app]]
slug = "unrelated"
source = "./apps/does-not-exist"
`), 0o644); err != nil {
		t.Fatal(err)
	}

	cmd := newFleetDevCmd()
	var stdout, stderr bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)
	cmd.SetArgs([]string{"sales", "-f", manifest, "--check", "--no-sync", "--state-dir", t.TempDir()})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("fleet dev --check: %v\nstdout:\n%s\nstderr:\n%s", err, stdout.String(), stderr.String())
	}
	for _, want := range []string{"_shared/theme.py -> helpers/theme.py", "starting manifest command"} {
		if !strings.Contains(stdout.String(), want) {
			t.Fatalf("fleet dev output missing %q:\nstdout:\n%s\nstderr:\n%s", want, stdout.String(), stderr.String())
		}
	}
}

func TestFleetDevSymlinkedManifestUsesInvocationDirectoryForSourcesAndInputs(t *testing.T) {
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 not in PATH")
	}
	invocationRoot := t.TempDir()
	manifestStore := t.TempDir()
	appDir := filepath.Join(invocationRoot, "app")
	if err := os.MkdirAll(filepath.Join(appDir), 0o755); err != nil {
		t.Fatal(err)
	}
	for path, body := range map[string]string{
		filepath.Join(appDir, "server.py"): `import http.server, os
assert open("helpers/theme.py").read() == "orange\n"
http.server.HTTPServer(("127.0.0.1", int(os.environ["PORT"])), http.server.SimpleHTTPRequestHandler).serve_forever()
`,
		filepath.Join(appDir, "shinyhub.toml"):               "[app]\ncommand = [\"python3\", \"server.py\"]\n",
		filepath.Join(invocationRoot, "_shared", "theme.py"): "orange\n",
	} {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	realManifest := filepath.Join(manifestStore, "fleet.toml")
	if err := os.WriteFile(realManifest, []byte(`
fleet_id = "analytics"
[[bundle_file]]
from = "_shared/theme.py"
to = "helpers/theme.py"
consumers = ["sales"]
[[app]]
slug = "sales"
source = "app"
`), 0o644); err != nil {
		t.Fatal(err)
	}
	linkedManifest := filepath.Join(invocationRoot, "fleet.toml")
	if err := os.Symlink(realManifest, linkedManifest); err != nil {
		t.Skipf("create manifest symlink: %v", err)
	}

	cmd := newFleetDevCmd()
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	cmd.SetArgs([]string{"sales", "-f", linkedManifest, "--check", "--no-sync", "--state-dir", t.TempDir()})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("fleet dev through symlinked manifest: %v", err)
	}
}

func TestFleetDevRejectsUnknownAndGitAppsBeforeLocalState(t *testing.T) {
	root := t.TempDir()
	manifest := filepath.Join(root, "fleet.toml")
	if err := os.WriteFile(manifest, []byte(`
fleet_id = "analytics"
[[app]]
slug = "remote"
source = "git+https://example.com/org/apps.git@main#remote"
`), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name string
		slug string
		want string
	}{
		{name: "unknown", slug: "missing", want: "is not declared"},
		{name: "git", slug: "remote", want: "supports local app sources only"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			state := filepath.Join(root, "state-"+tc.name)
			cmd := newFleetDevCmd()
			cmd.SetOut(io.Discard)
			cmd.SetErr(io.Discard)
			cmd.SetArgs([]string{tc.slug, "-f", manifest, "--state-dir", state})
			err := cmd.Execute()
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %v, want %q", err, tc.want)
			}
			if _, statErr := os.Stat(state); !os.IsNotExist(statErr) {
				t.Fatalf("validation created local state: %v", statErr)
			}
		})
	}
}

func TestFleetDevRejectsIrrelevantServerFlags(t *testing.T) {
	parent := &cobra.Command{Use: "shinyhub"}
	AddCommandsTo(parent)
	parent.SetArgs([]string{"fleet", "dev", "sales", "--host", "https://hub.example.com"})
	parent.SetOut(io.Discard)
	parent.SetErr(io.Discard)
	err := parent.Execute()
	var exitErr *ExitCodeError
	if !errors.As(err, &exitErr) || exitErr.Kind != KindValidation || !strings.Contains(err.Error(), "does not apply") {
		t.Fatalf("error = %v, want actionable validation error", err)
	}
	hostFlagOverride = ""
}
