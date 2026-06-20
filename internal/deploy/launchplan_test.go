package deploy

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
)

// writeRunBundle writes files into a fresh temp bundle dir.
func writeRunBundle(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func TestResolveLaunch_CommandOverride_SubstitutesNoValidate(t *testing.T) {
	dir := writeRunBundle(t, map[string]string{"app.py": "x=1\n"})
	plan, err := ResolveLaunch(dir, LaunchOptions{
		CommandOverride: []string{"mycmd", "--port", "{port}", "--host", "{host}", "--data", "{data_dir}"},
		Port:            9001, BindHost: "127.0.0.1",
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"mycmd", "--port", "9001", "--host", "127.0.0.1", "--data", "data"}
	if !slices.Equal(plan.Command, want) {
		t.Fatalf("command = %v, want %v", plan.Command, want)
	}
	if len(plan.DepPrep) != 0 {
		t.Fatalf("override path must have no dep-prep, got %v", plan.DepPrep)
	}
}

func TestResolveLaunch_ManifestCommand_NoPrep(t *testing.T) {
	dir := writeRunBundle(t, map[string]string{
		"app.py":           "x=1\n",
		"requirements.txt": "shiny\n",
		"shinyhub.toml":    "[app]\ncommand = [\"streamlit\", \"run\", \"app.py\", \"--server.port\", \"{port}\"]\n",
	})
	plan, err := ResolveLaunch(dir, LaunchOptions{Port: 9002, BindHost: "127.0.0.1", PrepHostDeps: true})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"streamlit", "run", "app.py", "--server.port", "9002"}
	if !slices.Equal(plan.Command, want) {
		t.Fatalf("command = %v, want %v", plan.Command, want)
	}
	if len(plan.DepPrep) != 0 {
		t.Fatalf("manifest-command path must skip dep-prep even with PrepHostDeps, got %v", plan.DepPrep)
	}
	if plan.AppType != "" {
		t.Fatalf("manifest-command path must not set AppType, got %q", plan.AppType)
	}
}
