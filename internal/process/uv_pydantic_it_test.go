package process

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestEnsureProject_ShinyAppGetsPydantic verifies end-to-end that a requirements-
// based shiny app, after EnsureProject, resolves pydantic into its lock/venv - so
// `import shiny.ui` (which loads shinychat, which imports pydantic) does not crash.
// Gated: needs uv + network (resolves shiny from PyPI); set WWC_UV_IT=1 to run.
func TestEnsureProject_ShinyAppGetsPydantic(t *testing.T) {
	if os.Getenv("WWC_UV_IT") == "" {
		t.Skip("set WWC_UV_IT=1 to run (needs uv + network to resolve shiny)")
	}
	if _, err := exec.LookPath("uv"); err != nil {
		t.Skip("uv not installed")
	}
	dir := t.TempDir()
	write := func(name, content string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("requirements.txt", "shiny>=1.2\n")
	write("app.py", "from shiny import App, ui\n")

	if err := EnsureProject(context.Background(), dir); err != nil {
		t.Fatalf("EnsureProject: %v", err)
	}

	lock, err := os.ReadFile(filepath.Join(dir, "uv.lock"))
	if err != nil {
		t.Fatalf("read uv.lock: %v", err)
	}
	if !strings.Contains(string(lock), `name = "pydantic"`) {
		t.Fatalf("pydantic missing from uv.lock after EnsureProject for a shiny app")
	}

	// The .venv must be able to import shiny.ui (which loads shinychat -> pydantic).
	cmd := exec.Command("uv", "run", "--frozen", "--no-sync", "python", "-c", "import shiny.ui")
	cmd.Dir = dir
	cmd.Env = SanitizedEnv()
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("import shiny.ui failed in synthesized venv: %v\n%s", err, out)
	}
}
