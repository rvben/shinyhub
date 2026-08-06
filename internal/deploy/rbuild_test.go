package deploy

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/rvben/shinyhub/internal/process"
)

// stubRscript puts a recording Rscript first on PATH. It writes the arguments
// it was called with, one per line, into argv.txt in its working directory -
// which the build step sets to the bundle - so a test can assert on the exact
// invocation the production path produced.
func stubRscript(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the stub is a POSIX shell script")
	}
	bin := t.TempDir()
	script := "#!/bin/sh\nprintf '%s\\n' \"$@\" > argv.txt\n"
	if err := os.WriteFile(filepath.Join(bin, "Rscript"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
}

func recordedArgv(t *testing.T, dir string) []string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, "argv.txt"))
	if err != nil {
		t.Fatalf("Rscript was not invoked: %v", err)
	}
	return strings.Split(strings.TrimRight(string(b), "\n"), "\n")
}

func rTestBundle(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for name, body := range files {
		p := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// TestBuildConfinement_DisablesTheRenvSandbox pins the setting onto the build
// environment. renv's sandbox is created while R sources the project profile,
// so it has to arrive as environment; the options() call this replaced was
// evaluated after activation had already built the mode 0555 tree that later
// made the app undeletable.
func TestBuildConfinement_DisablesTheRenvSandbox(t *testing.T) {
	_, env := buildConfinement(t.TempDir())
	if got := findEnv(env, "RENV_CONFIG_SANDBOX_ENABLED"); got != "FALSE" {
		t.Fatalf("RENV_CONFIG_SANDBOX_ENABLED = %q, want FALSE (env = %q)", got, env)
	}
}

// TestSandboxedRSync_RestoresIntoTheInterposedLibrary drives the production
// build step for a bare-lockfile bundle and asserts both halves of the fix: the
// library exists before renv runs, and renv is told to install into it.
func TestSandboxedRSync_RestoresIntoTheInterposedLibrary(t *testing.T) {
	stubRscript(t)
	dir := rTestBundle(t, map[string]string{
		"app.R":     "shiny::shinyApp(ui, server)\n",
		"renv.lock": `{"R":{"Version":"4.6.1"},"Packages":{}}`,
	})

	if err := sandboxedRSync(context.Background(), dir, nil); err != nil {
		t.Fatalf("sandboxedRSync: %v", err)
	}

	lib := filepath.Join(dir, process.RProjectLibraryDir)
	if fi, err := os.Stat(lib); err != nil || !fi.IsDir() {
		t.Fatalf("interposed library not created before the restore: stat err = %v", err)
	}
	argv := recordedArgv(t, dir)
	if len(argv) != 2 || argv[0] != "-e" {
		t.Fatalf("argv = %q, want -e <expr>", argv)
	}
	if !strings.Contains(argv[1], "library = '"+process.RProjectLibraryDir+"'") {
		t.Fatalf("restore is not aimed at the interposed library: %s", argv[1])
	}
}

// TestSandboxedRSync_RenvActivatedBundleIsUntouched pins that the previously
// working path is byte-identical: renv picks its own library, and no ShinyHub
// directory appears in the bundle.
func TestSandboxedRSync_RenvActivatedBundleIsUntouched(t *testing.T) {
	stubRscript(t)
	dir := rTestBundle(t, map[string]string{
		"app.R":           "shiny::shinyApp(ui, server)\n",
		"renv.lock":       `{"R":{"Version":"4.6.1"},"Packages":{}}`,
		".Rprofile":       "source(\"renv/activate.R\")\n",
		"renv/activate.R": "# renv bootstrap\n",
	})

	if err := sandboxedRSync(context.Background(), dir, nil); err != nil {
		t.Fatalf("sandboxedRSync: %v", err)
	}

	if _, err := os.Stat(filepath.Join(dir, process.RProjectLibraryDir)); err == nil {
		t.Fatal("interposed a library into an renv-activated bundle")
	}
	argv := recordedArgv(t, dir)
	if strings.Contains(argv[1], "library =") || strings.Contains(argv[1], ".libPaths(") {
		t.Fatalf("overrode renv's own library selection: %s", argv[1])
	}
}

// TestSandboxedRSync_NoLockfileIsNoOp keeps a bundle that manages its own
// packages free of both the exec and the directory.
func TestSandboxedRSync_NoLockfileIsNoOp(t *testing.T) {
	stubRscript(t)
	dir := rTestBundle(t, map[string]string{"app.R": "shiny::shinyApp(ui, server)\n"})

	if err := sandboxedRSync(context.Background(), dir, nil); err != nil {
		t.Fatalf("sandboxedRSync: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "argv.txt")); err == nil {
		t.Fatal("Rscript ran for a bundle with no renv.lock")
	}
	if _, err := os.Stat(filepath.Join(dir, process.RProjectLibraryDir)); err == nil {
		t.Fatal("created a library for a bundle with no renv.lock")
	}
}
