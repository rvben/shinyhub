package deploy

import (
	"slices"
	"testing"
)

// TestServerBootUsesResolveLaunch_Python asserts that ResolveLaunch produces
// the same command as the server's buildCommand path for an inferred Python bundle.
// This pins that the two code paths share one definition of the boot command.
func TestServerBootUsesResolveLaunch_Python(t *testing.T) {
	dir := writeRunBundle(t, map[string]string{"app.py": "x=1\n", "requirements.txt": "shiny\n"})
	// The server's inferred python command for one replica.
	got := buildCommand(dir, 7000, 1, "127.0.0.1", false, true)
	plan, err := ResolveLaunch(dir, LaunchOptions{Port: 7000, Workers: 1, BindHost: "127.0.0.1", CommandHostDeps: true})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(plan.Command, got) {
		t.Fatalf("ResolveLaunch command %v != server buildCommand %v", plan.Command, got)
	}
}

// TestServerBootUsesResolveLaunch_ManifestCommand asserts that ResolveLaunch
// produces the same command as substituteCommand for a manifest-supplied command.
func TestServerBootUsesResolveLaunch_ManifestCommand(t *testing.T) {
	dir := writeRunBundle(t, map[string]string{
		"app.py":        "x=1\n",
		"shinyhub.toml": "[app]\ncommand = [\"gunicorn\", \"app:server\", \"-b\", \"{host}:{port}\"]\n",
	})
	got := substituteCommand([]string{"gunicorn", "app:server", "-b", "{host}:{port}"}, 7001, "127.0.0.1")
	plan, err := ResolveLaunch(dir, LaunchOptions{Port: 7001, BindHost: "127.0.0.1"})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(plan.Command, got) {
		t.Fatalf("manifest command mismatch: %v != %v", plan.Command, got)
	}
}
