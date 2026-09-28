package localrun

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// A local uv launch resolves from the index the bundle's requirements.txt
// names, as a server launch does. The fake uv serves only when it receives
// that index with the credential expanded from the local env, so a healthy
// check proves the configuration arrived.
func TestRun_UVLaunchAppliesRequirementsIndex(t *testing.T) {
	skipIfNoPython3(t)
	bin := t.TempDir()
	fakeUV := "#!/bin/sh\n" +
		"[ \"$UV_DEFAULT_INDEX\" = 'https://__token__:s3cret@private.example/simple' ] || exit 3\n" +
		"exec python3 server.py\n"
	if err := os.WriteFile(filepath.Join(bin, "uv"), []byte(fakeUV), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("UV_DEFAULT_INDEX", "")
	os.Unsetenv("UV_DEFAULT_INDEX")

	dir := writeHealthyFixture(t)
	if err := os.WriteFile(filepath.Join(dir, "shinyhub.toml"),
		[]byte("[app]\ncommand = [\"uv\", \"run\", \"server.py\"]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "requirements.txt"),
		[]byte("--index-url https://__token__:${PYPI_TOKEN}@private.example/simple\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	err := Run(ctx, Options{
		BundleDir: dir, StateDir: t.TempDir(), Slug: "reqidx",
		Check: true, NoReload: true, NoSync: true, Env: []string{"PYPI_TOKEN=s3cret"},
	}, os.Stdout, os.Stderr)
	if err != nil {
		t.Fatalf("the uv launch did not receive the requirements index: %v", err)
	}
}
